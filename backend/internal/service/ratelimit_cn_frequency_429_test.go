//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 智谱 GLM Coding Plan 实测 429 文案：额度充裕时的“限频”。
const zhipuFrequencyLimitedBody = `{"error":{"code":"1302","message":"您的账户已达到速率限制，请您控制请求频率"}}`

// cnFrequencyRepoStub 记录 SetRateLimited 的冷却时间，区分秒级冷却与窗口冷却。
type cnFrequencyRepoStub struct {
	rateLimitAccountRepoStub
	rateLimitedCalls  int
	lastRateLimitedAt time.Time
}

func (r *cnFrequencyRepoStub) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.rateLimitedCalls++
	r.lastRateLimitedAt = resetAt
	return nil
}

// cnFrequencyAccount 构造带统一用量快照的 Coding Plan 账号；used 为 nil 表示没有快照。
func cnFrequencyAccount(platform string, used ...float64) *Account {
	account := cnCodingTestAccount(platform)
	if len(used) > 0 {
		now := time.Now().UTC()
		limits := make([]UpstreamUsageLimit, 0, len(used))
		for i, value := range used {
			v := value
			reset := now.Add(time.Duration(i+2) * time.Hour)
			limits = append(limits, UpstreamUsageLimit{Name: []string{"5h", "weekly", "monthly"}[i], Used: &v, ResetAt: &reset})
		}
		attachCNMonitorLimits(account, now, limits)
	}
	return account
}

func runCNFrequency429(t *testing.T, account *Account, body string) *cnFrequencyRepoStub {
	t.Helper()
	repo := &cnFrequencyRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, []byte(body))
	require.Zero(t, repo.setErrorCalls, "429 不能把账号置为错误")
	require.Equal(t, 1, repo.rateLimitedCalls)
	return repo
}

// 额度充裕 + 限频文案：走秒级 429 兜底，而不是冷却到 5h 窗口结束；MiniMax Coding 同样适用。
func TestCNCodingPlanFrequency429UsesShortCooldown(t *testing.T) {
	for _, platform := range []string{PlatformZhipu, PlatformMiniMax} {
		repo := runCNFrequency429(t, cnFrequencyAccount(platform, 12, 4), zhipuFrequencyLimitedBody)
		require.WithinDuration(t, time.Now().Add(defaultRateLimit429CooldownSeconds*time.Second), repo.lastRateLimitedAt, 30*time.Second, platform)
	}
}

// 护栏：快照显示窗口接近耗尽、文案含额度类字样、或没有快照时，仍冷却到窗口重置点。
func TestCNCodingPlanFrequency429KeepsWindowCooldownWhenQuotaUnclear(t *testing.T) {
	cases := map[string]struct {
		account *Account
		body    string
	}{
		"窗口接近耗尽": {cnFrequencyAccount(PlatformZhipu, 95, 40), zhipuFrequencyLimitedBody},
		"额度类文案":  {cnFrequencyAccount(PlatformZhipu, 12, 4), `{"error":{"message":"速率限制：本周期额度已用尽"}}`},
	}
	for name, tc := range cases {
		repo := runCNFrequency429(t, tc.account, tc.body)
		require.True(t, repo.lastRateLimitedAt.After(time.Now().Add(time.Hour)), name)
	}
	// 没有快照时无法证明与额度无关，也不降级；此时窗口重置点未知，交给默认兜底冷却。
	require.True(t, cnProviderQuotaSnapshotNearExhausted(cnFrequencyAccount(PlatformZhipu), cnFrequencyLimitQuotaGuardPercent, time.Now()))
}

func TestCNProviderResponseIndicatesFrequencyLimit(t *testing.T) {
	for _, body := range []string{zhipuFrequencyLimitedBody, `{"error":{"message":"Too Many Requests"}}`, `{"error":{"message":"请求过于频繁"}}`} {
		require.True(t, cnProviderResponseIndicatesFrequencyLimit([]byte(body)), body)
	}
	for _, body := range []string{``, `{"error":{"message":"usage limit reached"}}`, `{"error":{"message":"rate limit exceeded: quota"}}`, `{"error":{"message":"server busy"}}`} {
		require.False(t, cnProviderResponseIndicatesFrequencyLimit([]byte(body)), body)
	}
}
