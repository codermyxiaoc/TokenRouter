package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

// 账户开关与基准倍率同步只属于账号成本，默认不得替任何旧账号启用。
func TestBillingProbeAccountSwitchValidation(t *testing.T) {
	for _, tc := range []struct {
		name                string
		extra               map[string]any
		probe, syncRate     *bool
		wantProbe, wantSync *bool
		invalid             bool
	}{
		{name: "默认关闭"},
		{name: "同步带动探测", syncRate: probeBool(true), wantProbe: probeBool(true), wantSync: probeBool(true)},
		{name: "关闭探测同时关同步", probe: probeBool(false), wantProbe: probeBool(false), wantSync: probeBool(false)},
		{name: "字符串不是真布尔", extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: "true"}, invalid: true},
		{name: "空值不等于关闭", extra: map[string]any{UpstreamBillingRateSyncEnabledExtraKey: nil}, invalid: true},
		{name: "顶层优先", extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: true}, probe: probeBool(false), wantProbe: probeBool(false), wantSync: probeBool(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe, syncRate, err := normalizeBillingProbeSwitches(tc.extra, tc.probe, tc.syncRate)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantProbe, probe)
			require.Equal(t, tc.wantSync, syncRate)
		})
	}
}

func TestBillingProbeCreateSyncAndUnsupportedIdentity(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGemini, PlatformMiniMax, PlatformTypeSafe} {
		t.Run(platform, func(t *testing.T) {
			repo := &upstreamBillingProbeAccountRepo{}
			account, err := (&adminServiceImpl{accountRepo: repo}).CreateAccount(context.Background(), &CreateAccountInput{
				Name: "relay", Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://relay.example"}, SkipDefaultGroupBind: true,
				UpstreamBillingRateSyncEnabled: probeBool(true), Extra: map[string]any{UpstreamBillingProbeExtraKey: map[string]any{"status": "ok", "synced_rate_multiplier": 99}},
			})
			require.NoError(t, err)
			require.True(t, upstreamBillingRateSyncEnabled(account))
			require.NotContains(t, account.Extra, UpstreamBillingProbeExtraKey)
		})
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.ErrorIs(t, applyBillingProbeSwitches(account, probeBool(true), nil), ErrUpstreamBillingProbeAccountInvalid)
	require.False(t, IsUpstreamBillingProbeIdentity(PlatformAntigravity, AccountTypeAPIKey))
	require.False(t, IsUpstreamBillingProbeIdentity(PlatformQoder, AccountTypeAPIKey))
}

func TestBillingProbeEditingRateRequiresDisableSync(t *testing.T) {
	initial := 0.6
	changed := 0.25
	for _, disable := range []bool{false, true} {
		t.Run(map[bool]string{false: "同步中拒绝手改", true: "明确关闭后可手改"}[disable], func(t *testing.T) {
			repo := &accountServiceTestRepo{accounts: map[int64]*Account{1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, RateMultiplier: &initial, Extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: true, UpstreamBillingRateSyncEnabledExtraKey: true}}}}
			input := &UpdateAccountInput{RateMultiplier: &changed}
			if disable {
				input.UpstreamBillingRateSyncEnabled = probeBool(false)
			}
			result, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), 1, input)
			if !disable {
				require.ErrorIs(t, err, ErrUpstreamBillingRateSyncConflict)
				return
			}
			require.NoError(t, err)
			require.Equal(t, changed, result.BillingRateMultiplier())
			require.False(t, upstreamBillingRateSyncEnabled(result))
		})
	}
}

func TestBillingProbeQueueDoesNotBlockAccountCreation(t *testing.T) {
	svc := NewUpstreamBillingProbeService(nil, nil, nil)
	defer svc.Stop()
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: true}}
	for i := 0; i < 1000; i++ {
		svc.QueueAccount(account)
	}
	require.Equal(t, 100, len(svc.queue))
	var missing *UpstreamBillingProbeService
	missing.QueueAccount(account)
}

func probeBool(v bool) *bool { return &v }

func TestBillingProbeURLPreservesRelayPrefix(t *testing.T) {
	for _, item := range []struct{ base, want string }{
		{"https://relay.example", "https://relay.example/v1/sub2api/billing"},
		{"https://relay.example/relay/", "https://relay.example/relay/v1/sub2api/billing"},
		{"https://relay.example/relay/v1", "https://relay.example/relay/v1/sub2api/billing"},
		{"https://relay.example/relay/v1beta", "https://relay.example/relay/v1/sub2api/billing"},
		{"https://relay.example/relay/anthropic", "https://relay.example/relay/v1/sub2api/billing"},
		{"https://relay.example/relay/anthropic/v1", "https://relay.example/relay/v1/sub2api/billing"},
	} {
		require.Equal(t, item.want, buildUpstreamBillingProbeURL(item.base))
	}
}

// 旧弹窗回传 Extra 不能重新启用另一位管理员已经关闭的同步开关。
func TestBillingProbeStaleExtraCannotReenableSwitches(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		repo := &accountServiceTestRepo{accounts: map[int64]*Account{1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: false, UpstreamBillingRateSyncEnabledExtraKey: false}}}}
		input := &UpdateAccountInput{Name: "renamed", Extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: true, UpstreamBillingRateSyncEnabledExtraKey: true}}
		if explicit {
			input.UpstreamBillingRateSyncEnabled = probeBool(true)
		}
		account, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), 1, input)
		require.NoError(t, err)
		require.Equal(t, explicit, upstreamBillingRateSyncEnabled(account))
		require.Equal(t, explicit, upstreamBillingProbeEnabled(account))
	}
}
