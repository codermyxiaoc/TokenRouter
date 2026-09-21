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

// rateLimitKimiQuotaRepoStub 记录配额 403 分支写入的限流时间，避免测试依赖真实数据库。
type rateLimitKimiQuotaRepoStub struct {
	rateLimitAccountRepoStub
	setRateLimitedCalls int
	lastRateLimitedAt   time.Time
}

func (r *rateLimitKimiQuotaRepoStub) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.setRateLimitedCalls++
	r.lastRateLimitedAt = resetAt
	return nil
}

func TestIsCNProviderQuotaExhausted403RequiresCodingPlanAndExactSignal(t *testing.T) {
	t.Parallel()

	kimiCoding := cnCodingTestAccount(PlatformKimi)
	kimiPayG := &Account{
		Platform:    PlatformKimi,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "account_mode": AccountModePayG},
	}

	tests := []struct {
		name    string
		account *Account
		body    string
		message string
		want    bool
	}{
		{
			name:    "structured access terminated error",
			account: kimiCoding,
			body:    `{"error":{"type":"access_terminated_error"}}`,
			want:    true,
		},
		{
			name:    "usage limit message",
			account: kimiCoding,
			message: "The usage limit has been reached",
			want:    true,
		},
		{
			name:    "quota reset message",
			account: kimiCoding,
			message: "Quota will reset in 2 hours",
			want:    true,
		},
		{
			name:    "payg account is not treated as window quota",
			account: kimiPayG,
			body:    `{"error":{"type":"access_terminated_error"}}`,
			want:    false,
		},
		{
			name:    "generic quota wording is not enough",
			account: kimiCoding,
			message: "quota exhausted",
			want:    false,
		},
		{
			name:    "concurrency wording remains separate",
			account: kimiCoding,
			message: kimiConcurrentRequestLimitMessage,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isCNProviderQuotaExhausted403(tt.account, []byte(tt.body), tt.message))
		})
	}
}

func TestRateLimitService_KimiCodingPlanQuota403UsesSnapshotCooldown(t *testing.T) {
	t.Parallel()

	account := cnCodingTestAccount(PlatformKimi)
	now := time.Now()
	reset := now.Add(2 * time.Hour)
	used := 100.0
	attachCNMonitorLimits(account, now, []UpstreamUsageLimit{{Name: "5h", Used: &used, ResetAt: &reset}})

	repo := &rateLimitKimiQuotaRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	blocker := &runtimeBlockRecorder{}
	service.SetAccountRuntimeBlocker(blocker)

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"type":"access_terminated_error","message":"usage limit reached"}}`),
	)

	require.True(t, shouldDisable, "the current request must fail over")
	require.Equal(t, 0, repo.setErrorCalls, "quota exhaustion must not permanently disable the account")
	require.Equal(t, 0, repo.tempCalls, "a valid quota snapshot should use the rate-limited state")
	require.Equal(t, 1, repo.setRateLimitedCalls)
	require.WithinDuration(t, reset, repo.lastRateLimitedAt, time.Second)
	require.Len(t, blocker.accounts, 1)
	require.Equal(t, cnQuotaExhaustedReasonPrefix, blocker.reasons[0])
}

func TestRateLimitService_KimiCodingPlanQuota403FallsBackToTemporaryCooldown(t *testing.T) {
	t.Parallel()

	account := cnCodingTestAccount(PlatformKimi)
	repo := &rateLimitKimiQuotaRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	blocker := &runtimeBlockRecorder{}
	service.SetAccountRuntimeBlocker(blocker)

	before := time.Now()
	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"The usage limit has been reached"}}`),
	)

	require.True(t, shouldDisable, "the current request must fail over")
	require.Equal(t, 0, repo.setErrorCalls, "missing snapshot must not permanently disable the account")
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, 0, repo.setRateLimitedCalls)
	require.Contains(t, repo.lastTempReason, cnQuotaExhaustedReasonPrefix)
	require.WithinDuration(t, before.Add(openAI403CooldownMinutesDefault*time.Minute), repo.lastTempUntil, 2*time.Second)
	require.Len(t, blocker.accounts, 1)
	require.Equal(t, cnQuotaExhaustedReasonPrefix, blocker.reasons[0])
}
