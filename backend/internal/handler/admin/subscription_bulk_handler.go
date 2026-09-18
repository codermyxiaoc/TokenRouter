package admin

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// BulkExtendSubscriptionRequest 的 days 表示增加的天数，与单条目标有效期调整区分。
type BulkExtendSubscriptionRequest struct {
	SubscriptionIDs []int64 `json:"subscription_ids" binding:"required,min=1,max=100,dive,gt=0"`
	Days            int     `json:"days" binding:"required,min=1,max=36500"`
}

// BulkResetSubscriptionQuotaRequest 显式选择需要重置的额度窗口。
type BulkResetSubscriptionQuotaRequest struct {
	SubscriptionIDs []int64 `json:"subscription_ids" binding:"required,min=1,max=100,dive,gt=0"`
	Daily           bool    `json:"daily"`
	Weekly          bool    `json:"weekly"`
	Monthly         bool    `json:"monthly"`
}

// BulkExtend 对整批订阅增加天数，使用既有幂等协调器处理客户端重试。
func (h *SubscriptionHandler) BulkExtend(c *gin.Context) {
	var req BulkExtendSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if strings.TrimSpace(c.GetHeader("Idempotency-Key")) == "" {
		response.BadRequest(c, "Idempotency-Key is required")
		return
	}
	executeAdminIdempotentJSON(c, "admin.subscriptions.bulk_extend", req, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.subscriptionService.BulkExtendSubscriptions(ctx, req.SubscriptionIDs, req.Days)
	})
}

// BulkResetQuota 仅重置选中的窗口，整批复用原有管理员额度规则。
func (h *SubscriptionHandler) BulkResetQuota(c *gin.Context) {
	var req BulkResetSubscriptionQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if !req.Daily && !req.Weekly && !req.Monthly {
		response.BadRequest(c, "At least one of 'daily', 'weekly', or 'monthly' must be true")
		return
	}
	if strings.TrimSpace(c.GetHeader("Idempotency-Key")) == "" {
		response.BadRequest(c, "Idempotency-Key is required")
		return
	}
	executeAdminIdempotentJSON(c, "admin.subscriptions.bulk_reset_quota", req, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.subscriptionService.BulkResetSubscriptionQuota(ctx, req.SubscriptionIDs, req.Daily, req.Weekly, req.Monthly)
	})
}
