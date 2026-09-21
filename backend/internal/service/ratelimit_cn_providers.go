package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// 国产供应商（kimi/zhipu/deepseek）的响应式冷却辅助。
//
// 与 openai/anthropic 不同：
//   - 余额不足是「可恢复」状态（充值/检测恢复后自动重新调度），不能走 handleAuthError
//     永久置 status=error。这里改为 SetTempUnschedulable，由独立用量监控在同一
//     身份的余额恢复后 ClearTempUnschedulable。
//   - Coding Plan 滚动窗口耗尽（429）的冷却终点应是真实的窗口重置时间（已由
//     用量监控写入统一快照），而非默认的秒级兜底。

// kimiConcurrentRequestLimitMessage 是 Kimi 账号并发限制的精确上游文案。
const kimiConcurrentRequestLimitMessage = "You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again."

// cnConcurrencyLimitReasonPrefix 标记 Kimi 并发限制导致的临时停调，
// 供恢复任务与其它账号状态来源区分。
const cnConcurrencyLimitReasonPrefix = "cn_concurrency_limit"

// cnQuotaExhausted403ErrorType 是 Coding Plan 配额窗口耗尽时上游返回的结构化错误类型。
// 这类错误会在窗口重置后自动恢复，不能按普通权限错误永久禁用账号。
const cnQuotaExhausted403ErrorType = "access_terminated_error"

// cnQuotaExhaustedReasonPrefix 是配额耗尽临时停调 reason 的稳定前缀，便于
// 运维日志和后台恢复任务区分它与并发限制、余额不足等其它状态。
const cnQuotaExhaustedReasonPrefix = "cn_quota_exhausted"

// isCNProviderConcurrencyLimit403 只识别 Kimi 返回的精确并发限制文案，
// 避免把其它权限错误或其它国产平台的相似文案误判为可恢复状态。
func isCNProviderConcurrencyLimit403(account *Account, upstreamMsg string) bool {
	return account != nil && account.Platform == PlatformKimi &&
		strings.TrimSpace(upstreamMsg) == kimiConcurrentRequestLimitMessage
}

// isCNProviderQuotaExhausted403 识别国产供应商 Coding Plan 的窗口配额耗尽 403。
// 上游可能只提供 usage limit/quota will reset 文案，也可能在 error.type 中返回
// access_terminated_error；两者均只在 Coding Plan 账号上生效，避免把普通 API Key
// 的权限错误误判成可恢复限流。并发限制文案由独立分支处理，保持两类信号互斥。
func isCNProviderQuotaExhausted403(account *Account, responseBody []byte, upstreamMsg string) bool {
	if account == nil || !account.IsCNProvider() || !account.IsCodingPlan() {
		return false
	}

	message := strings.ToLower(strings.TrimSpace(upstreamMsg))
	if strings.Contains(message, "usage limit") || strings.Contains(message, "quota will reset") {
		return true
	}

	return strings.EqualFold(
		strings.TrimSpace(gjson.GetBytes(responseBody, "error.type").String()),
		cnQuotaExhausted403ErrorType,
	)
}

// handleCNProviderQuotaExhausted403 将窗口配额耗尽按可恢复限流处理。
// 有效监控快照存在时使用真实窗口重置时间；快照尚未刷新时只设置短期临时停调，
// 这样既能等待配额恢复，也不会因为缺少快照而永久禁用账号。
func (s *RateLimitService) handleCNProviderQuotaExhausted403(
	ctx context.Context,
	account *Account,
	upstreamMsg string,
) {
	now := time.Now()
	if until := cnProviderQuotaSnapshotReset(account, now); until != nil {
		s.notifyAccountSchedulingBlocked(account, *until, cnQuotaExhaustedReasonPrefix)
		if err := s.accountRepo.SetRateLimited(ctx, account.ID, *until); err == nil {
			slog.Info("cn_quota_exhausted_rate_limited",
				"account_id", account.ID,
				"platform", account.Platform,
				"reset_at", until.UTC(),
			)
			return
		} else {
			// 快照写入失败时继续写临时停调，避免账号在当前请求后立即再次被选中。
			slog.Warn("cn_quota_exhausted_rate_limit_set_failed", "account_id", account.ID, "error", err)
		}
	}

	reason := cnQuotaExhaustedReasonPrefix
	if message := strings.TrimSpace(upstreamMsg); message != "" {
		reason += ": " + message
	}
	until := now.Add(time.Duration(openAI403CooldownMinutesDefault) * time.Minute)
	s.notifyAccountSchedulingBlocked(account, until, cnQuotaExhaustedReasonPrefix)
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("cn_quota_exhausted_set_temp_unschedulable_failed", "account_id", account.ID, "error", err)
		return
	}
	slog.Info("cn_quota_exhausted_temp_unschedulable",
		"account_id", account.ID,
		"platform", account.Platform,
		"until", until.UTC(),
	)
}

// handleCNProviderConcurrencyLimit403 将 Kimi 并发限制写为短期临时不可调度，
// 保留当前请求的切号信号，并确保不会进入累计 403 永久禁用计数。
func (s *RateLimitService) handleCNProviderConcurrencyLimit403(
	ctx context.Context,
	account *Account,
) {
	until := time.Now().Add(time.Duration(openAI403CooldownMinutesDefault) * time.Minute)
	reason := cnConcurrencyLimitReasonPrefix + ": " + kimiConcurrentRequestLimitMessage
	s.notifyAccountSchedulingBlocked(account, until, cnConcurrencyLimitReasonPrefix)
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("cn_concurrency_limit_set_temp_unschedulable_failed", "account_id", account.ID, "error", err)
		return
	}
	slog.Info("cn_provider_concurrency_limited",
		"account_id", account.ID,
		"platform", account.Platform,
		"until", until.UTC(),
	)
}

// cnProviderResponseIndicatesInsufficientBalance 通过响应体文案识别余额不足
// （智谱 payg 无独立余额端点，仅能靠响应文案识别）。
func cnProviderResponseIndicatesInsufficientBalance(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	s := strings.ToLower(string(body))
	return strings.Contains(s, "余额不足") ||
		strings.Contains(s, "insufficient balance") ||
		strings.Contains(s, "insufficient_credit") ||
		strings.Contains(s, "balance is not enough") ||
		strings.Contains(s, "no enough balance")
}

// handleCNProviderInsufficientBalance 把余额不足标记为可恢复的临时停调：
// 写入 balance_low 快照 + SetTempUnschedulable 一个余额检测周期，
// 由周期任务在余额恢复后清除。返回前已通知调度阻塞。
func (s *RateLimitService) handleCNProviderInsufficientBalance(
	ctx context.Context,
	account *Account,
	upstreamMsg string,
) {
	identityHash := cnUsageMonitorIdentityFingerprint(account)
	if identityHash == "" {
		identityHash = "unknown"
	}
	msg := cnUsageMonitorReason(identityHash)
	if upstreamMsg = strings.TrimSpace(upstreamMsg); upstreamMsg != "" {
		msg += ": " + upstreamMsg
	}

	until := time.Now().Add(s.cnBalanceCooldownDuration())
	s.notifyAccountSchedulingBlocked(account, until, "cn_insufficient_balance")
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, msg); err != nil {
		slog.Warn("cn_balance_set_temp_unschedulable_failed", "account_id", account.ID, "error", err)
		return
	}
	slog.Info("cn_provider_insufficient_balance",
		"account_id", account.ID,
		"platform", account.Platform,
		"until", until.UTC(),
	)
}

// cnBalanceCooldownDuration 返回余额不足临时停调的持续时长（= 2× 余额检测周期，
// 默认 20 分钟）。周期任务会在余额恢复后提前清除，故此处只需保证冷却覆盖到下一次
// 周期检测即可。
func (s *RateLimitService) cnBalanceCooldownDuration() time.Duration {
	minutes := 10
	if s != nil && s.cfg != nil {
		if cfgMin := s.cfg.Gateway.CNProviders.IntervalMinutes; cfgMin > 0 {
			minutes = cfgMin
		}
	}
	cooldown := time.Duration(minutes) * time.Minute * 2
	if cooldown < time.Minute {
		cooldown = 10 * time.Minute
	}
	return cooldown
}

// cnProviderQuotaSnapshotReset 读取 Coding Plan 统一快照中最早一个仍在未来的窗口
// 重置时间（5h / weekly）。429 多数由 5h 滚动窗口触发，取较早的重置点可避免
// 把账号冷却到 weekly 重置（可达数天）的过度停调；如果确是 weekly 窗口耗尽，
// 周期额度探测刷新快照后阈值评估会再次停调到正确的时间点。
// 无快照或均已过期返回 nil。
func cnProviderQuotaSnapshotReset(account *Account, now time.Time) *time.Time {
	if account != nil && account.IsOpenCodeGoPlan() {
		// GO 有月窗口：只选择已确认耗尽窗口中的最晚恢复点，不能用未耗尽窗口推测 429。
		if candidate := pickLatestResetSchedulingCandidate(openCodeGoThresholdCandidates(account, now), 100, now); candidate != nil {
			return cloneTimePtr(candidate.until)
		}
		return nil
	}
	if account == nil || !account.IsCNProvider() || !account.IsCodingPlan() {
		return nil
	}
	snapshot := validCNUsageMonitorSnapshot(account)
	if snapshot == nil || snapshot.Mode != "limits" {
		return nil
	}
	var earliest *time.Time
	for _, limit := range snapshot.Limits {
		t := cloneTimePtr(limit.ResetAt)
		if t == nil || !t.After(now) {
			continue
		}
		if earliest == nil || t.Before(*earliest) {
			earliest = t
		}
	}
	return earliest
}

// applyCNProviderReactive429 处理国产供应商的 429 响应。
// 返回 true 表示已处理（调用方应 return），false 表示未命中、继续走默认 429 逻辑。
func (s *RateLimitService) applyCNProviderReactive429(
	ctx context.Context,
	account *Account,
	headers http.Header,
	responseBody []byte,
) bool {
	if account != nil && account.IsOpenCodeGoPlan() {
		now := time.Now()
		until := cnProviderQuotaSnapshotReset(account, now)
		if until == nil {
			if resetAt := parseOpenAIRateLimitResetTime(responseBody); resetAt != nil {
				reset := time.Unix(*resetAt, 0)
				if reset.After(now) {
					until = &reset
				}
			}
		}
		if until == nil {
			return false
		}
		// 真实 429 继续走既有错误记录与切号链路，这里只补账号恢复时间。
		s.notifyAccountSchedulingBlocked(account, *until, "429")
		if err := s.accountRepo.SetRateLimited(ctx, account.ID, *until); err != nil {
			slog.Warn("rate_limit_set_failed", "account_id", account.ID, "error", err)
			return true
		}
		slog.Info("opencode_go_rate_limited", "account_id", account.ID, "platform", account.Platform, "reset_at", *until)
		return true
	}
	if !account.IsCNProvider() {
		return false
	}
	// 1) 余额不足文案：可恢复临时停调（含智谱 payg 这类无余额端点的场景）。
	if cnProviderResponseIndicatesInsufficientBalance(responseBody) {
		s.handleCNProviderInsufficientBalance(ctx, account, extractUpstreamErrorMessage(responseBody))
		return true
	}
	// 2) Coding Plan 窗口耗尽：冷却到快照中最早的窗口重置点（见
	// cnProviderQuotaSnapshotReset：429 多由 5h 窗口触发，取较早点避免过度停调）。
	if account.IsCodingPlan() {
		if until := cnProviderQuotaSnapshotReset(account, time.Now()); until != nil {
			s.notifyAccountSchedulingBlocked(account, *until, "429")
			if err := s.accountRepo.SetRateLimited(ctx, account.ID, *until); err != nil {
				slog.Warn("rate_limit_set_failed", "account_id", account.ID, "error", err)
				return true
			}
			slog.Info("cn_coding_plan_rate_limited",
				"account_id", account.ID,
				"platform", account.Platform,
				"reset_at", *until,
			)
			return true
		}
	}
	return false
}
