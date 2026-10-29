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
	if s.prepared && s.reservation != nil {
		return nil
	}
	// 无预占的同步候选切到 MJ 时必须重新检查；已有预占继续只复用一次。
	s.prepared = true
	reservation, err := s.gateway.PrepareAsyncImageBilling(ctx, key, account, subscription, model, count, sizeTier, mapping)
	if err != nil {
		return err
	}
	s.reservation = reservation
	if reservation != nil {
		// 预占按请求 ID 冻结且报价含分组：换组会与已冻结合同冲突、无法按新分组重新定价，
		// 因此一旦冻结资金，本请求不再跨组重放（组内安全换号仍复用原预占）。
		service.MarkSmartRoutingAttemptNonReplayable(ctx, "image_billing_reserved")
	}
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
	if decision, ok := service.ImageAttemptDecisionForError(err); ok {
		return decision.Outcome == service.ImageAttemptNotSent || decision.Outcome == service.ImageAttemptRejected
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
	// 异步任务先交付后扣费：登记捕获，待图片转存成功后由任务执行器结算；
	// uncertain 保持为真，请求收尾只把预占标记为待核对，资金冻结到交付结果确定。
	if deferred := service.AsyncImageDeferredCaptureFromContext(c.Request.Context()); deferred != nil {
		gateway, reservation := s.gateway, s.reservation
		// 扣费在交付前同步重试，失败即按未交付释放；不写后台恢复证据，避免未交付任务被补扣。
		input.SkipImageSettlementEvidence = true
		// 结算必须沿用原请求上下文的值（请求 ID、载荷指纹等），否则与预占身份不一致而被账本拒绝。
		requestCtx := context.WithoutCancel(c.Request.Context())
		if deferred.Defer(
			func(context.Context) error {
				ctx, cancel := context.WithTimeout(requestCtx, 10*time.Second)
				defer cancel()
				return gateway.RecordUsage(ctx, input)
			},
			func(context.Context) error {
				ctx, cancel := context.WithTimeout(requestCtx, 10*time.Second)
				defer cancel()
				return gateway.ReleaseAsyncImageBilling(ctx, reservation)
			},
		) {
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
	defer cancel()
	if err := s.gateway.RecordUsage(ctx, input); err != nil {
		return err
	}
	s.settled = true
	return nil
}
