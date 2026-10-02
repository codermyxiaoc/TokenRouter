package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// 通用按量查询只访问本地模拟服务，记录完整请求以验证协议路径和认证隔离。
type payGUsageTestHTTP struct {
	client   *http.Client
	requests []*http.Request
}

func (h *payGUsageTestHTTP) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	return h.DoWithTLS(req, proxyURL, accountID, concurrency, nil)
}

func (h *payGUsageTestHTTP) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	h.requests = append(h.requests, req.Clone(req.Context()))
	return h.client.Do(req)
}

const payGUsageBalanceFixture = `{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"payg","remaining":12.5,"balance":12.5}`

func newPayGUsageTestAccount(platform string) *Account {
	mode := AccountModePayG
	if platform == PlatformOpenCodeGo {
		mode = AccountModeZen
	}
	account := newCNUsageMonitorAccount(981, platform, mode)
	account.Credentials["base_url"] = "https://relay.example/v1"
	return account
}

// 三个平台分别覆盖三个通用协议，并证明独立查询地址与转发地址的选择不串线。
func TestPayGUpstreamUsageAdapterMatrix(t *testing.T) {
	for _, platform := range []string{PlatformOpenCodeGo, PlatformMiniMax, PlatformZhipu} {
		for _, adapter := range []string{UpstreamUsageAdapterSub2API, UpstreamUsageAdapterNewAPI, UpstreamUsageAdapterZivv} {
			for _, source := range []string{"account_base", "query_override"} {
				t.Run(platform+"/"+adapter+"/"+source, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						switch r.URL.Path {
						case "/relay/v1/usage":
							_, _ = io.WriteString(w, payGUsageBalanceFixture)
						case "/relay/v1/user/balance":
							_, _ = io.WriteString(w, `{"balance":12.5,"currency":"USD","is_available":true,"key_limit":0,"key_used":1,"total_used":2}`)
						case "/relay/api/status":
							_, _ = io.WriteString(w, `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`)
						case "/relay/api/usage/token/":
							_, _ = io.WriteString(w, `{"code":true,"data":{"object":"token_usage","unlimited_quota":true,"expires_at":0}}`)
						case "/relay/api/user/self":
							_, _ = io.WriteString(w, `{"success":true,"data":{"id":42,"quota":6250000}}`)
						default:
							http.NotFound(w, r)
						}
					}))
					defer server.Close()
					account := newPayGUsageTestAccount(platform)
					query := map[string]any{"adapter": adapter}
					if source == "query_override" {
						query["base_url"] = server.URL + "/relay/v1"
					} else {
						account.Credentials["base_url"] = server.URL + "/relay/v1"
					}
					account.Credentials[NewAPIUserAccessTokenCredentialKey] = "wallet-test-token"
					account.Credentials[NewAPIUserIDCredentialKey] = "42"
					account.Extra[UpstreamUsageQueryExtraKey] = query
					before := string(mustJSONMarshal(t, account))
					client := server.Client()
					client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
					upstream := &payGUsageTestHTTP{client: client}
					service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
					result, err := service.QueryAccount(context.Background(), account.ID)
					require.NoError(t, err)
					require.Equal(t, adapter, result.Adapter)
					require.Equal(t, adapter, result.Provider)
					require.Equal(t, 12.5, *result.Balance.Remaining)
					require.Equal(t, before, string(mustJSONMarshal(t, account)), "手动查询不能写账号、监控快照或调度状态")
					require.NotContains(t, string(mustJSONMarshal(t, result)), "wallet-test-token")
					wantPaths := []string{"/relay/v1/usage"}
					if adapter == UpstreamUsageAdapterZivv {
						wantPaths = []string{"/relay/v1/user/balance"}
					} else if adapter == UpstreamUsageAdapterNewAPI {
						wantPaths = []string{"/relay/api/status", "/relay/api/usage/token/", "/relay/api/user/self"}
					}
					require.Len(t, upstream.requests, len(wantPaths))
					for index, request := range upstream.requests {
						require.Equal(t, http.MethodGet, request.Method)
						require.Equal(t, wantPaths[index], request.URL.Path)
						require.Equal(t, server.URL, request.URL.Scheme+"://"+request.URL.Host)
						require.True(t, HTTPUpstreamRedirectsDisabled(request.Context()))
						wantAuth := "Bearer sk-test"
						if strings.HasSuffix(request.URL.Path, "/api/status") {
							wantAuth = ""
						} else if strings.HasSuffix(request.URL.Path, "/api/user/self") {
							wantAuth = "Bearer wallet-test-token"
							require.Equal(t, "42", request.Header.Get("New-Api-User"))
						} else {
							require.Empty(t, request.Header.Get("New-Api-User"))
						}
						require.Equal(t, wantAuth, request.Header.Get("Authorization"))
					}
				})
			}
		}
	}
}

// 缺省配置与历史 Zen 保存值只影响本次生效配置，不原地改写持久化对象。
func TestPayGUpstreamUsageDefaultAndLegacyZen(t *testing.T) {
	for _, platform := range []string{PlatformOpenCodeGo, PlatformMiniMax, PlatformZhipu} {
		t.Run(platform, func(t *testing.T) {
			account := newPayGUsageTestAccount(platform)
			if platform != PlatformOpenCodeGo {
				delete(account.Credentials, "account_mode")
			}
			config, err := EffectiveUpstreamUsageConfig(account)
			require.NoError(t, err)
			require.Equal(t, UpstreamUsageAdapterSub2API, config.Adapter)
			require.True(t, config.Enabled)
			if platform == PlatformOpenCodeGo {
				account.Extra[UpstreamUsageQueryExtraKey] = map[string]any{"adapter": UpstreamUsageAdapterOpenCodeGo}
			}
			upstream := &cnUsageMonitorHTTP{body: payGUsageBalanceFixture}
			service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
			result, err := service.QueryAccount(context.Background(), account.ID)
			require.NoError(t, err)
			require.Equal(t, UpstreamUsageAdapterSub2API, result.Adapter)
			require.Equal(t, 1, upstream.calls)
			require.Equal(t, "/v1/usage", upstream.requests[0].URL.Path)
			if platform == PlatformOpenCodeGo {
				require.Equal(t, UpstreamUsageAdapterOpenCodeGo, account.Extra[UpstreamUsageQueryExtraKey].(map[string]any)["adapter"])
			}
		})
	}
}

// 原生窗口与原生钱包保留平台固定协议，编辑通用选项不会改变原来的监控身份。
func TestPayGUpstreamUsagePreservesNativeAdapters(t *testing.T) {
	for _, test := range []struct{ platform, mode, adapter string }{
		{PlatformKimi, AccountModePayG, UpstreamUsageAdapterKimiBalance},
		{PlatformDeepseek, AccountModePayG, UpstreamUsageAdapterDeepseekBalance},
		{PlatformKimi, AccountModeCoding, UpstreamUsageAdapterKimiCoding},
		{PlatformZhipu, AccountModeCoding, UpstreamUsageAdapterZhipuCoding},
		{PlatformMiniMax, AccountModeCoding, UpstreamUsageAdapterMiniMaxCoding},
		{PlatformOpenCodeGo, AccountModeGo, UpstreamUsageAdapterOpenCodeGo},
		{PlatformOpenCodeGo, "", UpstreamUsageAdapterOpenCodeGo},
	} {
		for _, adapter := range []string{UpstreamUsageAdapterSub2API, UpstreamUsageAdapterNewAPI, UpstreamUsageAdapterZivv} {
			t.Run(test.platform+"/"+test.mode+"/"+adapter, func(t *testing.T) {
				account := newCNUsageMonitorAccount(982, test.platform, test.mode)
				account.Extra[UpstreamUsageQueryExtraKey] = map[string]any{"adapter": adapter}
				config, err := EffectiveUpstreamUsageConfig(account)
				require.NoError(t, err)
				require.Equal(t, test.adapter, config.Adapter)
				require.Equal(t, test.adapter, cnUpstreamUsageAdapterName(account))
				require.False(t, supportsGenericPayGUpstreamUsage(account))
			})
		}
	}
}

// 放开查询资格不能绕过关闭开关、固定协议隔离、URL 白名单和私网限制。
func TestPayGUpstreamUsageRejectsUnsafeOrDisabledQueries(t *testing.T) {
	for _, platform := range []string{PlatformOpenCodeGo, PlatformMiniMax, PlatformZhipu} {
		for _, test := range []struct {
			name  string
			query map[string]any
		}{
			{"disabled", map[string]any{"enabled": false}},
			{"native_adapter", map[string]any{"adapter": UpstreamUsageAdapterMiniMaxCoding}},
			{"unknown_adapter", map[string]any{"adapter": "custom-script"}},
			{"credentials_in_url", map[string]any{"base_url": "https://user:secret@relay.example/v1"}},
			{"query_in_url", map[string]any{"base_url": "https://relay.example/v1?token=secret"}},
			{"private_host", map[string]any{"base_url": "https://127.0.0.1/v1"}},
			{"unlisted_host", map[string]any{"base_url": "https://other.example/v1"}},
		} {
			t.Run(platform+"/"+test.name, func(t *testing.T) {
				account := newPayGUsageTestAccount(platform)
				account.Extra[UpstreamUsageQueryExtraKey] = test.query
				cfg := testUpstreamUsageConfig()
				cfg.Security.URLAllowlist.Enabled = true
				cfg.Security.URLAllowlist.AllowInsecureHTTP = false
				cfg.Security.URLAllowlist.AllowPrivateHosts = false
				cfg.Security.URLAllowlist.UpstreamHosts = []string{"relay.example"}
				upstream := &upstreamUsageHTTPStub{}
				service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, cfg, nil)
				_, err := service.QueryAccount(context.Background(), account.ID)
				require.Error(t, err)
				require.Empty(t, upstream.requests)
			})
		}
	}
}

// 即便查询中的账号仍为同一 ID，钱包令牌或适配器变更也必须丢弃旧身份结果。
func TestPayGUpstreamUsageRejectsIdentityChanges(t *testing.T) {
	for _, platform := range []string{PlatformOpenCodeGo, PlatformMiniMax, PlatformZhipu} {
		for _, change := range []string{"wallet_token", "adapter"} {
			t.Run(platform+"/"+change, func(t *testing.T) {
				account := newPayGUsageTestAccount(platform)
				repo := &upstreamUsageAccountRepoStub{account: account}
				upstream := &blockingUpstreamUsageHTTP{started: make(chan struct{}), release: make(chan struct{}), body: payGUsageBalanceFixture}
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
				repo.mu.Lock()
				if change == "wallet_token" {
					credentials := make(map[string]any, len(account.Credentials)+1)
					for key, value := range account.Credentials {
						credentials[key] = value
					}
					credentials[NewAPIUserAccessTokenCredentialKey] = "new-wallet-token"
					repo.account.Credentials = credentials
				} else {
					repo.account.Extra = map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"adapter": UpstreamUsageAdapterZivv}}
				}
				repo.mu.Unlock()
				close(upstream.release)
				require.ErrorIs(t, <-done, ErrUpstreamUsageIdentityChanged)
			})
		}
	}
}

// 通用查询不授予监控资格，伪造同身份快照也不能影响账号额度调度。
func TestPayGUpstreamUsageNeverEntersNativeMonitor(t *testing.T) {
	for _, platform := range []string{PlatformOpenCodeGo, PlatformMiniMax, PlatformZhipu} {
		t.Run(platform, func(t *testing.T) {
			account := newPayGUsageTestAccount(platform)
			account.Extra[UpstreamUsageQueryExtraKey] = map[string]any{"adapter": UpstreamUsageAdapterSub2API}
			config, err := EffectiveUpstreamUsageConfig(account)
			require.NoError(t, err)
			now, reset := time.Now().UTC(), time.Now().UTC().Add(time.Hour)
			used, limit, remaining := 100.0, 100.0, 0.0
			account.Extra[CNUsageMonitorSnapshotExtraKey] = &CNUsageMonitorSnapshot{
				Version: cnUsageMonitorSnapshotVersion, Adapter: config.Adapter,
				IdentityHash: upstreamUsageContextFingerprint(account, config), Provider: platform,
				Mode: "limits", Unit: "PERCENT", ObservedAt: &now,
				Limits: []UpstreamUsageLimit{{Name: "5h", Used: &used, Limit: &limit, Remaining: &remaining, ResetAt: &reset}},
			}
			require.Nil(t, validCNUsageMonitorSnapshot(account))
			require.Nil(t, cnProviderQuotaSnapshotReset(account, now))
			require.False(t, EvaluateAccountSchedulingThreshold(account, map[string]int{platform: 70}, now).ShouldPause)
			repo := &cnUsageMonitorRepo{accounts: map[int64]*Account{account.ID: account}, byPlatform: map[string][]int64{platform: {account.ID}}, casResult: true}
			upstream := &cnUsageMonitorHTTP{body: payGUsageBalanceFixture}
			monitor := newCNUsageMonitorForTest(repo, upstream, testUpstreamUsageConfig())
			monitor.runOnce(context.Background())
			monitor.probeOne(context.Background(), account.ID)
			require.Zero(t, upstream.calls)
			require.Empty(t, repo.writes)
			require.Empty(t, repo.pauseReason)
			require.Zero(t, repo.updateExtraCalls)
		})
	}
}
