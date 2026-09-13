package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type upstreamUsageAccountRepoStub struct {
	AccountRepository
	mu       sync.Mutex
	account  *Account
	getEvent chan struct{}
}

func (s *upstreamUsageAccountRepoStub) GetByID(_ context.Context, _ int64) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.account == nil {
		return nil, ErrAccountNotFound
	}
	if s.getEvent != nil {
		select {
		case s.getEvent <- struct{}{}:
		default:
		}
	}
	copy := *s.account
	return &copy, nil
}

type blockingUpstreamUsageHTTP struct {
	started chan struct{}
	release chan struct{}
	body    string
	once    sync.Once
	calls   atomic.Int32
}

func (s *blockingUpstreamUsageHTTP) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, accountID, concurrency, nil)
}

func (s *blockingUpstreamUsageHTTP) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	s.calls.Add(1)
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(s.body))}, nil
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

type upstreamUsageHTTPStub struct {
	mu        sync.Mutex
	requests  []*http.Request
	responses []struct {
		status int
		body   string
		err    error
	}
}

func (s *upstreamUsageHTTPStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return s.DoWithTLS(req, "", 0, 0, nil)
}

func (s *upstreamUsageHTTPStub) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req.Clone(req.Context()))
	if len(s.responses) == 0 {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	if response.err != nil {
		return nil, response.err
	}
	return &http.Response{StatusCode: response.status, Body: io.NopCloser(strings.NewReader(response.body))}, nil
}

func testUpstreamUsageConfig() *config.Config {
	return &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
		Enabled:           false,
		AllowInsecureHTTP: true,
	}}}
}

func TestEffectiveUpstreamUsageConfigDefaultsAndNormalization(t *testing.T) {
	account := &Account{Type: AccountTypeAPIKey}
	config, err := EffectiveUpstreamUsageConfig(account)
	require.NoError(t, err)
	require.Equal(t, UpstreamUsageQueryConfig{Enabled: true, Adapter: UpstreamUsageAdapterSub2API}, config)

	extra := map[string]any{UpstreamUsageQueryExtraKey: map[string]any{
		"enabled":  false,
		"adapter":  UpstreamUsageAdapterNewAPI,
		"base_url": "https://usage.example/v1",
	}}
	require.NoError(t, NormalizeUpstreamUsageExtra(extra))
	require.Equal(t, map[string]any{
		"enabled":  false,
		"adapter":  UpstreamUsageAdapterNewAPI,
		"base_url": "https://usage.example/v1",
	}, extra[UpstreamUsageQueryExtraKey])

	bad := map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"api_key": "secret"}}
	require.Error(t, NormalizeUpstreamUsageExtra(bad))

	disabledAccount := &Account{Extra: map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"enabled": false}}}
	disabled, err := EffectiveUpstreamUsageConfig(disabledAccount)
	require.NoError(t, err)
	require.False(t, disabled.Enabled)
	require.Equal(t, UpstreamUsageAdapterSub2API, disabled.Adapter)

	unknown := map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"adapter": "custom-script"}}
	require.ErrorIs(t, NormalizeUpstreamUsageExtra(unknown), ErrUpstreamUsageConfigInvalid)
	_, err = EffectiveUpstreamUsageConfig(&Account{Extra: unknown})
	require.ErrorIs(t, err, ErrUpstreamUsageUnsupported)

	unsafeURL := map[string]any{UpstreamUsageQueryExtraKey: map[string]any{
		"base_url": "https://user:secret@gateway.example/v1?token=secret",
	}}
	require.ErrorIs(t, NormalizeUpstreamUsageExtra(unsafeURL), ErrUpstreamUsageConfigInvalid)
	_, ok := normalizedUpstreamUsageConfigValue(map[string]any{"api_key": "secret"})
	require.False(t, ok)

	require.Equal(t, []UpstreamUsageAdapterOption{
		{Name: UpstreamUsageAdapterSub2API, Label: "Sub2API / TokenRouter"},
		{Name: UpstreamUsageAdapterNewAPI, Label: "New API"},
		{Name: UpstreamUsageAdapterZivv, Label: "Zivv"},
	}, UpstreamUsageAdapterOptions())
}

func TestUpstreamUsageBaseURLReusesSecurityPolicy(t *testing.T) {
	service := NewUpstreamUsageService(nil, nil, &config.Config{Security: config.SecurityConfig{
		URLAllowlist: config.URLAllowlistConfig{
			Enabled:           true,
			UpstreamHosts:     []string{"gateway.example"},
			AllowPrivateHosts: false,
		},
	}}, nil)

	value, err := service.validateBaseURL("https://gateway.example/v1/")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/v1", value)
	value, err = service.validateBaseURL("HTTPS://gateway.example/v1/")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/v1", value)
	_, err = service.validateBaseURL("http://gateway.example/v1")
	require.Error(t, err)
	_, err = service.validateBaseURL("https://127.0.0.1/v1")
	require.Error(t, err)
	_, err = service.validateBaseURL("https://unlisted.example/v1")
	require.Error(t, err)
}

func TestUpstreamUsageBaseURLUsesStrictDefaultsWithoutConfig(t *testing.T) {
	service := NewUpstreamUsageService(nil, nil, nil, nil)
	_, err := service.validateBaseURL("http://gateway.example/v1")
	require.Error(t, err)
	_, err = service.validateBaseURL("https://127.0.0.1/v1")
	require.Error(t, err)
	value, err := service.validateBaseURL("https://gateway.example/v1")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/v1", value)
}

func TestCNUpstreamUsageAdaptersPreserveConfiguredHostAndNormalizeResults(t *testing.T) {
	tests := []struct {
		name        string
		account     *Account
		response    string
		wantAdapter string
		wantPath    string
		wantQuery   string
		wantAuth    string
		wantOrg     string
		wantProject string
		assert      func(*testing.T, *UpstreamUsageQueryResult)
	}{
		{
			name: "Kimi Coding 百分比窗口",
			account: &Account{ID: 11, Platform: PlatformKimi, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
				Credentials: map[string]any{"api_key": "kimi-key", "account_mode": AccountModeCoding, "base_url": "https://relay.example/coding/v1"}},
			response:    `{"limits":[{"detail":{"limit":100,"remaining":25,"resetTime":"2026-08-24T00:00:00Z"}}],"usage":{"limit":1000,"remaining":800,"resetTime":"2026-08-30T00:00:00Z"}}`,
			wantAdapter: UpstreamUsageAdapterKimiCoding,
			wantPath:    "/coding/v1/usages",
			wantAuth:    "Bearer kimi-key",
			assert: func(t *testing.T, result *UpstreamUsageQueryResult) {
				require.Equal(t, "PERCENT", result.Unit)
				require.Len(t, result.Limits, 2)
				require.InDelta(t, 75, *result.Limits[0].Used, 1e-9)
			},
		},
		{
			name: "智谱 Coding 裸密钥",
			account: &Account{ID: 12, Platform: PlatformZhipu, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
				Credentials: map[string]any{"api_key": "zhipu-key", "account_mode": AccountModeCoding, "base_url": "https://relay.example/api/coding/paas/v4"}},
			response:    `{"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":32,"nextResetTime":1787529600000}]}}`,
			wantAdapter: UpstreamUsageAdapterZhipuCoding,
			wantPath:    "/api/monitor/usage/quota/limit",
			wantQuery:   "",
			wantAuth:    "zhipu-key",
			assert: func(t *testing.T, result *UpstreamUsageQueryResult) {
				require.Len(t, result.Limits, 1)
				require.InDelta(t, 32, *result.Limits[0].Used, 1e-9)
			},
		},
		{
			name: "智谱团队 Coding 组织项目",
			account: &Account{ID: 121, Platform: PlatformZhipu, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
				Credentials: map[string]any{"api_key": "zhipu-team-key", "account_mode": AccountModeCoding, "base_url": "https://relay.example/api/coding/paas/v4", "zhipu_organization": "org-demo", "zhipu_project": "proj-demo"}},
			response:    `{"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":12,"nextResetTime":1787529600000}]}}`,
			wantAdapter: UpstreamUsageAdapterZhipuCoding,
			wantPath:    "/api/monitor/usage/quota/limit",
			wantQuery:   "2",
			wantAuth:    "zhipu-team-key",
			wantOrg:     "org-demo",
			wantProject: "proj-demo",
			assert: func(t *testing.T, result *UpstreamUsageQueryResult) {
				require.Len(t, result.Limits, 1)
				require.InDelta(t, 12, *result.Limits[0].Used, 1e-9)
			},
		},
		{
			name: "Kimi 按量余额",
			account: &Account{ID: 13, Platform: PlatformKimi, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
				Credentials: map[string]any{"api_key": "kimi-payg", "base_url": "https://relay.example/v1"}},
			response:    `{"code":0,"data":{"available_balance":"6.25"}}`,
			wantAdapter: UpstreamUsageAdapterKimiBalance,
			wantPath:    "/v1/users/me/balance",
			wantAuth:    "Bearer kimi-payg",
			assert: func(t *testing.T, result *UpstreamUsageQueryResult) {
				require.Equal(t, "CNY", result.Unit)
				require.InDelta(t, 6.25, *result.Balance.Remaining, 1e-9)
			},
		},
		{
			name: "DeepSeek 多币种余额",
			account: &Account{ID: 14, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
				Credentials: map[string]any{"api_key": "deepseek-key", "base_url": "https://relay.example/anthropic", "api_protocol": APIProtocolAnthropic}},
			response:    `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"3.5"},{"currency":"USD","total_balance":"1.25"}]}`,
			wantAdapter: UpstreamUsageAdapterDeepseekBalance,
			wantPath:    "/user/balance",
			wantAuth:    "Bearer deepseek-key",
			assert: func(t *testing.T, result *UpstreamUsageQueryResult) {
				require.Len(t, result.Balances, 2)
				require.NotNil(t, result.Available)
				require.True(t, *result.Available)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &upstreamUsageAccountRepoStub{account: test.account}
			upstream := &upstreamUsageHTTPStub{responses: []struct {
				status int
				body   string
				err    error
			}{{status: http.StatusOK, body: test.response}}}
			service := NewUpstreamUsageService(repo, upstream, testUpstreamUsageConfig(), nil)
			result, err := service.QueryAccount(context.Background(), test.account.ID)
			require.NoError(t, err)
			require.Equal(t, test.wantAdapter, result.Adapter)
			require.Len(t, upstream.requests, 1)
			request := upstream.requests[0]
			require.Equal(t, "relay.example", request.URL.Hostname())
			require.Equal(t, test.wantPath, request.URL.Path)
			require.Equal(t, test.wantQuery, request.URL.Query().Get("type"))
			require.Equal(t, test.wantAuth, request.Header.Get("Authorization"))
			require.Equal(t, test.wantOrg, request.Header.Get("bigmodel-organization"))
			require.Equal(t, test.wantProject, request.Header.Get("bigmodel-project"))
			require.True(t, HTTPUpstreamRedirectsDisabled(request.Context()))
			test.assert(t, result)
		})
	}
}

func TestCNUpstreamUsageUnsupportedPayGDoesNotSendRequest(t *testing.T) {
	account := &Account{
		ID: 15, Platform: PlatformZhipu, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{"api_key": "zhipu-key", "account_mode": AccountModePayG},
	}
	repo := &upstreamUsageAccountRepoStub{account: account}
	upstream := &upstreamUsageHTTPStub{}
	service := NewUpstreamUsageService(repo, upstream, testUpstreamUsageConfig(), nil)
	_, err := service.QueryAccount(context.Background(), account.ID)
	require.ErrorIs(t, err, ErrUpstreamUsageUnsupported)
	require.Empty(t, upstream.requests)
}

func TestParseSub2APIUsageModes(t *testing.T) {
	balance, err := parseSub2APIUsage([]byte(`{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"payg","remaining":12.5,"balance":12.5}`))
	require.NoError(t, err)
	require.Equal(t, "balance", balance.Mode)
	require.Equal(t, 12.5, *balance.Balance.Remaining)

	quota, err := parseSub2APIUsage([]byte(`{"isValid":true,"mode":"quota_limited","status":"active","unit":"USD","remaining":75,"quota":{"limit":100,"used":25,"remaining":75,"unit":"USD"},"rate_limits":[{"window":"5h","limit":10,"used":2,"remaining":8,"window_start":null}]}`))
	require.NoError(t, err)
	require.Equal(t, "quota", quota.Mode)
	require.Len(t, quota.Limits, 1)

	unlimited, err := parseSub2APIUsage([]byte(`{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"pro","remaining":-1,"subscription":{"daily_usage_usd":0,"weekly_usage_usd":0,"monthly_usage_usd":0,"expires_at":"2030-01-01T00:00:00Z","unlimited":true}}`))
	require.NoError(t, err)
	require.True(t, unlimited.Subscription.Unlimited)
	require.Nil(t, unlimited.Subscription.Remaining)
	require.NotContains(t, string(mustJSONMarshal(t, unlimited)), `-1`)

	_, err = parseSub2APIUsage([]byte(`{"isValid":true,"mode":"quota_limited","status":"active","unit":"USD","remaining":90,"quota":{"limit":100,"used":25,"remaining":90,"unit":"USD"}}`))
	require.Error(t, err)
}

func TestParseSub2APIUsageNormalizesNegativeWalletAndRejectsMalformedWindowStart(t *testing.T) {
	balance, err := parseSub2APIUsage([]byte(`{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"wallet","remaining":-2.5,"balance":-2.5,"expires_at":"2030-01-01T00:00:00+08:00"}`))
	require.NoError(t, err)
	require.Equal(t, -2.5, *balance.Balance.Remaining)
	require.Equal(t, time.Date(2029, 12, 31, 16, 0, 0, 0, time.UTC), *balance.ExpiresAt)

	_, err = parseSub2APIUsage([]byte(`{"isValid":true,"mode":"quota_limited","status":"active","rate_limits":[{"window":"5h","limit":10,"used":2,"remaining":8,"window_start":"not-a-time"}]}`))
	require.ErrorIs(t, err, ErrUpstreamUsageInvalidResponse)
	_, err = parseSub2APIUsage([]byte(`{"isValid":true,"mode":"quota_limited","status":"active","rate_limits":[{"window":"5h","limit":10,"used":2,"remaining":8}]}`))
	require.ErrorIs(t, err, ErrUpstreamUsageInvalidResponse)
}

func TestParseSub2APIUsageDistinguishesMissingValidityFromRejectedKey(t *testing.T) {
	_, err := parseSub2APIUsage([]byte(`{"mode":"unrestricted"}`))
	require.ErrorIs(t, err, ErrUpstreamUsageInvalidResponse)

	_, err = parseSub2APIUsage([]byte(`{"isValid":false}`))
	require.ErrorIs(t, err, ErrUpstreamUsageAuthFailed)
}

func TestUpstreamUsageEndpointUsesExistingVersionedBaseURLRules(t *testing.T) {
	tests := map[string]string{
		"https://gateway.example/v1":     "https://gateway.example/v1/usage",
		"https://gateway.example/v4":     "https://gateway.example/v4/usage",
		"https://gateway.example/v1beta": "https://gateway.example/v1beta/usage",
		"https://gateway.example/api":    "https://gateway.example/api/v1/usage",
	}
	for base, want := range tests {
		got, err := upstreamUsageEndpoint(base, "/v1/usage")
		require.NoError(t, err, base)
		require.Equal(t, want, got, base)
	}

	statusTests := map[string]string{
		"https://gateway.example/v1":         "https://gateway.example/api/status",
		"https://gateway.example/subpath/v1": "https://gateway.example/subpath/api/status",
		"https://gateway.example/v4":         "https://gateway.example/api/status",
	}
	for base, want := range statusTests {
		got, err := upstreamUsageStatusEndpoint(base)
		require.NoError(t, err, base)
		require.Equal(t, want, got, base)
	}
	tokenEndpoint, err := upstreamUsageRootEndpoint("https://gateway.example/v1", "/api/usage/token")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/api/usage/token", tokenEndpoint)
	tokenEndpoint, err = upstreamUsageTokenEndpoint("https://gateway.example/v1")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/api/usage/token/", tokenEndpoint)
	walletEndpoint, err := upstreamUsageWalletEndpoint("https://gateway.example/v1")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/user/balance", walletEndpoint)
	selfEndpoint, err := upstreamUsageUserSelfEndpoint("https://gateway.example/v1")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/api/user/self", selfEndpoint)
}

func TestParseZivvUsageNormalizesWalletAndKeyQuota(t *testing.T) {
	usage, err := parseZivvUsage([]byte(`{"balance":900.490184,"currency":"USD","is_available":true,"key_limit":1000,"key_used":206.4,"plan_name":"cc b","total_used":22460.664473}`))
	require.NoError(t, err)
	require.Equal(t, UpstreamUsageAdapterZivv, usage.Provider)
	require.Equal(t, "balance", usage.Mode)
	require.Equal(t, 900.490184, *usage.Balance.Remaining)
	require.Equal(t, 22460.664473, *usage.Balance.Used)
	require.InDelta(t, 23361.154657, *usage.Balance.Total, 0.000001)
	require.Len(t, usage.Limits, 1)
	require.Equal(t, "key_quota", usage.Limits[0].Name)
	require.Equal(t, 793.6, *usage.Limits[0].Remaining)
	require.Equal(t, "cc b", usage.Subscription.PlanName)
	require.False(t, usage.Subscription.Unlimited)
	require.Equal(t, 793.6, *usage.Subscription.Remaining)

	unlimited, err := parseZivvUsage([]byte(`{"balance":1,"currency":"USD","is_available":true,"key_limit":0,"key_used":20646.4,"plan_name":"cc b","total_used":2}`))
	require.NoError(t, err)
	require.True(t, unlimited.Subscription.Unlimited)
	require.Nil(t, unlimited.Subscription.Remaining)
	require.NotContains(t, string(mustJSONMarshal(t, unlimited)), `"limit":0`)
}

func TestParseZivvUsageRejectsUnavailableOrMalformedResponses(t *testing.T) {
	_, err := parseZivvUsage([]byte(`{"balance":1,"currency":"USD","is_available":false,"key_limit":0,"key_used":0,"total_used":0}`))
	require.ErrorIs(t, err, ErrUpstreamUsageAuthFailed)
	_, err = parseZivvUsage([]byte(`{"balance":1,"currency":"EUR","is_available":true,"key_limit":0,"key_used":0,"total_used":0}`))
	require.ErrorIs(t, err, ErrUpstreamUsageInvalidResponse)
	_, err = parseZivvUsage([]byte(`{"balance":1,"currency":"USD","is_available":true,"key_limit":100,"key_used":0}`))
	require.ErrorIs(t, err, ErrUpstreamUsageInvalidResponse)
}

func TestZivvUsageQueryUsesVersionedBalanceEndpoint(t *testing.T) {
	account := &Account{
		ID: 17, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-zivv", "base_url": "https://zivv.example/"},
		Extra:       map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"adapter": UpstreamUsageAdapterZivv}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{
		{status: http.StatusOK, body: `{"balance":900.49,"currency":"USD","is_available":true,"key_limit":0,"key_used":20646.4,"plan_name":"cc b","total_used":22460.66}`},
	}}
	service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
	result, err := service.QueryAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, UpstreamUsageAdapterZivv, result.Adapter)
	require.Equal(t, 900.49, *result.Balance.Remaining)

	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "/v1/user/balance", upstream.requests[0].URL.Path)
	require.Equal(t, "Bearer sk-zivv", upstream.requests[0].Header.Get("Authorization"))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.requests[0].Context()))
}

func TestUpstreamUsageAccountBaseURLUsesPlatformNormalization(t *testing.T) {
	account := &Account{
		Platform:    PlatformAntigravity,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://gateway.example/"},
	}
	require.Equal(t, "https://gateway.example/antigravity", upstreamUsageAccountBaseURL(account))
}

func TestParseNewAPITokenUsageConvertsQuotaUnits(t *testing.T) {
	response, err := parseNewAPITokenUsage([]byte(`{"code":true,"message":"ok","data":{"object":"token_usage","name":"Default Token","total_granted":632500000,"total_used":360000,"total_available":632140000,"unlimited_quota":false,"expires_at":1893456000}}`))
	require.NoError(t, err)
	settings := newAPIUsageDisplaySettings{Unit: "USD", QuotaPerUnit: 500000}
	usage, err := normalizeNewAPITokenUsage(response, settings)
	require.NoError(t, err)
	require.Nil(t, usage.Balance, "token quota must not be exposed as wallet balance")
	require.Len(t, usage.Limits, 1)
	require.Equal(t, 1264.28, *usage.Limits[0].Remaining)
	require.Equal(t, "Default Token", usage.Subscription.PlanName)
	require.Equal(t, time.Unix(1893456000, 0).UTC(), *usage.ExpiresAt)

	// 无限量 token 的 quota 字段可能因上游整数溢出而出现负数；这些字段
	// 不参与归一化，不能因此拒绝整个查询结果。
	unlimitedResponse, err := parseNewAPITokenUsage([]byte(`{"code":true,"data":{"object":"token_usage","name":"Unlimited","total_granted":-16015,"total_used":1995297521,"total_available":-1995313536,"unlimited_quota":true,"expires_at":0}}`))
	require.NoError(t, err)
	unlimited, err := normalizeNewAPITokenUsage(unlimitedResponse, settings)
	require.NoError(t, err)
	require.True(t, unlimited.Subscription.Unlimited)
	require.Nil(t, unlimited.Balance)
	require.NotContains(t, string(mustJSONMarshal(t, unlimited)), "-1995313536")

	_, err = parseNewAPITokenUsage([]byte(`{"success":false,"message":"token not found"}`))
	require.ErrorIs(t, err, ErrUpstreamUsageAuthFailed)

	inconsistentResponse, err := parseNewAPITokenUsage([]byte(`{"code":true,"data":{"object":"token_usage","total_granted":10,"total_used":2,"total_available":2,"unlimited_quota":false,"expires_at":0}}`))
	require.NoError(t, err)
	_, err = normalizeNewAPITokenUsage(inconsistentResponse, settings)
	require.ErrorIs(t, err, ErrUpstreamUsageInvalidResponse)
}

func TestNewAPIUsageQueryUsesTokenQuotaEndpoint(t *testing.T) {
	account := &Account{
		ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-new-api", "base_url": "https://new-api.example/v1",
			"header_override_enabled": true,
			"header_overrides":        map[string]any{"x-custom": "usage-query", "authorization": "Bearer wrong-key"},
		},
		Extra: map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"adapter": UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{
		{status: http.StatusOK, body: `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`},
		{status: http.StatusOK, body: `{"code":true,"message":"ok","data":{"object":"token_usage","name":"Default Token","total_granted":632500000,"total_used":360000,"total_available":632140000,"unlimited_quota":false,"expires_at":1893456000}}`},
		{status: http.StatusOK, body: `{"balance_infos":[{"currency":"USD","total_balance":"1264.28"}]}`},
	}}
	service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
	result, err := service.QueryAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1264.28, *result.Usage.Balance.Remaining)
	require.Equal(t, 1264.28, *result.Usage.Limits[0].Remaining)
	require.Equal(t, "Default Token", result.Usage.Subscription.PlanName)
	require.Equal(t, 1264.28, *result.Usage.Subscription.Remaining)
	require.NotContains(t, string(mustJSONMarshal(t, result)), "sk-new-api")

	upstream.mu.Lock()
	require.Len(t, upstream.requests, 3)
	require.Empty(t, upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer sk-new-api", upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, "/api/status", upstream.requests[0].URL.Path)
	require.Equal(t, "/api/usage/token/", upstream.requests[1].URL.Path)
	require.Equal(t, "/user/balance", upstream.requests[2].URL.Path)
	for _, request := range upstream.requests {
		require.Equal(t, "usage-query", getHeaderRaw(request.Header, "x-custom"))
		require.True(t, HTTPUpstreamRedirectsDisabled(request.Context()))
	}
	upstream.mu.Unlock()
}

func TestNewAPIUsageEndpointMissingIsUnsupported(t *testing.T) {
	account := &Account{
		ID: 12, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{"api_key": "sk-new-api", "base_url": "https://new-api.example/v1"},
		Extra:       map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"adapter": UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{
		{status: http.StatusOK, body: `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`},
		{status: http.StatusNotFound, body: `{}`},
	}}
	service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
	_, err := service.QueryAccount(context.Background(), account.ID)
	require.ErrorIs(t, err, ErrUpstreamUsageUnsupported)
}

// DeepSeek relay 返回结构缺失或数值非法时不能伪造 CNY=0，否则会把上游协议故障
// 误显示成真实余额；合法的零余额仍应保留为成功结果。
func TestDeepSeekBalanceAdapterRejectsMalformedPayloads(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "missing balance_infos", body: `{"is_available":true}`},
		{name: "non array balance_infos", body: `{"balance_infos":{}}`},
		{name: "empty balance_infos", body: `{"balance_infos":[]}`},
		{name: "invalid total_balance", body: `{"balance_infos":[{"currency":"CNY","total_balance":"not-a-number"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{ID: 901, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
				Credentials: map[string]any{"api_key": "deepseek-key", "base_url": "https://relay.example/anthropic", "api_protocol": APIProtocolAnthropic}}
			upstream := &upstreamUsageHTTPStub{responses: []struct {
				status int
				body   string
				err    error
			}{{status: http.StatusOK, body: tc.body}}}
			svc := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
			_, err := svc.QueryAccount(context.Background(), account.ID)
			require.ErrorIs(t, err, ErrUpstreamUsageInvalidResponse)
		})
	}
}

func TestDeepSeekBalanceAdapterPreservesValidZeroBalance(t *testing.T) {
	account := &Account{ID: 902, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "deepseek-key", "base_url": "https://relay.example/anthropic", "api_protocol": APIProtocolAnthropic}}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{{status: http.StatusOK, body: `{"is_available":false,"balance_infos":[{"currency":"CNY","total_balance":"0"}]}`}}}
	svc := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)

	result, err := svc.QueryAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.NotNil(t, result.Usage)
	require.NotNil(t, result.Usage.Balance)
	require.Zero(t, *result.Usage.Balance.Remaining)
	require.False(t, *result.Usage.Available)
}

func TestNewAPIUsageContinuesWhenStatusProbeFails(t *testing.T) {
	account := &Account{
		ID: 13, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{"api_key": "sk-new-api", "base_url": "https://new-api.example/v1"},
		Extra:       map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"adapter": UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{
		{status: http.StatusServiceUnavailable, body: `{"success":false}`},
		{status: http.StatusOK, body: `{"code":true,"data":{"object":"token_usage","name":"Token","total_granted":632500000,"total_used":360000,"total_available":632140000,"unlimited_quota":false,"expires_at":0}}`},
		{status: http.StatusOK, body: `{"balance_infos":[{"currency":"USD","total_balance":"1264.28"}]}`},
	}}
	service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
	result, err := service.QueryAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1264.28, *result.Usage.Balance.Remaining)

	upstream.mu.Lock()
	require.Len(t, upstream.requests, 3)
	require.Empty(t, upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer sk-new-api", upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, "Bearer sk-new-api", upstream.requests[2].Header.Get("Authorization"))
	upstream.mu.Unlock()
}

func TestParseNewAPIWalletBalanceSupportsDisplayAndQuotaUnits(t *testing.T) {
	display, err := parseNewAPIWalletBalance([]byte(`{"balance_infos":[{"currency":"USD","total_balance":"1264.28"}]}`), newAPIUsageDisplaySettings{Unit: "USD", QuotaPerUnit: 500000})
	require.NoError(t, err)
	require.Equal(t, 1264.28, *display.Balance.Remaining)
	require.Equal(t, "USD", display.Unit)
	display, err = parseNewAPIWalletBalance([]byte(`{"balance_infos":[{"total_balance":1264.28}]}`), newAPIUsageDisplaySettings{Unit: "USD", QuotaPerUnit: 500000})
	require.NoError(t, err)
	require.Equal(t, 1264.28, *display.Balance.Remaining)

	quota, err := parseNewAPIUserSelfWallet([]byte(`{"success":true,"data":{"id":42,"quota":632140000,"used_quota":360000}}`), newAPIUsageDisplaySettings{Unit: "USD", QuotaPerUnit: 500000}, "42")
	require.NoError(t, err)
	require.Equal(t, 1264.28, *quota.Balance.Remaining)
	require.Nil(t, quota.Balance.Used)
	require.Nil(t, quota.Balance.Total)

	_, err = parseNewAPIWalletBalance([]byte(`<html>frontend</html>`), newAPIUsageDisplaySettings{Unit: "USD", QuotaPerUnit: 500000})
	require.ErrorIs(t, err, ErrUpstreamUsageInvalidResponse)
}

func TestNormalizeNewAPIQuotaUsesConfiguredCNYExchangeRate(t *testing.T) {
	settings := newAPIUsageDisplaySettings{Unit: "CNY", QuotaPerUnit: 500000, USDExchangeRate: 7.3}
	require.Equal(t, 7.3, normalizeNewAPIQuota(500000, settings))
}

func TestNewAPIUsageUsesConfiguredUserWalletToken(t *testing.T) {
	account := &Account{
		ID: 14, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-new-api", "base_url": "https://new-api.example/v1",
			NewAPIUserAccessTokenCredentialKey: "pat-secret", NewAPIUserIDCredentialKey: "42",
		},
		Extra: map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"adapter": UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{
		{status: http.StatusOK, body: `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`},
		{status: http.StatusOK, body: `{"code":true,"data":{"object":"token_usage","name":"tf","total_granted":-1,"total_used":1,"total_available":-2,"unlimited_quota":true,"expires_at":0}}`},
		{status: http.StatusOK, body: `{"success":true,"data":{"id":42,"quota":632140000,"used_quota":360000}}`},
	}}
	service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
	result, err := service.QueryAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1264.28, *result.Usage.Balance.Remaining)
	require.True(t, result.Usage.Subscription.Unlimited)

	upstream.mu.Lock()
	require.Len(t, upstream.requests, 3)
	require.Equal(t, "/api/user/self", upstream.requests[2].URL.Path)
	require.Equal(t, "Bearer pat-secret", upstream.requests[2].Header.Get("Authorization"))
	require.Equal(t, "42", upstream.requests[2].Header.Get("New-Api-User"))
	forbidden := string(mustJSONMarshal(t, result))
	require.NotContains(t, forbidden, "pat-secret")
	upstream.mu.Unlock()
}

func TestNewAPIUsageUsesWalletTokenWithoutConfiguredUserID(t *testing.T) {
	account := &Account{
		ID: 16, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-new-api", "base_url": "https://new-api.example/v1",
			NewAPIUserAccessTokenCredentialKey: "pat-secret",
		},
		Extra: map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"adapter": UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{
		{status: http.StatusOK, body: `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`},
		{status: http.StatusOK, body: `{"code":true,"data":{"object":"token_usage","name":"tf","total_granted":-27753,"total_used":2006775843,"total_available":-2006803596,"unlimited_quota":true,"expires_at":0}}`},
		{status: http.StatusOK, body: `{"success":true,"data":{"id":2,"quota":608218554,"used_quota":2006781446}}`},
	}}
	service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
	result, err := service.QueryAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.InDelta(t, 1216.437108, *result.Usage.Balance.Remaining, 0.000001)
	require.Nil(t, result.Usage.Balance.Used)
	require.Nil(t, result.Usage.Balance.Total)
	require.True(t, result.Usage.Subscription.Unlimited)

	upstream.mu.Lock()
	require.Len(t, upstream.requests, 3)
	require.Equal(t, "/api/user/self", upstream.requests[2].URL.Path)
	require.Equal(t, "Bearer pat-secret", upstream.requests[2].Header.Get("Authorization"))
	require.Empty(t, upstream.requests[2].Header.Get("New-Api-User"))
	upstream.mu.Unlock()
}

func TestNewAPIUsageRequiresWalletInsteadOfTokenQuota(t *testing.T) {
	account := &Account{
		ID: 15, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{"api_key": "sk-new-api", "base_url": "https://new-api.example/v1"},
		Extra:       map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"adapter": UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{
		{status: http.StatusOK, body: `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`},
		{status: http.StatusOK, body: `{"code":true,"data":{"object":"token_usage","name":"tf","total_granted":-1,"total_used":1,"total_available":-2,"unlimited_quota":true,"expires_at":0}}`},
		{status: http.StatusOK, body: `<html>frontend</html>`},
	}}
	service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
	_, err := service.QueryAccount(context.Background(), account.ID)
	require.ErrorIs(t, err, ErrUpstreamUsageWalletUnavailable)
}

func mustJSONMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

func TestUpstreamUsageServiceQueriesAPIKeyWithoutMutatingAccount(t *testing.T) {
	account := &Account{
		ID:          7,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://usage.example/v1"},
		Extra:       map[string]any{},
		Concurrency: 2,
	}
	original := *account
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{
		{status: http.StatusOK, body: `{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"payg","remaining":12.5,"balance":12.5}`},
	}}
	repo := &upstreamUsageAccountRepoStub{account: account}
	service := NewUpstreamUsageService(repo, upstream, testUpstreamUsageConfig(), nil)
	result, err := service.QueryAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, account.ID, result.AccountID)
	require.Equal(t, UpstreamUsageAdapterSub2API, result.Adapter)
	require.Equal(t, 12.5, *result.Usage.Balance.Remaining)
	require.Equal(t, original.Credentials, account.Credentials)
	require.Equal(t, original.Extra, account.Extra)
	require.Equal(t, int64(1), service.SnapshotMetrics().Counts[UpstreamUsageAdapterSub2API+":success"])

	upstream.mu.Lock()
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "/v1/usage", upstream.requests[0].URL.Path)
	require.Equal(t, "Bearer sk-test", upstream.requests[0].Header.Get("Authorization"))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.requests[0].Context()))
	upstream.mu.Unlock()
}

func TestUpstreamUsageServiceRejectsBedrockAndSupportsBatchErrors(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeBedrock, Credentials: map[string]any{"api_key": "x", "base_url": "https://example.com"}}
	repo := &upstreamUsageAccountRepoStub{account: account}
	service := NewUpstreamUsageService(repo, &upstreamUsageHTTPStub{}, testUpstreamUsageConfig(), nil)
	_, err := service.QueryAccount(context.Background(), 1)
	require.ErrorIs(t, err, ErrUpstreamUsageAccountInvalid)

	_, errorsByID, err := service.QueryBatch(context.Background(), []int64{1, 0, -1})
	require.NoError(t, err)
	require.ErrorIs(t, errorsByID[1], ErrUpstreamUsageAccountInvalid)
}

func TestUpstreamUsageServiceReportsMissingProxyAsRequestFailure(t *testing.T) {
	proxyID := int64(9)
	account := &Account{
		ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		ProxyID: &proxyID, Credentials: map[string]any{"api_key": "key", "base_url": "https://example.com"},
	}
	service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, &upstreamUsageHTTPStub{}, testUpstreamUsageConfig(), nil)
	_, err := service.QueryAccount(context.Background(), account.ID)
	require.ErrorIs(t, err, ErrUpstreamUsageRequestFailed)
}

func TestValidateNormalizedUsageRejectsUnknownMode(t *testing.T) {
	err := validateNormalizedUsage(&UpstreamUsageInfo{Provider: "test", Mode: "window"})
	require.Error(t, err)
	err = validateNormalizedUsage(&UpstreamUsageInfo{Provider: "test", Mode: "balance"})
	require.Error(t, err)
}

func TestUpstreamUsageServiceTimeoutError(t *testing.T) {
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{{err: context.DeadlineExceeded}}}
	account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "x", "base_url": "https://example.com"}}
	service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := service.QueryAccount(ctx, account.ID)
	require.ErrorIs(t, err, ErrUpstreamUsageTimeout)
}

func TestUpstreamUsageServiceRejectsDisabledQueryAndOversizedResponse(t *testing.T) {
	account := &Account{
		ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{"api_key": "key", "base_url": "https://example.com"},
		Extra:       map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"enabled": false}},
	}
	upstream := &upstreamUsageHTTPStub{}
	service := NewUpstreamUsageService(&upstreamUsageAccountRepoStub{account: account}, upstream, testUpstreamUsageConfig(), nil)
	_, err := service.QueryAccount(context.Background(), account.ID)
	require.ErrorIs(t, err, ErrUpstreamUsageDisabled)
	require.Empty(t, upstream.requests)

	account.Extra = nil
	upstream.responses = append(upstream.responses, struct {
		status int
		body   string
		err    error
	}{status: http.StatusOK, body: strings.Repeat("x", upstreamUsageMaxBodyBytes+1)})
	_, err = service.QueryAccount(context.Background(), account.ID)
	require.ErrorIs(t, err, ErrUpstreamUsageInvalidResponse)
}

func TestUpstreamUsageServiceSingleflightWaitersCancelIndependently(t *testing.T) {
	account := &Account{
		ID: 5, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "key", "base_url": "https://example.com"},
	}
	repo := &upstreamUsageAccountRepoStub{account: account, getEvent: make(chan struct{}, 16)}
	upstream := &blockingUpstreamUsageHTTP{
		started: make(chan struct{}),
		release: make(chan struct{}),
		body:    `{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"payg","remaining":3,"balance":3}`,
	}
	service := NewUpstreamUsageService(repo, upstream, testUpstreamUsageConfig(), nil)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := service.QueryAccount(firstCtx, account.ID)
		firstErr <- err
	}()
	select {
	case <-upstream.started:
	case <-time.After(time.Second):
		t.Fatal("shared query did not start")
	}
	for len(repo.getEvent) > 0 {
		<-repo.getEvent
	}

	secondResult := make(chan *UpstreamUsageQueryResult, 1)
	secondErr := make(chan error, 1)
	go func() {
		result, err := service.QueryAccount(context.Background(), account.ID)
		secondResult <- result
		secondErr <- err
	}()
	select {
	case <-repo.getEvent:
	case <-time.After(time.Second):
		t.Fatal("second waiter did not load its identity snapshot")
	}
	cancelFirst()
	require.ErrorIs(t, <-firstErr, context.Canceled)
	// 等待方完成预读后给它一次调度机会进入 singleflight。
	time.Sleep(10 * time.Millisecond)
	close(upstream.release)
	require.NoError(t, <-secondErr)
	require.NotNil(t, <-secondResult)
	require.Equal(t, int32(1), upstream.calls.Load())
}

func TestUpstreamUsageServiceRejectsIdentityChangeAfterQuery(t *testing.T) {
	account := &Account{
		ID: 6, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "key", "base_url": "https://example.com"},
	}
	repo := &upstreamUsageAccountRepoStub{account: account}
	upstream := &blockingUpstreamUsageHTTP{
		started: make(chan struct{}),
		release: make(chan struct{}),
		body:    `{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"payg","remaining":3,"balance":3}`,
	}
	service := NewUpstreamUsageService(repo, upstream, testUpstreamUsageConfig(), nil)
	resultErr := make(chan error, 1)
	go func() {
		_, err := service.QueryAccount(context.Background(), account.ID)
		resultErr <- err
	}()
	select {
	case <-upstream.started:
	case <-time.After(time.Second):
		t.Fatal("query did not start")
	}
	repo.mu.Lock()
	repo.account.Status = "inactive"
	repo.mu.Unlock()
	close(upstream.release)
	require.ErrorIs(t, <-resultErr, ErrUpstreamUsageIdentityChanged)
}

func TestUpstreamUsageServiceTreatsDeletionDuringQueryAsIdentityChange(t *testing.T) {
	account := &Account{
		ID: 8, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "key", "base_url": "https://example.com"},
	}
	repo := &upstreamUsageAccountRepoStub{account: account}
	upstream := &blockingUpstreamUsageHTTP{
		started: make(chan struct{}),
		release: make(chan struct{}),
		body:    `{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"payg","remaining":3,"balance":3}`,
	}
	service := NewUpstreamUsageService(repo, upstream, testUpstreamUsageConfig(), nil)
	resultErr := make(chan error, 1)
	go func() {
		_, err := service.QueryAccount(context.Background(), account.ID)
		resultErr <- err
	}()
	select {
	case <-upstream.started:
	case <-time.After(time.Second):
		t.Fatal("query did not start")
	}
	repo.mu.Lock()
	repo.account = nil
	repo.mu.Unlock()
	close(upstream.release)
	require.ErrorIs(t, <-resultErr, ErrUpstreamUsageIdentityChanged)
}
