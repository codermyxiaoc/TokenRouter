package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// MiniMax 固定响应样本同时包含无关视频套餐，确保它不会污染编程额度窗口。
const minimaxUsageFixture = `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"video","current_interval_remaining_percent":1},{"model_name":"general","current_interval_remaining_percent":"25","end_time":1900000000000,"current_weekly_status":1,"current_weekly_remaining_percent":40,"weekly_end_time":1900604800000}]}`

func TestMiniMaxUpstreamUsagePreservesConfiguredHost(t *testing.T) {
	for _, test := range []struct{ baseURL, wantURL string }{
		{"", "https://api.minimax.io/v1/api/openplatform/coding_plan/remains"},
		{"https://api.minimax.cn/anthropic", "https://api.minimax.cn/v1/api/openplatform/coding_plan/remains"},
		{"https://api.minimaxi.com/v1", "https://api.minimaxi.com/v1/api/openplatform/coding_plan/remains"},
		{"https://relay.example/anthropic", "https://relay.example/v1/api/openplatform/coding_plan/remains"},
	} {
		t.Run(test.baseURL, func(t *testing.T) {
			account := newCNUsageMonitorAccount(81, PlatformMiniMax, AccountModeCoding)
			if test.baseURL != "" {
				account.Credentials["base_url"] = test.baseURL
			}
			// 手动选择通用适配器也不能改变 MiniMax 账号的供应商身份。
			account.Extra[UpstreamUsageQueryExtraKey] = map[string]any{"adapter": UpstreamUsageAdapterSub2API}
			upstream := &cnUsageMonitorHTTP{body: minimaxUsageFixture}
			service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
			result, err := service.QueryAccount(context.Background(), account.ID)
			require.NoError(t, err)
			require.Equal(t, UpstreamUsageAdapterMiniMaxCoding, result.Adapter)
			require.Equal(t, PlatformMiniMax, result.Provider)
			require.Equal(t, "PERCENT", result.Unit)
			require.Equal(t, "limits", result.Mode)
			require.Nil(t, result.Balance)
			require.Len(t, result.Limits, 2)
			require.Equal(t, "5h", result.Limits[0].Name)
			require.Equal(t, 75.0, *result.Limits[0].Used)
			require.Equal(t, 25.0, *result.Limits[0].Remaining)
			require.Equal(t, time.UnixMilli(1900000000000).UTC(), *result.Limits[0].ResetAt)
			require.Equal(t, "weekly", result.Limits[1].Name)
			require.Equal(t, 60.0, *result.Limits[1].Used)
			require.Len(t, upstream.requests, 1)
			request := upstream.requests[0]
			require.Equal(t, test.wantURL, request.URL.String())
			require.Equal(t, "Bearer sk-test", request.Header.Get("Authorization"))
			require.True(t, HTTPUpstreamRedirectsDisabled(request.Context()))
		})
	}
}

func TestMiniMaxUpstreamUsagePayGUnsupportedWithoutRequest(t *testing.T) {
	account := newCNUsageMonitorAccount(82, PlatformMiniMax, AccountModePayG)
	upstream := &cnUsageMonitorHTTP{}
	service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
	_, err := service.QueryAccount(context.Background(), account.ID)
	require.ErrorIs(t, err, ErrUpstreamUsageUnsupported)
	require.Zero(t, upstream.calls)
}

func TestMiniMaxUsageRejectsMalformedOrFailedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"业务错误":    `{"base_resp":{"status_code":1004},"model_remains":[{"model_name":"general","current_interval_remaining_percent":25}]}`,
		"缺少业务状态":  `{"model_remains":[{"model_name":"general","current_interval_remaining_percent":25}]}`,
		"没有编程套餐":  `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"video","current_interval_remaining_percent":25}]}`,
		"缺少五小时额度": `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"general"}]}`,
		"缺少有效周额度": `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"general","current_interval_remaining_percent":25,"current_weekly_status":1}]}`,
		"负剩余额度":   `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"general","current_interval_remaining_percent":-1}]}`,
		"超过百分比上限": `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"general","current_interval_remaining_percent":101}]}`,
		"非有限额度":   `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"general","current_interval_remaining_percent":"NaN"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			account := newCNUsageMonitorAccount(83, PlatformMiniMax, AccountModeCoding)
			upstream := &cnUsageMonitorHTTP{body: body}
			service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
			_, err := service.QueryAccount(context.Background(), account.ID)
			require.ErrorIs(t, err, ErrUpstreamUsageInvalidResponse)
		})
	}
}

func TestMiniMaxUsageOmitsInactiveWeeklyWindow(t *testing.T) {
	// 周额度未开放时忽略其哨兵值；秒级与毫秒级重置时间使用统一归一化规则。
	for _, timestamp := range []int64{1900000000, 1900000000000} {
		body := fmt.Sprintf(`{"model_remains":[{"model_name":"general","current_interval_remaining_percent":100,"end_time":%d,"current_weekly_status":3,"current_weekly_remaining_percent":-1}]}`, timestamp)
		tiers := parseMiniMaxUsageTiers([]byte(body))
		require.Len(t, tiers, 1)
		require.Zero(t, tiers[0].UsedPercent)
		require.Equal(t, time.Unix(1900000000, 0).UTC().Format(time.RFC3339), tiers[0].ResetAt)
	}
}

func TestMiniMaxUsageMonitorPreservesFailedSnapshotAndThreshold(t *testing.T) {
	account := newCNUsageMonitorAccount(84, PlatformMiniMax, AccountModeCoding)
	payg := newCNUsageMonitorAccount(85, PlatformMiniMax, AccountModePayG)
	repo := &cnUsageMonitorRepo{
		accounts:   map[int64]*Account{84: account, 85: payg},
		byPlatform: map[string][]int64{PlatformMiniMax: {84, 85}},
		casResult:  true,
	}
	upstream := &cnUsageMonitorHTTP{body: minimaxUsageFixture}
	service := newCNUsageMonitorForTest(repo, upstream, testUpstreamUsageConfig())
	service.runOnce(context.Background())
	require.Len(t, repo.writes, 1)
	require.Equal(t, 1, upstream.calls, "PAYG 不应进入周期监控")
	first := *repo.writes[0]
	require.Nil(t, first.LastError)
	require.Equal(t, UpstreamUsageAdapterMiniMaxCoding, first.Adapter)
	account.Extra[CNUsageMonitorSnapshotExtraKey] = &first
	upstream.status = http.StatusBadGateway
	service.runOnce(context.Background())
	require.Len(t, repo.writes, 2)
	last := repo.writes[1]
	require.NotNil(t, last.LastError)
	require.Equal(t, first.ObservedAt, last.ObservedAt)
	require.Equal(t, first.Limits, last.Limits)
	require.Zero(t, repo.updateExtraCalls)
	require.Empty(t, repo.pauseReason, "百分比窗口不能作为货币余额停调")
	account.Extra[CNUsageMonitorSnapshotExtraKey] = last
	decision := EvaluateAccountSchedulingThreshold(account, map[string]int{PlatformMiniMax: 70}, time.Now())
	require.True(t, decision.ShouldPause)
	require.Equal(t, "5h", decision.Window)
	require.Equal(t, PlatformMiniMax, decision.Scope)
	// 更换凭据立即使旧身份快照失效，不得沿用其停调结论。
	account.Credentials["api_key"] = "changed-key"
	require.False(t, EvaluateAccountSchedulingThreshold(account, map[string]int{PlatformMiniMax: 70}, time.Now()).ShouldPause)
}

func TestMiniMaxUsageMonitorRequiresExactOfficialOrAllowedHost(t *testing.T) {
	account := newCNUsageMonitorAccount(86, PlatformMiniMax, AccountModeCoding)
	service := newCNUsageMonitorForTest(&cnUsageMonitorRepo{}, &cnUsageMonitorHTTP{}, testUpstreamUsageConfig())
	for _, host := range []string{"api.minimax.io", "api.minimax.cn", "api.minimaxi.com"} {
		require.NoError(t, service.validateMonitorHost(account, UpstreamUsageQueryConfig{BaseURL: "https://" + host + "/anthropic"}))
	}
	for _, host := range []string{"relay.example", "api.minimax.io.evil.example", "api.deepseek.com"} {
		require.Error(t, service.validateMonitorHost(account, UpstreamUsageQueryConfig{BaseURL: "https://" + host + "/v1"}))
	}
	service.cfg.Security.URLAllowlist.Enabled = true
	service.cfg.Security.URLAllowlist.UpstreamHosts = []string{"relay.example"}
	require.NoError(t, service.validateMonitorHost(account, UpstreamUsageQueryConfig{BaseURL: "https://relay.example/v1"}))
}
