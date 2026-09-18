package service

// @project-doc docs/operations/account_maintenance.md#ollama_async_reset
// Ollama Cloud 用量窗口恢复：复用现有抓取流程，异步探测只延长已确认的账号限流。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

const ollamaCloudUsageProbeWritebackTimeout = 10 * time.Second

type ollamaCloudUsageProbeScheduler interface {
	ScheduleOllamaCloudUsageRateLimitProbe(accountID int64, onExhausted OllamaCloudUsageRateLimitProbeCallback) bool
}

type ollamaCloudUsageRateLimitStarter interface {
	BeginOllamaCloudRateLimit(ctx context.Context, id int64, resetAt *time.Time) error
}

type ollamaCloudUsageRateLimitSetterIfGeneration interface {
	SetRateLimitedIfUnchanged(ctx context.Context, id int64, expectedUpdatedAt time.Time, expectedLimitedAt, expectedResetAt *time.Time, newResetAt time.Time) (bool, error)
}

func (s *RateLimitService) SetOllamaCloudUsageProbeScheduler(scheduler ollamaCloudUsageProbeScheduler) {
	s.ollamaCloudUsageProbe = scheduler
}

func (s *RateLimitService) handleOllamaCloudUsage429(ctx context.Context, account *Account, headers http.Header) {
	if s == nil || account == nil || account.ID <= 0 || s.accountRepo == nil {
		return
	}

	var shortReset time.Time
	now := time.Now()
	if d := ollamaCloudUsageRetryAfter(headers, now); d > 0 {
		shortReset = now.Add(d)
	} else if cooldown, enabled := s.get429FallbackCooldown(ctx, account); enabled {
		shortReset = now.Add(cooldown)
	} else {
		slog.Info("rate_limit_ollama_429_fallback_ignored", "account_id", account.ID, "platform", account.Platform)
	}

	var resetFloor *time.Time
	if !shortReset.IsZero() {
		resetFloor = &shortReset
	}
	if !s.applyOllamaCloudUsageImmediateCooldown(ctx, account, resetFloor) {
		return
	}

	authoritative, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || authoritative == nil {
		slog.Warn("ollama_cloud_usage_authoritative_load_failed", "account_id", account.ID, "error", err)
		return
	}
	if authoritative.RateLimitResetAt != nil && authoritative.RateLimitResetAt.After(now) {
		s.notifyAccountSchedulingBlocked(authoritative, *authoritative.RateLimitResetAt, "ollama_429")
	}

	if s.ollamaCloudUsageProbe == nil {
		return
	}
	s.scheduleOllamaCloudUsageProbe(authoritative)
}

// 先应用账号级下限；权威行可能已有更晚的重置点，通知调度器时必须重新读取。
func (s *RateLimitService) applyOllamaCloudUsageImmediateCooldown(ctx context.Context, account *Account, shortReset *time.Time) bool {
	if starter, ok := s.accountRepo.(ollamaCloudUsageRateLimitStarter); ok {
		if err := starter.BeginOllamaCloudRateLimit(ctx, account.ID, shortReset); err != nil {
			slog.Warn("rate_limit_ollama_429_iflater_failed", "account_id", account.ID, "error", err)
			return false
		}
		return true
	}
	// 缺少原子延长能力时不降级为无条件写入，避免并发缩短既有冷却。
	slog.Warn("ollama_cloud_usage_atomic_cooldown_unavailable", "account_id", account.ID)
	return false
}

func (s *RateLimitService) scheduleOllamaCloudUsageProbe(account *Account) {
	if s == nil || account == nil || s.ollamaCloudUsageProbe == nil {
		return
	}
	if !IsOllamaCloudUsageAccount(account) {
		return
	}
	fingerprint, valid := ollamaCloudUsageRateLimitFingerprint(account)
	if !valid {
		return
	}
	expectedLimitedAt := cloneTimePtr(account.RateLimitedAt)
	expectedResetAt := cloneTimePtr(account.RateLimitResetAt)
	accepted := s.ollamaCloudUsageProbe.ScheduleOllamaCloudUsageRateLimitProbe(
		account.ID,
		func(accountID int64, resetAt time.Time) {
			s.applyOllamaCloudUsageProbeReset(accountID, fingerprint, expectedLimitedAt, expectedResetAt, resetAt)
		},
	)
	if !accepted {
		slog.Debug("ollama_cloud_usage_probe_schedule_rejected", "account_id", account.ID)
	}
}

func (s *RateLimitService) applyOllamaCloudUsageProbeReset(
	accountID int64,
	expectedFingerprint string,
	expectedLimitedAt, expectedResetAt *time.Time,
	resetAt time.Time,
) {
	now := time.Now()
	if s == nil || accountID <= 0 || s.accountRepo == nil || !resetAt.After(now) {
		return
	}
	if expectedResetAt != nil && !resetAt.After(*expectedResetAt) {
		return
	}

	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), ollamaCloudUsageProbeWritebackTimeout)
	defer cancel()

	account, err := s.accountRepo.GetByID(bgCtx, accountID)
	if err != nil || account == nil {
		slog.Warn("ollama_cloud_usage_probe_reset_load_failed", "account_id", accountID, "error", err)
		return
	}
	if !IsOllamaCloudUsageAccount(account) {
		return
	}
	if !account.IsActive() || !account.Schedulable {
		return
	}
	currentFingerprint, valid := ollamaCloudUsageRateLimitFingerprint(account)
	if !valid || currentFingerprint != expectedFingerprint {
		return
	}

	setter, ok := s.accountRepo.(ollamaCloudUsageRateLimitSetterIfGeneration)
	if !ok {
		return
	}
	// 用量快照写入会合法更新 updated_at，因此用当前版本做最终 CAS，配置指纹仍对照触发时。
	updated, err := setter.SetRateLimitedIfUnchanged(bgCtx, accountID, account.UpdatedAt, expectedLimitedAt, expectedResetAt, resetAt)
	if err != nil {
		slog.Warn("rate_limit_set_failed", "account_id", accountID, "error", err)
		return
	}
	if !updated {
		slog.Debug("ollama_cloud_usage_probe_reset_skipped_stale", "account_id", accountID)
		return
	}

	s.notifyAccountSchedulingBlocked(account, resetAt, "ollama_cloud_usage_429_probe")
	slog.Info("ollama_cloud_account_rate_limited_probe",
		"account_id", accountID,
		"reset_at", resetAt.UTC(),
		"reset_in", time.Until(resetAt).Truncate(time.Second),
	)
}

// 捕获可编辑配置而忽略抓取快照和使用时间，确保改 Key、代理、会话或账号策略后旧回调失效。
// 敏感内容仅参与进程内摘要计算，不写入日志、缓存键或接口。
func ollamaCloudUsageRateLimitFingerprint(account *Account) (string, bool) {
	if _, ok := ollamaCloudUsageGroupFingerprint(account); !ok {
		return "", false
	}
	extra := make(map[string]any, len(account.Extra))
	for key, value := range account.Extra {
		if key != OllamaCloudUsageSnapshotExtraKey {
			extra[key] = value
		}
	}
	groupIDs := append([]int64(nil), account.GroupIDs...)
	sort.Slice(groupIDs, func(i, j int) bool { return groupIDs[i] < groupIDs[j] })
	data, err := json.Marshal(struct {
		ID                              int64
		Name                            string
		Notes                           *string
		Platform, Type, Status          string
		Credentials, Extra              map[string]any
		ProxyID                         *int64
		Proxy                           *Proxy
		Concurrency, Priority           int
		RateMultiplier                  *float64
		LoadFactor                      *int
		Schedulable, AutoPauseOnExpired bool
		ExpiresAt                       *time.Time
		GroupIDs                        []int64
	}{
		ID: account.ID, Name: account.Name, Notes: account.Notes,
		Platform: account.Platform, Type: account.Type, Status: account.Status,
		Credentials: account.Credentials, Extra: extra,
		ProxyID: account.ProxyID, Proxy: account.Proxy,
		Concurrency: account.Concurrency, Priority: account.Priority,
		RateMultiplier: account.RateMultiplier, LoadFactor: account.LoadFactor,
		Schedulable: account.Schedulable, AutoPauseOnExpired: account.AutoPauseOnExpired,
		ExpiresAt: account.ExpiresAt, GroupIDs: groupIDs,
	})
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true
}
