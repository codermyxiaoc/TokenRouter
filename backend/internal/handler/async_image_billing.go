package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// asyncImageBillingState 保存一次网关执行的预占归属，账号重试不能重复冻结或提前释放。
type asyncImageBillingState struct {
	gateway      *service.OpenAIGatewayService
	reservation  *service.OpenAIImageBillingReservation
	prepared     bool
	uncertain    bool
	settled      bool
	priorUnknown bool
	eventCount   int
	attemptCount int
}

func (s *asyncImageBillingState) prepare(ctx context.Context, key *service.APIKey, account *service.Account, subscription *service.UserSubscription, model string, count int, sizeTier string, mapping service.ChannelMappingResult) error {
	if s.prepared {
		return nil
	}
	s.prepared = true
	reservation, err := s.gateway.PrepareAsyncImageBilling(ctx, key, account, subscription, model, count, sizeTier, mapping)
	if err != nil {
		return err
	}
	s.reservation = reservation
	return nil
}

// startForward 在可能发出请求前先保守标记未知，确保 panic 也不会释放可能已消费的预占。
func (s *asyncImageBillingState) startForward(c *gin.Context) {
	if s.reservation == nil {
		return
	}
	s.priorUnknown = s.uncertain
	s.uncertain = true
	s.eventCount = len(asyncImageUpstreamEvents(c))
	s.attemptCount = service.ImageUpstreamAttemptCount(c)
}

func (s *asyncImageBillingState) forwardFailed(c *gin.Context, err error) {
	if s.reservation != nil && err != nil && (service.ImageUpstreamAttemptCount(c) == s.attemptCount || asyncImageFailureDefinitelyRejected(c, err, s.eventCount)) {
		// 模型、凭据或地址校验在发送前失败时，本轮没有产生上游费用。
		// 前一账号的未知执行结果不能被后续账号的明确拒绝覆盖。
		s.uncertain = s.priorUnknown
	}
}

func asyncImageUpstreamEvents(c *gin.Context) []*service.OpsUpstreamErrorEvent {
	if c == nil {
		return nil
	}
	value, _ := c.Get(service.OpsUpstreamErrorsKey)
	events, _ := value.([]*service.OpsUpstreamErrorEvent)
	return events
}

// asyncImageFailureDefinitelyRejected 只接受本轮明确拒绝证据，不能把网关包装的 502 当成上游未生成。
func asyncImageFailureDefinitelyRejected(c *gin.Context, err error, eventStart int) bool {
	if err == nil {
		return false
	}
	if _, _, readFailure := service.OpenAIUpstreamStreamReadErrorDetails(err); readFailure {
		return false
	}
	events := asyncImageUpstreamEvents(c)
	if eventStart >= 0 && eventStart < len(events) {
		for _, event := range events[eventStart:] {
			// 内部重试也可能累积多个发送结果，后来的明确拒绝不能覆盖先前网络未知。
			if event != nil && event.Kind == "request_error" {
				return false
			}
		}
	}
	var imageErr *service.OpenAIImagesUpstreamError
	if errors.As(err, &imageErr) {
		return imageErr.Code != service.OpenAIUpstreamStreamReadErrorCode && imageErr.Code != service.OpenAIUpstreamStreamTruncatedCode && imageErr.Code != service.OpenAIUpstreamHTTP2StreamErrorCode
	}
	var failoverErr *service.UpstreamFailoverError
	if errors.As(err, &failoverErr) && failoverErr.IsCredentialFailure() {
		return true
	}
	if eventStart < 0 || eventStart >= len(events) {
		return false
	}
	rejected := false
	for _, event := range events[eventStart:] {
		if event == nil {
			continue
		}
		if event.Kind == "request_error" || event.UpstreamStatusCode < http.StatusBadRequest {
			return false
		}
		if event.Kind == "http_error" || event.Kind == "failover" {
			rejected = true
		}
	}
	return rejected
}

// finish 只在整个请求结束时处理剩余预占，已产图但未成功捕获的资金留待核对。
func (s *asyncImageBillingState) finish(c *gin.Context, log *zap.Logger) {
	if s.reservation == nil || s.settled {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
	defer cancel()
	var err error
	if s.uncertain {
		err = s.gateway.MarkAsyncImageBillingReconciliation(ctx, s.reservation)
	} else {
		err = s.gateway.ReleaseAsyncImageBilling(ctx, s.reservation)
	}
	if err != nil {
		log.Error("async_image.billing_finalize_failed", zap.Bool("reconciliation", s.uncertain), zap.Error(err))
	}
}

// record 同步捕获预占，避免 handler 退出释放与后台 usage worker 扣费互相竞争。
func (s *asyncImageBillingState) record(c *gin.Context, input *service.OpenAIRecordUsageInput) error {
	s.uncertain = true
	if input == nil || input.Result == nil || input.Result.ImageCount <= 0 {
		return errors.New("async image capture requires confirmed image output")
	}
	input.ImageReservation = s.reservation.Reservation
	input.ImageQuote = s.reservation.Quote
	service.MarkSmartRoutingAttemptNonReplayable(c.Request.Context(), "image_billing_capture")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
	defer cancel()
	if err := s.gateway.RecordUsage(ctx, input); err != nil {
		return err
	}
	s.settled = true
	return nil
}
