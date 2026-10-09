package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func appendAggregatorResponse(client *upstreamUsageHTTPStub, status int, body string) {
	client.responses = append(client.responses, struct {
		status int
		body   string
		err    error
	}{status: status, body: body})
}

// 官方账户查询取数必须保持组织身份、金额单位与钱包隔离。
func TestAggregatorUsageQueriesOfficialContracts(t *testing.T) {
	for _, platform := range []string{PlatformCline, PlatformCommandCode} {
		t.Run(platform, func(t *testing.T) {
			a := &Account{ID: 1, Type: AccountTypeAPIKey, Platform: platform, Status: StatusActive, Credentials: map[string]any{"api_key": "private-key"}}
			upstream := &upstreamUsageHTTPStub{}
			if platform == PlatformCline {
				appendAggregatorResponse(upstream, 200, `{"success":true,"data":{"id":"u1"}}`)
				appendAggregatorResponse(upstream, 200, `{"success":true,"data":{"balance":2500000}}`)
				appendAggregatorResponse(upstream, 200, `{"success":true,"data":{"limits":[{"type":"five_hour","percentUsed":40}]}}`)
			} else {
				appendAggregatorResponse(upstream, 200, `{"org":{"id":"org/a"}}`)
				appendAggregatorResponse(upstream, 200, `{"credits":{"monthlyCredits":"2","purchasedCredits":3,"freeCredits":0},"windowLimits":{"limited":true,"fiveHour":{"used":2,"cap":2,"resetAt":9999999999999}}}`)
				appendAggregatorResponse(upstream, 200, `{"data":{"planId":"pro","currentPeriodStart":"2026-01-01T00:00:00Z","currentPeriodEnd":"2026-02-01T00:00:00Z"}}`)
				appendAggregatorResponse(upstream, 200, `{"totalMonthlyCredits":8,"totalCost":10}`)
			}
			svc := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: a}, upstream, testUpstreamUsageConfig(), nil)
			result, err := svc.QueryAccount(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, "wallets", result.Mode)
			if platform == PlatformCline {
				require.Equal(t, 2.5, *result.Balance.Remaining)
				require.Equal(t, "PERCENT", result.Limits[0].Unit)
			} else {
				require.Equal(t, 5.0, *result.Balance.Remaining)
				require.Equal(t, 3.0, commandCodePurchasedBalance(result.Balances))
				require.Equal(t, 10.0, *result.Limits[1].Limit)
				require.Equal(t, "org/a", upstream.requests[1].URL.Query().Get("orgId"))
				require.NotEmpty(t, upstream.requests[3].URL.Query().Get("since"))
			}
			for _, req := range upstream.requests {
				require.Equal(t, "Bearer private-key", req.Header.Get("Authorization"))
				require.Equal(t, http.MethodGet, req.Method)
			}
		})
	}
}

func TestAggregatorCustomHostCannotSendCredentialsToOfficialUsage(t *testing.T) {
	for _, platform := range []string{PlatformCline, PlatformCommandCode} {
		a := &Account{Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.example/v1"}}
		require.Empty(t, cnUpstreamUsageAdapterName(a))
		require.Nil(t, validCNUsageMonitorSnapshot(a))
		cfg, err := EffectiveUpstreamUsageConfig(a)
		require.NoError(t, err)
		require.Equal(t, UpstreamUsageAdapterSub2API, cfg.Adapter)
	}
}

func TestCommandCodeThresholdIdentityAndPurchasedWallet(t *testing.T) {
	now := time.Now().UTC()
	account := newCNUsageMonitorAccount(801, PlatformCommandCode, AccountModePayG)
	queryConfig, err := EffectiveUpstreamUsageConfig(account)
	require.NoError(t, err)
	used, total, reset := 8.0, 10.0, now.Add(time.Hour)
	snapshot := &CNUsageMonitorSnapshot{Version: cnUsageMonitorSnapshotVersion, Adapter: UpstreamUsageAdapterCommandCode,
		IdentityHash: upstreamUsageContextFingerprint(account, queryConfig), Provider: PlatformCommandCode, Mode: "wallets", Unit: "USD", ObservedAt: &now,
		Limits: []UpstreamUsageLimit{{Name: "5h", Unit: "USD", Used: &used, Limit: &total, ResetAt: &reset}}}
	account.Extra[CNUsageMonitorSnapshotExtraKey] = snapshot
	decision := EvaluateAccountSchedulingThreshold(account, map[string]int{PlatformCommandCode: 70}, now)
	require.True(t, decision.ShouldPause)
	require.Equal(t, 80.0, decision.UsedPercent)
	snapshot.Balances = []UpstreamUsageBalanceEntry{{Kind: "purchased", Currency: "USD", Remaining: 1}}
	require.False(t, EvaluateAccountSchedulingThreshold(account, map[string]int{PlatformCommandCode: 70}, now).ShouldPause)
	snapshot.Balances = nil
	account.Credentials["api_key"] = "changed-key"
	require.False(t, EvaluateAccountSchedulingThreshold(account, map[string]int{PlatformCommandCode: 70}, now).ShouldPause)
}

// 套餐缺少周期字段仍可展示真实剩余量；身份异常不得降级查询个人钱包。
func TestCommandCodeUsagePartialPeriodAndMalformedIdentity(t *testing.T) {
	for _, orgJSON := range []string{`null`, `"broken"`, `{}`, `{"id":""}`, `{"id":123}`} {
		malformed := orgJSON != `null`
		account := &Account{ID: 82, Type: AccountTypeAPIKey, Platform: PlatformCommandCode, Credentials: map[string]any{"api_key": "private-key"}}
		upstream := &upstreamUsageHTTPStub{}
		if malformed {
			appendAggregatorResponse(upstream, 200, `{"org":`+orgJSON+`}`)
		} else {
			appendAggregatorResponse(upstream, 200, `{"org":null}`)
			appendAggregatorResponse(upstream, 200, `{"credits":{"monthlyCredits":2,"purchasedCredits":0,"freeCredits":0}}`)
			appendAggregatorResponse(upstream, 200, `{"data":{"planId":"pro"}}`)
		}
		svc := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
		result, err := svc.QueryAccount(context.Background(), account.ID)
		if malformed {
			require.Error(t, err)
			require.Len(t, upstream.requests, 1)
		} else {
			require.NoError(t, err)
			require.Equal(t, 2.0, *result.Subscription.Remaining)
			require.Empty(t, result.Subscription.Limits)
		}
	}
}

type aggregatorMonitorRepo struct {
	AccountRepository
	scopes  []string
	reasons []string
	until   []time.Time
	clears  int
}

func (r *aggregatorMonitorRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, until time.Time, reason ...string) error {
	r.scopes = append(r.scopes, scope)
	r.reasons = append(r.reasons, firstRequestedModel(reason))
	r.until = append(r.until, until)
	return nil
}
func (r *aggregatorMonitorRepo) SetTempUnschedulable(_ context.Context, _ int64, until time.Time, reason string) error {
	r.until = append(r.until, until)
	r.reasons = append(r.reasons, reason)
	return nil
}
func (r *aggregatorMonitorRepo) ClearTempUnschedulable(context.Context, int64) error {
	r.clears++
	return nil
}

func TestAggregatorWalletRestoreDoesNotClearOrganizationLimit(t *testing.T) {
	remaining := 10.0
	used, total := 20.0, 100.0
	now := time.Now()
	future := now.Add(time.Hour)
	a := &Account{ID: 1, Platform: PlatformCline, Type: AccountTypeAPIKey, Extra: map[string]any{}}
	setAccountModelRateLimitSnapshot(a, clineCreditsRateLimitKey, future, clineSpendLimitReason, now)
	r := &aggregatorMonitorRepo{}
	s := &CNProviderBalanceCheckService{accountRepo: r, interval: time.Minute}
	result := &UpstreamUsageQueryResult{Mode: "wallets", Balance: &UpstreamUsageAmount{Remaining: &remaining}, Subscription: &UpstreamUsageSubscription{PlanName: "pass"}, Limits: []UpstreamUsageLimit{{Name: "5h", Used: &used, Limit: &total, ResetAt: &future}}}
	s.applyAggregatorWalletDecision(context.Background(), a, result, 0.5, "identity")
	require.Empty(t, r.scopes)
	setAccountModelRateLimitSnapshot(a, clineCreditsRateLimitKey, future, clineCreditsReason+": low", now)
	s.applyAggregatorWalletDecision(context.Background(), a, result, 0.5, "identity")
	require.Equal(t, []string{clineCreditsRateLimitKey}, r.scopes)
	require.True(t, r.until[0].Before(time.Now()))
	a.Platform = PlatformCommandCode
	a.TempUnschedulableUntil = &future
	a.TempUnschedulableReason = commandCodeSpendLimitReason
	result.Balances = []UpstreamUsageBalanceEntry{{Kind: "purchased", Currency: "USD", Remaining: 5}}
	s.applyAggregatorWalletDecision(context.Background(), a, result, 0.5, "identity")
	require.Zero(t, r.clears)
	a.TempUnschedulableReason = commandCodeUsageLimitReason + ": window exhausted"
	s.applyAggregatorWalletDecision(context.Background(), a, result, 0.5, "identity")
	require.Equal(t, 1, r.clears)
}

func TestClineMissingResetExhaustionAndWalletScope(t *testing.T) {
	now := time.Now()
	used, total := 100.0, 100.0
	fallback := now.Add(time.Hour)
	require.Equal(t, &fallback, clineExhaustedReset([]UpstreamUsageLimit{{Name: "5h", Used: &used, Limit: &total}}, now, fallback))
	a := &Account{ID: 1, Platform: PlatformCline, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"alias": "cline-pass/model"}}, Extra: map[string]any{}}
	setAccountModelRateLimitSnapshot(a, clinePassRateLimitKey, fallback, clinePassLimitReason, now)
	require.True(t, a.isModelRateLimitedWithContext(context.Background(), "alias"))
	require.False(t, a.isModelRateLimitedWithContext(context.Background(), "deepseek/model"))
	require.False(t, a.isModelRateLimitedWithContext(context.Background(), "cline-free/model"))
	kind, _ := parseClineError([]byte(`{"error":{"message":"bad request","metadata":{"code":"insufficient_credits"}}}`))
	require.Equal(t, clineErrorNone, kind)
}
