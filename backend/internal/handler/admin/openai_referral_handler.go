package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

type openAIReferralService interface {
	QueryReferralEligibility(context.Context, int64) (*service.OpenAIReferralEligibility, error)
	CacheReferralSnapshot(context.Context, int64, *service.OpenAIReferralEligibility) error
	SendReferralInvite(context.Context, int64, service.OpenAIReferralSendRequest) (*service.OpenAIReferralSendResult, error)
}

type openAIReferralRefreshResponse struct {
	Eligibility    *service.OpenAIReferralEligibility `json:"eligibility"`
	CachePersisted bool                               `json:"cache_persisted"`
}

type openAIReferralSendResponse struct {
	service.OpenAIReferralSendResult
	openAIReferralRefreshResponse
	RefreshFailed bool `json:"refresh_failed"`
}

func (h *OpenAIOAuthHandler) referralAccountID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	if h.referralService == nil {
		response.BadRequest(c, "OpenAI referral service is not enabled")
		return 0, false
	}
	return id, true
}

// RefreshReferrals 会持久化展示快照，因此使用 POST 并纳入管理员审计。
func (h *OpenAIOAuthHandler) RefreshReferrals(c *gin.Context) {
	id, ok := h.referralAccountID(c)
	if !ok {
		return
	}
	eligibility, err := h.referralService.QueryReferralEligibility(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if eligibility == nil {
		response.Error(c, http.StatusBadGateway, "Empty invitation eligibility response")
		return
	}
	cacheErr := h.referralService.CacheReferralSnapshot(c.Request.Context(), id, eligibility)
	response.Success(c, openAIReferralRefreshResponse{Eligibility: eligibility, CachePersisted: cacheErr == nil})
}

func (h *OpenAIOAuthHandler) SendReferralInvite(c *gin.Context) {
	id, ok := h.referralAccountID(c)
	if !ok {
		return
	}
	var input service.OpenAIReferralSendRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid invitation request")
		return
	}
	result, err := h.referralService.SendReferralInvite(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if result == nil || !result.Sent {
		response.Error(c, http.StatusBadGateway, "Invitation outcome is unknown; check Codex before sending again")
		return
	}
	// 邮件已发送，即使浏览器断开或随后刷新失败，也保留发送成功状态，
	// 避免用户误以为发送失败而重复提交。
	baseCtx := context.WithoutCancel(c.Request.Context())
	refreshCtx, cancelRefresh := context.WithTimeout(baseCtx, 8*time.Second)
	eligibility, refreshErr := h.referralService.QueryReferralEligibility(refreshCtx, id)
	cancelRefresh()
	if refreshErr != nil {
		eligibility = nil
	}
	// 刷新可能耗尽全部超时预算，持久化及失效旧快照使用独立的超时。
	cacheCtx, cancelCache := context.WithTimeout(baseCtx, 3*time.Second)
	defer cancelCache()
	cacheErr := h.referralService.CacheReferralSnapshot(cacheCtx, id, eligibility)
	response.Success(c, openAIReferralSendResponse{
		OpenAIReferralSendResult:      *result,
		openAIReferralRefreshResponse: openAIReferralRefreshResponse{Eligibility: eligibility, CachePersisted: cacheErr == nil},
		RefreshFailed:                 eligibility == nil,
	})
}
