package service

// Ollama Cloud 用量窗口恢复：复用现有抓取流程，异步探测只延长已确认的账号限流。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

const (
	ollamaCloudUsageProbeTimeout = 45 * time.Second

	ollamaCloudUsageProbeMaxQueue = 256

	ollamaCloudUsageProbeGroupRetention = 2 * ollamaCloudUsageManualRefreshInterval
)

// 回调只在新鲜成功快照确认耗尽时调用；持久化方必须自行使用有界上下文和代次 CAS。
type OllamaCloudUsageRateLimitProbeCallback func(accountID int64, resetAt time.Time)

type ollamaCloudUsageProbeRequest struct {
	accountID   int64
	onExhausted OllamaCloudUsageRateLimitProbeCallback
}

type ollamaCloudUsageProbeGroupEntry struct {
	attemptAt time.Time
	snapshot  *OllamaCloudUsageSnapshot
}

// 只入队，不执行网络或数据库访问；同账号合并，队列满时拒绝新的账号。
func (s *OllamaCloudUsageService) ScheduleOllamaCloudUsageRateLimitProbe(
	accountID int64,
	onExhausted OllamaCloudUsageRateLimitProbeCallback,
) bool {
	if s == nil || onExhausted == nil || accountID <= 0 {
		return false
	}
	s.mu.Lock()
	running := s.started && !s.stopped
	s.mu.Unlock()
	if !running {
		return false
	}

	s.probeMu.Lock()
	for i := range s.probeQueue {
		if s.probeQueue[i].accountID == accountID {
			s.probeQueue[i].onExhausted = onExhausted
			s.probeMu.Unlock()
			s.wakeProbeLoop()
			return true
		}
	}
	if len(s.probeQueue) >= ollamaCloudUsageProbeMaxQueue {
		s.probeMu.Unlock()
		return false
	}
	s.probeQueue = append(s.probeQueue, ollamaCloudUsageProbeRequest{
		accountID:   accountID,
		onExhausted: onExhausted,
	})
	s.probeMu.Unlock()

	s.wakeProbeLoop()
	return true
}

func (s *OllamaCloudUsageService) wakeProbeLoop() {
	if s == nil {
		return
	}
	select {
	case s.probeWake <- struct{}{}:
	default:
	}
}

// 单协调循环受服务 Stop 取消，每次探测限时，禁止按模型请求无限创建后台任务。
func (s *OllamaCloudUsageService) probeLoop() {
	defer s.wg.Done()
	for {
		s.probeMu.Lock()
		if len(s.probeQueue) == 0 {
			s.probeMu.Unlock()
			select {
			case <-s.probeWake:
			case <-s.parentCtx.Done():
				return
			}
			continue
		}
		batch := s.probeQueue
		s.probeQueue = nil
		s.probeMu.Unlock()

		for _, req := range batch {
			if s.parentCtx.Err() != nil {
				return
			}
			ctx, cancel := context.WithTimeout(s.parentCtx, ollamaCloudUsageProbeTimeout)
			s.runOllamaCloudUsageProbe(ctx, req.accountID, req.onExhausted)
			cancel()
		}
	}
}

func (s *OllamaCloudUsageService) probeGroupResult(key string) (ollamaCloudUsageProbeGroupEntry, bool) {
	if s == nil {
		return ollamaCloudUsageProbeGroupEntry{}, false
	}
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	entry, ok := s.probeGroups[key]
	return entry, ok
}

func (s *OllamaCloudUsageService) storeProbeGroupResult(key string, entry ollamaCloudUsageProbeGroupEntry) {
	if s == nil {
		return
	}
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if s.probeGroups == nil {
		s.probeGroups = make(map[string]ollamaCloudUsageProbeGroupEntry)
	}
	pruneAt := entry.attemptAt.Add(-ollamaCloudUsageProbeGroupRetention)
	for groupKey, existing := range s.probeGroups {
		if existing.attemptAt.Before(pruneAt) {
			delete(s.probeGroups, groupKey)
		}
	}
	s.probeGroups[key] = entry
}

func (s *OllamaCloudUsageService) runOllamaCloudUsageProbe(
	ctx context.Context,
	accountID int64,
	onExhausted OllamaCloudUsageRateLimitProbeCallback,
) {
	if s == nil || s.accountRepo == nil || onExhausted == nil || ctx.Err() != nil {
		return
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return
	}
	if !IsOllamaCloudUsageAccount(account) || !account.IsActive() || !account.Schedulable {
		return
	}
	if err := s.ResolveAccounts(ctx, []*Account{account}); err != nil {
		return
	}
	if !ollamaCloudUsageConfigured(account) {
		return
	}
	key, valid := ollamaCloudUsageProbeCacheKey(account)
	if !valid {
		return
	}
	configuration, valid := ollamaCloudUsageRateLimitFingerprint(account)
	if !valid {
		return
	}
	// 回调交付前重新确认账号配置与共享会话，排除排队/网络等待期间的账号编辑。
	deliver := func(id int64, resetAt time.Time) {
		current, err := s.accountRepo.GetByID(ctx, id)
		if err != nil || current == nil || !current.IsActive() || !current.Schedulable {
			return
		}
		if err := s.ResolveAccounts(ctx, []*Account{current}); err != nil {
			return
		}
		fingerprint, ok := ollamaCloudUsageRateLimitFingerprint(current)
		if !ok || fingerprint != configuration || ctx.Err() != nil {
			return
		}
		// 人工查询与探测共享 singleflight；会话更换若恰好发生在保存后，旧返回值不能复活已清除的快照。
		now := s.currentTime()
		confirmedReset, exhausted := ollamaCloudUsageExhaustionResetAt(
			decodeOllamaCloudUsageSnapshot(current.Extra), now, now.Add(-ollamaCloudUsageManualRefreshInterval),
		)
		if !exhausted || !confirmedReset.Equal(resetAt) {
			return
		}
		onExhausted(id, resetAt)
	}

	window := ollamaCloudUsageManualRefreshInterval
	accountSnapshot := decodeOllamaCloudUsageSnapshot(account.Extra)
	cached, hasCached := s.probeGroupResult(key)

	now := s.currentTime()
	if newest := ollamaCloudUsageProbeNewestSuccess(accountSnapshot, cached, hasCached, now, window); newest != nil {
		maybeOllamaCloudUsageProbeExhaustion(ctx, accountID, newest, s.currentTime(), window, deliver)
		return
	}

	if horizon := ollamaCloudUsageProbeBackoffHorizon(accountSnapshot, cached, hasCached); !horizon.IsZero() && now.Before(horizon) {
		return
	}
	if hasCached && now.Before(cached.attemptAt.Add(window)) {
		return
	}

	settings, settingsErr := s.GetSettings(ctx)
	if settingsErr != nil {
		return
	}
	// 429 的一次性探测不受周期刷新开关限制，但仍遵守会话配置、抓取退避及并发限制。
	fetched, refreshErr := s.refreshAccountWithCancelableWait(ctx, accountID, settings, false, true)
	if refreshErr == nil && fetched == nil {
		return
	}
	doneNow := s.currentTime()
	s.storeProbeGroupResult(key, ollamaCloudUsageProbeGroupEntry{attemptAt: doneNow, snapshot: fetched})
	if refreshErr != nil {
		return
	}
	maybeOllamaCloudUsageProbeExhaustion(ctx, accountID, fetched, doneNow, window, deliver)
}

// 同 Key 且同管理会话复用探测结果；更换会话后不能消费旧会话的短期缓存。
func ollamaCloudUsageProbeCacheKey(account *Account) (string, bool) {
	group, ok := ollamaCloudUsageGroupFingerprint(account)
	if !ok || !ollamaCloudUsageConfigured(account) {
		return "", false
	}
	session, _ := account.Extra[OllamaCloudUsageSessionExtraKey].(string)
	sum := sha256.Sum256([]byte(group + "\x00" + session))
	return hex.EncodeToString(sum[:]), true
}

func ollamaCloudUsageProbeObservedAt(snapshot *OllamaCloudUsageSnapshot) (time.Time, bool) {
	if snapshot == nil {
		return time.Time{}, false
	}
	if snapshot.Status == OllamaCloudUsageStatusOK && snapshot.FetchedAt != nil && !snapshot.FetchedAt.IsZero() {
		return snapshot.FetchedAt.UTC(), true
	}
	if !snapshot.LastAttemptAt.IsZero() {
		return snapshot.LastAttemptAt.UTC(), true
	}
	return time.Time{}, false
}

// 按实际观测时间选择最新状态；新失败必须压过旧成功，不能用残留 fetched_at 掩盖错误。
func ollamaCloudUsageProbeNewestSuccess(
	accountSnapshot *OllamaCloudUsageSnapshot,
	cached ollamaCloudUsageProbeGroupEntry,
	hasCached bool,
	now time.Time,
	window time.Duration,
) *OllamaCloudUsageSnapshot {
	var newest *OllamaCloudUsageSnapshot
	var newestAt time.Time
	consider := func(snapshot *OllamaCloudUsageSnapshot) {
		if snapshot == nil {
			return
		}
		at, ok := ollamaCloudUsageProbeObservedAt(snapshot)
		if !ok {
			return
		}
		if newest == nil || at.After(newestAt) {
			newest = snapshot
			newestAt = at
		}
	}
	consider(accountSnapshot)
	if hasCached {
		if cached.snapshot != nil {
			consider(cached.snapshot)
		} else if !cached.attemptAt.IsZero() && (newest == nil || cached.attemptAt.After(newestAt)) {
			newest = nil
			newestAt = cached.attemptAt
		}
	}
	if newest == nil || newest.Status != OllamaCloudUsageStatusOK {
		return nil
	}
	if newest.FetchedAt == nil || newest.FetchedAt.IsZero() || newest.FetchedAt.Before(now.Add(-window)) {
		return nil
	}
	return newest
}

// 抓取设置页的 429/认证失败退避不能被模型 429 反复绕过。
func ollamaCloudUsageProbeBackoffHorizon(
	accountSnapshot *OllamaCloudUsageSnapshot,
	cached ollamaCloudUsageProbeGroupEntry,
	hasCached bool,
) time.Time {
	var horizon time.Time
	consider := func(snapshot *OllamaCloudUsageSnapshot) {
		if snapshot == nil {
			return
		}
		if snapshot.Status != OllamaCloudUsageStatusFailed && snapshot.Status != OllamaCloudUsageStatusUnauthorized {
			return
		}
		if snapshot.NextRefreshAt.IsZero() {
			return
		}
		if horizon.IsZero() || snapshot.NextRefreshAt.After(horizon) {
			horizon = snapshot.NextRefreshAt.UTC()
		}
	}
	consider(accountSnapshot)
	if hasCached {
		consider(cached.snapshot)
	}
	return horizon
}

func maybeOllamaCloudUsageProbeExhaustion(
	ctx context.Context,
	accountID int64,
	snapshot *OllamaCloudUsageSnapshot,
	now time.Time,
	window time.Duration,
	onExhausted OllamaCloudUsageRateLimitProbeCallback,
) {
	if snapshot == nil || onExhausted == nil {
		return
	}
	resetAt, exhausted := ollamaCloudUsageExhaustionResetAt(snapshot, now, now.Add(-window))
	if !exhausted {
		return
	}
	if ctx.Err() != nil {
		return
	}
	onExhausted(accountID, resetAt)
}
