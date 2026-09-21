package handler

import (
	"context"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	middleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// SeedanceTasks 提供方舟原生异步视频任务入口。
// @project-doc docs/interfaces/seedance_upstream.md#seedance_task_lifecycle
func (h *OpenAIGatewayHandler) SeedanceTasks(c *gin.Context) {
	if c.Request.Method == http.MethodPost && c.GetHeader("Content-Type") != "" {
		mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || mediaType != "application/json" {
			h.errorResponse(c, http.StatusUnsupportedMediaType, "invalid_request_error", "Seedance requires application/json")
			return
		}
	}
	key, ok := middleware.GetAPIKeyFromContext(c)
	lookupByIdentity := ok && key != nil && (key.IsComposite || key.SmartRouting) && key.GroupID == nil && c.Request.Method != http.MethodPost
	if !ok || key == nil || (!lookupByIdentity && (key.Group == nil || key.Group.Platform != service.PlatformOpenAI)) {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "Seedance requires an OpenAI or composite group")
		return
	}
	endpoint := service.SeedanceEndpointCreate
	setActualUpstreamEndpoint(c, EndpointSeedanceTasks)
	taskID := ""
	if c.Request.Method != http.MethodPost {
		if strings.TrimSpace(c.Param("task_id")) == "" {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "task_id is required")
			return
		}
		taskID = service.SeedanceTaskKey(c.Param("task_id"))
		endpoint = service.SeedanceEndpointStatus
		if c.Request.Method == http.MethodDelete {
			endpoint = service.SeedanceEndpointDelete
		}
	} else {
		service.DeferSeedanceResponse(c)
	}
	h.handleGrokMedia(c, endpoint, taskID)
}

// 方舟只按完成响应的真实 token 结算，不推算视频时长；重复轮询共用任务级扣费键。
// @project-doc docs/interfaces/seedance_upstream.md#seedance_billing
func prepareSeedanceCompletionBilling(ctx context.Context, h *OpenAIGatewayHandler, key *service.APIKey, subject middleware.AuthSubject, taskID string, result *service.OpenAIForwardResult) (*service.OpenAIForwardResult, *service.SeedanceBillingSnapshot) {
	if result == nil || result.Usage.OutputTokens <= 0 {
		return nil, nil
	}
	pending, err := h.gatewayService.LoadGrokVideoPendingBilling(ctx, taskID, subject.UserID, key.ID)
	if err != nil || pending == nil || pending.SeedanceBilling == nil {
		logger.L().Warn("seedance.billing_snapshot_unavailable", zap.String("task_id", taskID), zap.Int64("api_key_id", key.ID), zap.Error(err))
		return nil, nil
	}
	if sub := pending.SeedanceBilling.Subscription; sub != nil && (key.User == nil || sub.UserID != key.User.ID) {
		logger.L().Error("seedance.billing_snapshot_owner_mismatch", zap.String("task_id", taskID), zap.Int64("api_key_id", key.ID))
		return nil, nil
	}
	claimed, err := h.gatewayService.ClaimGrokVideoBilling(ctx, taskID, subject.UserID, key.ID)
	if err != nil || !claimed {
		if err != nil {
			logger.L().Warn("seedance.billing_claim_failed", zap.String("task_id", taskID), zap.Int64("api_key_id", key.ID), zap.Error(err))
		}
		return nil, nil
	}
	merged := *result
	merged.Model = pending.Model
	merged.BillingModel = firstNonEmptyString(pending.BillingModel, pending.Model)
	merged.UpstreamModel = firstNonEmptyString(pending.UpstreamModel, result.UpstreamModel)
	merged.RequestID = service.StableGrokVideoBillingRequestID(taskID)
	merged.ResponseID = taskID
	merged.Duration = service.GrokVideoE2EDuration(pending.CreatedAt, time.Now())
	return &merged, pending.SeedanceBilling
}

// newSeedanceBillingSnapshot 只复制结算所需实体，禁止把用户和管理员完整资料存入任务快照。
func newSeedanceBillingSnapshot(key *service.APIKey, subscription *service.UserSubscription, mapping service.ChannelMappingResult) *service.SeedanceBillingSnapshot {
	snapshot := &service.SeedanceBillingSnapshot{BillingMode: service.APIKeyEffectiveBillingMode(key), ChannelMapping: mapping}
	if key.Group != nil {
		group := *key.Group
		// 分组预加载的账号关系可能包含上游凭据，任务快照只需要分组计费配置。
		group.AccountGroups = nil
		snapshot.Group = &group
	}
	if key.PreferredSubscriptionID != nil {
		id := *key.PreferredSubscriptionID
		snapshot.PreferredSubscriptionID = &id
	}
	if subscription != nil && snapshot.BillingMode != service.APIKeyBillingModeBalance {
		// 自动模式保留余额补足策略，原套餐 ID 通过内部结算候选约束传入账本。
		copy := *subscription
		copy.User = nil
		copy.AssignedByUser = nil
		copy.Notes = ""
		snapshot.Subscription = &copy
	} else if snapshot.BillingMode == service.APIKeyBillingModeAuto {
		// 创建时已选择余额，不能因轮询前购买了套餐而改用另一资金来源。
		snapshot.BillingMode = service.APIKeyBillingModeBalance
	}
	return snapshot
}
