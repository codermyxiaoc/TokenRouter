package handler

import (
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// accountSlotReschedule 只在尚未向上游提交时重选账号，不扩大用户并发或账号切换预算。
// 多账号共用第一次等待的截止时间，避免逐账号排队叠加成长时间阻塞。
// @project-doc docs/architecture/gateway_request_lifecycle.md#account_selection_and_failover
type accountSlotReschedule struct {
	excluded        map[int64]struct{}
	switches        *int
	maximum         int
	requiredAccount bool
	waitDeadline    time.Time
	retry           bool
	lastCode        string
	lastMessage     string
}

func (s *accountSlotReschedule) waitTimeout(timeout time.Duration) time.Duration {
	if s == nil || s.requiredAccount {
		return timeout
	}
	if s.waitDeadline.IsZero() {
		s.waitDeadline = time.Now().Add(timeout)
	}
	return min(timeout, time.Until(s.waitDeadline))
}

// handleCapacity 不将本地排队竞争作为上游故障，也不修改持久粘性或健康状态。
func (s *accountSlotReschedule) handleCapacity(c *gin.Context, accountID int64, status int, code, message string) bool {
	if s == nil || s.requiredAccount || s.switches == nil || s.excluded == nil ||
		c.Request.Context().Err() != nil || service.SmartRoutingRequiredAccountID(c.Request.Context()) > 0 ||
		status != http.StatusTooManyRequests || (code != gatewayQueueFullCode && code != gatewayConcurrencyLimitCode) {
		return false
	}
	s.lastCode, s.lastMessage = code, message
	if *s.switches >= s.maximum {
		return false
	}
	if _, seen := s.excluded[accountID]; seen {
		return false
	}
	s.excluded[accountID] = struct{}{}
	(*s.switches)++
	s.retry = true
	return true
}

// writeExhausted 保留账号容量错误语义，不能将本地满槽误报成用户模型错误或上游502。
func (s *accountSlotReschedule) writeExhausted(writeError openAISlotErrorWriter) bool {
	if s == nil || s.lastCode == "" {
		return false
	}
	writeError(http.StatusTooManyRequests, "rate_limit_error", s.lastCode, s.lastMessage)
	return true
}
