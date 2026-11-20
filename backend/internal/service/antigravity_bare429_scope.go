package service

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// 请求范围内已记录无详情 429 冷却的账号。
const antigravityBare429CooledAccountKey = "antigravity_bare429_cooled_account_id"

// 不含限流原因和重置时间的 429 可能由同一请求触发。
func isBareAntigravity429(statusCode int, body []byte) bool {
	if statusCode != http.StatusTooManyRequests {
		return false
	}
	if gjson.GetBytes(body, "error.details").Exists() {
		return false
	}
	return ParseGeminiRateLimitResetTime(body) == nil
}

// 每个请求只冷却首个返回无详情 429 的账号，其余账号仍可服务其他请求。
func claimBare429Cooldown(c *gin.Context, accountID int64) bool {
	if c == nil {
		return true
	}
	if v, ok := c.Get(antigravityBare429CooledAccountKey); ok {
		if id, ok := v.(int64); ok && id != accountID {
			return false
		}
	}
	c.Set(antigravityBare429CooledAccountKey, accountID)
	return true
}

// 重试后的错误处理沿用请求中首次冷却的账号，避免重复影响整个组。
func (s *AntigravityGatewayService) handleUpstreamErrorAfterRetryLoop(
	ctx context.Context, c *gin.Context, prefix string, account *Account,
	statusCode int, headers http.Header, body []byte,
	requestedModel string,
	groupID int64, sessionHash string, isStickySession bool,
) *handleModelRateLimitResult {
	if isBareAntigravity429(statusCode, body) && !claimBare429Cooldown(c, account.ID) {
		logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d bare_429_cooldown_skipped account=%d (after retry loop)", prefix, statusCode, account.ID)
		return nil
	}
	return s.handleUpstreamError(ctx, prefix, account, statusCode, headers, body, requestedModel, groupID, sessionHash, isStickySession)
}
