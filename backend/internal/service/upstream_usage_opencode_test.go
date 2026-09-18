package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 官方 GO 返回三种窗口；已用百分比保持原单位，不转换为货币余额。
const openCodeGoUsageFixture = `{"usage":{"rolling":{"percent":25,"resetsAt":1900000000000},"weekly":{"percent":"80","resetsAt":1900604800},"monthly":{"percent":100,"resetsAt":"2030-04-01T00:00:00Z"}}}`

func TestOpenCodeGoUpstreamUsageUsesFixedAdapterAndConfiguredHost(t *testing.T) {
	for _, test := range []struct{ baseURL, wantURL string }{
		{"", "https://opencode.ai/zen/go/v1/usage"},
		{"https://opencode.ai/zen/go/v1/", "https://opencode.ai/zen/go/v1/usage"},
		{"https://relay.example/prefix/v1", "https://relay.example/prefix/v1/usage"},
	} {
		t.Run(test.wantURL, func(t *testing.T) {
			account := newCNUsageMonitorAccount(901, PlatformOpenCodeGo, AccountModeGo)
			if test.baseURL != "" {
				account.Credentials["base_url"] = test.baseURL
			}
			account.Extra[UpstreamUsageQueryExtraKey] = map[string]any{"adapter": UpstreamUsageAdapterSub2API}
			upstream := &cnUsageMonitorHTTP{body: openCodeGoUsageFixture}
			service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
			result, err := service.QueryAccount(context.Background(), account.ID)
			require.NoError(t, err)
			require.Equal(t, UpstreamUsageAdapterOpenCodeGo, result.Adapter)
			require.Equal(t, PlatformOpenCodeGo, result.Provider)
			require.Equal(t, "limits", result.Mode)
			require.Equal(t, "PERCENT", result.Unit)
			require.Nil(t, result.Balance)
			require.Len(t, result.Limits, 3)
			for index, name := range []string{"5h", "weekly", "monthly"} {
				require.Equal(t, name, result.Limits[index].Name)
				require.Equal(t, 100.0, *result.Limits[index].Limit)
				require.NotNil(t, result.Limits[index].ResetAt)
			}
			require.Equal(t, 25.0, *result.Limits[0].Used)
			require.Equal(t, 75.0, *result.Limits[0].Remaining)
			require.Equal(t, time.Unix(1900000000, 0).UTC(), *result.Limits[0].ResetAt)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, test.wantURL, upstream.requests[0].URL.String())
			require.Equal(t, "Bearer sk-test", upstream.requests[0].Header.Get("Authorization"))
			require.True(t, HTTPUpstreamRedirectsDisabled(upstream.requests[0].Context()))
			require.NotContains(t, account.Extra, CNUsageMonitorSnapshotExtraKey, "人工查询不写监控或调度状态")
		})
	}
}

func TestOpenCodeGoUpstreamUsageUnsupportedAndDisabledDoNotRequest(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    string
		enabled bool
		want    error
	}{
		{"Zen 没有余额查询协议", AccountModeZen, true, ErrUpstreamUsageUnsupported},
		{"GO 查询开关关闭", AccountModeGo, false, ErrUpstreamUsageDisabled},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := newCNUsageMonitorAccount(902, PlatformOpenCodeGo, test.mode)
			account.Extra[UpstreamUsageQueryExtraKey] = map[string]any{"enabled": test.enabled, "adapter": UpstreamUsageAdapterSub2API}
			upstream := &cnUsageMonitorHTTP{body: openCodeGoUsageFixture}
			service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
			_, err := service.QueryAccount(context.Background(), account.ID)
			require.ErrorIs(t, err, test.want)
			require.Zero(t, upstream.calls)
		})
	}
}

func TestOpenCodeGoUsageRejectsInvalidResponses(t *testing.T) {
	for name, body := range map[string]string{
		"非法 JSON":    `{"usage":`,
		"空响应":        `{}`,
		"空窗口":        `{"usage":{}}`,
		"负百分比":       `{"usage":{"rolling":{"percent":-1}}}`,
		"非有限百分比":     `{"usage":{"rolling":{"percent":"NaN"}}}`,
		"缺失百分比":      `{"usage":{"rolling":{"resetsAt":1900000000000}}}`,
		"有效窗口夹带无效窗口": `{"usage":{"rolling":{"percent":10},"monthly":{"percent":"bad"}}}`,
		"2xx 内错误":    `{"error":{"type":"GoUsageLimitError"},"usage":{"rolling":{"percent":10}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			account := newCNUsageMonitorAccount(903, PlatformOpenCodeGo, AccountModeGo)
			upstream := &cnUsageMonitorHTTP{body: body}
			service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
			_, err := service.QueryAccount(context.Background(), account.ID)
			require.ErrorIs(t, err, ErrUpstreamUsageInvalidResponse)
		})
	}
	// 尚无重置时间仍可显示真实用量，调度评估不能凭空生成恢复点。
	tiers := parseOpenCodeGoUsageTiers([]byte(`{"usage":{"rolling":{"percent":0}}}`))
	require.Len(t, tiers, 1)
	require.Empty(t, tiers[0].ResetAt)
}

func TestOpenCodeGoUsageRejectsModeEditDuringQuery(t *testing.T) {
	account := newCNUsageMonitorAccount(913, PlatformOpenCodeGo, AccountModeGo)
	repo := &upstreamUsageAccountRepoStub{account: account}
	upstream := &blockingUpstreamUsageHTTP{started: make(chan struct{}), release: make(chan struct{}), body: openCodeGoUsageFixture}
	service := NewUpstreamUsageService(repo, upstream, testUpstreamUsageConfig(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := service.QueryAccount(ctx, account.ID); done <- err }()
	select {
	case <-upstream.started:
	case <-ctx.Done():
		t.Fatal("用量查询未启动")
	}
	// 用新的凭据 map 模拟仓储重新读出编辑结果，保留调用方的旧身份快照。
	repo.mu.Lock()
	repo.account.Credentials = map[string]any{"api_key": "sk-test", "account_mode": AccountModeZen}
	repo.mu.Unlock()
	close(upstream.release)
	require.ErrorIs(t, <-done, ErrUpstreamUsageIdentityChanged)
	require.NotContains(t, account.Extra, CNUsageMonitorSnapshotExtraKey)
}

func TestOpenCodeGoUsageMonitorPreservesFailuresAndSkipsZen(t *testing.T) {
	account := newCNUsageMonitorAccount(904, PlatformOpenCodeGo, AccountModeGo)
	zen := newCNUsageMonitorAccount(905, PlatformOpenCodeGo, AccountModeZen)
	repo := &cnUsageMonitorRepo{accounts: map[int64]*Account{904: account, 905: zen}, byPlatform: map[string][]int64{PlatformOpenCodeGo: {904, 905}}, casResult: true}
	upstream := &cnUsageMonitorHTTP{body: openCodeGoUsageFixture}
	service := newCNUsageMonitorForTest(repo, upstream, testUpstreamUsageConfig())
	service.runOnce(context.Background())
	require.Equal(t, 1, upstream.calls)
	require.Len(t, repo.writes, 1)
	first := *repo.writes[0]
	account.Extra[CNUsageMonitorSnapshotExtraKey] = &first
	upstream.status = http.StatusBadGateway
	service.runOnce(context.Background())
	require.Len(t, repo.writes, 2)
	last := repo.writes[1]
	require.NotNil(t, last.LastError)
	require.Equal(t, first.ObservedAt, last.ObservedAt)
	require.Equal(t, first.Limits, last.Limits)
	require.Zero(t, repo.updateExtraCalls)
	require.Empty(t, repo.pauseReason, "窗口百分比不得触发余额不足停调")
	account.Extra[CNUsageMonitorSnapshotExtraKey] = last
	decision := EvaluateAccountSchedulingThreshold(account, map[string]int{PlatformOpenCodeGo: 70}, time.Now())
	require.True(t, decision.ShouldPause)
	require.Equal(t, "monthly", decision.Window)
}

func TestOpenCodeGoUsageMonitorOnlyOfficialOrAllowedHost(t *testing.T) {
	account := newCNUsageMonitorAccount(906, PlatformOpenCodeGo, AccountModeGo)
	service := newCNUsageMonitorForTest(&cnUsageMonitorRepo{}, &cnUsageMonitorHTTP{}, testUpstreamUsageConfig())
	require.NoError(t, service.validateMonitorHost(account, UpstreamUsageQueryConfig{BaseURL: DefaultOpenCodeGoBaseURL}))
	for _, host := range []string{"relay.example", "opencode.ai.evil.example", "api.openai.com"} {
		require.Error(t, service.validateMonitorHost(account, UpstreamUsageQueryConfig{BaseURL: "https://" + host + "/v1"}))
	}
	service.cfg.Security.URLAllowlist.Enabled = true
	service.cfg.Security.URLAllowlist.UpstreamHosts = []string{"relay.example"}
	require.NoError(t, service.validateMonitorHost(account, UpstreamUsageQueryConfig{BaseURL: "https://relay.example/prefix/v1"}))
}

// 用真实查询身份生成统一快照，测试复制、改配置与窗口重置后的停调边界。
func attachOpenCodeGoSnapshot(t *testing.T, account *Account, now time.Time) *CNUsageMonitorSnapshot {
	t.Helper()
	queryConfig, err := EffectiveUpstreamUsageConfig(account)
	require.NoError(t, err)
	used, total, remaining := 100.0, 100.0, 0.0
	five, week, month := now.Add(time.Hour), now.Add(24*time.Hour), now.Add(30*24*time.Hour)
	snapshot := &CNUsageMonitorSnapshot{
		Version: cnUsageMonitorSnapshotVersion, Adapter: UpstreamUsageAdapterOpenCodeGo,
		IdentityHash: upstreamUsageContextFingerprint(account, queryConfig), Provider: PlatformOpenCodeGo,
		Mode: "limits", Unit: "PERCENT", ObservedAt: &now,
		Limits: []UpstreamUsageLimit{
			{Name: "5h", Used: &used, Limit: &total, Remaining: &remaining, ResetAt: &five},
			{Name: "weekly", Used: &used, Limit: &total, Remaining: &remaining, ResetAt: &week},
			{Name: "monthly", Used: &used, Limit: &total, Remaining: &remaining, ResetAt: &month},
		},
	}
	account.Extra[CNUsageMonitorSnapshotExtraKey] = snapshot
	return snapshot
}

func TestOpenCodeGoQuotaResetUsesOnlyExhaustedAndLatestWindow(t *testing.T) {
	now := time.Now().UTC()
	account := newCNUsageMonitorAccount(907, PlatformOpenCodeGo, AccountModeGo)
	snapshot := attachOpenCodeGoSnapshot(t, account, now)
	require.Equal(t, now.Add(30*24*time.Hour), *cnProviderQuotaSnapshotReset(account, now))
	used := 10.0
	snapshot.Limits[2].Used = &used
	require.Equal(t, now.Add(24*time.Hour), *cnProviderQuotaSnapshotReset(account, now))
	snapshot.Limits[1].Used = &used
	snapshot.Limits[0].Used = &used
	require.Nil(t, cnProviderQuotaSnapshotReset(account, now), "未来但未耗尽的窗口不能解释 429")
}

func TestOpenCodeGoThresholdRejectsStaleIdentityOrMissingObservation(t *testing.T) {
	now := time.Now().UTC()
	for name, modify := range map[string]func(*Account, *CNUsageMonitorSnapshot){
		"更换 Key": func(a *Account, _ *CNUsageMonitorSnapshot) { a.Credentials["api_key"] = "changed" },
		"切到 Zen": func(a *Account, _ *CNUsageMonitorSnapshot) { a.Credentials["account_mode"] = AccountModeZen },
		"修改地址":   func(a *Account, _ *CNUsageMonitorSnapshot) { a.Credentials["base_url"] = "https://relay.example/v1" },
		"关闭查询": func(a *Account, _ *CNUsageMonitorSnapshot) {
			a.Extra[UpstreamUsageQueryExtraKey] = map[string]any{"enabled": false}
		},
		"缺少观测": func(_ *Account, s *CNUsageMonitorSnapshot) { s.ObservedAt = nil },
		"未来观测": func(_ *Account, s *CNUsageMonitorSnapshot) { future := now.Add(time.Minute); s.ObservedAt = &future },
		"窗口全部过期": func(_ *Account, s *CNUsageMonitorSnapshot) {
			for i := range s.Limits {
				expired := now.Add(-time.Second)
				s.Limits[i].ResetAt = &expired
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			account := newCNUsageMonitorAccount(908, PlatformOpenCodeGo, AccountModeGo)
			snapshot := attachOpenCodeGoSnapshot(t, account, now)
			require.True(t, EvaluateAccountSchedulingThreshold(account, map[string]int{PlatformOpenCodeGo: 70}, now).ShouldPause)
			modify(account, snapshot)
			require.False(t, EvaluateAccountSchedulingThreshold(account, map[string]int{PlatformOpenCodeGo: 70}, now).ShouldPause)
			require.Nil(t, cnProviderQuotaSnapshotReset(account, now))
		})
	}
}

type openCodeGoCooldownRepo struct {
	AccountRepository
	resets []time.Time
}

func (r *openCodeGoCooldownRepo) SetRateLimited(_ context.Context, _ int64, until time.Time) error {
	r.resets = append(r.resets, until)
	return nil
}

func TestOpenCodeGoReactive429UsesSnapshotOrExplicitErrorReset(t *testing.T) {
	now := time.Now().UTC()
	account := newCNUsageMonitorAccount(909, PlatformOpenCodeGo, AccountModeGo)
	attachOpenCodeGoSnapshot(t, account, now)
	repo := &openCodeGoCooldownRepo{}
	service := NewRateLimitService(repo, nil, nil, nil, nil)
	require.True(t, service.applyCNProviderReactive429(context.Background(), account, http.Header{}, nil))
	require.Equal(t, now.Add(30*24*time.Hour), repo.resets[0])
	delete(account.Extra, CNUsageMonitorSnapshotExtraKey)
	require.True(t, service.applyCNProviderReactive429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"GoUsageLimitError","message":"Weekly usage limit reached. Resets in 2 days."}}`)))
	require.WithinDuration(t, time.Now().Add(48*time.Hour), repo.resets[1], 2*time.Second)
	for _, body := range []string{`{}`, `{"error":{"type":"GoUsageLimitError","resets_at":1}}`} {
		require.False(t, service.applyCNProviderReactive429(context.Background(), account, http.Header{}, []byte(body)))
	}
	account.Credentials["account_mode"] = AccountModeZen
	require.False(t, service.applyCNProviderReactive429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"GoUsageLimitError","message":"Resets in 2 days."}}`)))
	require.Len(t, repo.resets, 2)
}
