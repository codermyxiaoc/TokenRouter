package service

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type anthropicWindowHTTP struct {
	HTTPUpstream
	t              *testing.T
	registryCalls  int
	models         string
	registryStatus int
}

// 检查实际出站请求，无凭据的公共目录与带凭据的账号模型同步分开。
func (u *anthropicWindowHTTP) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	status, body := 200, u.models
	require.True(u.t, HTTPUpstreamRedirectsDisabled(req.Context()))
	if req.URL.Host == "models.dev" {
		u.registryCalls++
		require.Empty(u.t, getHeaderRaw(req.Header, "x-api-key"))
		require.Empty(u.t, getHeaderRaw(req.Header, "Authorization"))
		require.Empty(u.t, getHeaderRaw(req.Header, "anthropic-version"))
		require.Equal(u.t, "/api.json", req.URL.Path)
		body = `{"anthropic":{"models":{"registry":{"limit":{"context":1000000}},"short":{"limit":{"context":1000000}},"not-listed":{"limit":{"context":1000000}}}}}`
		if u.registryStatus != 0 {
			status = u.registryStatus
		}
	} else {
		require.Equal(u.t, "/v1/models", req.URL.Path)
		require.Equal(u.t, "key", getHeaderRaw(req.Header, "x-api-key"))
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestAnthropicWindowCatalogSync(t *testing.T) {
	httpClient := &anthropicWindowHTTP{t: t, models: `{"data":[{"id":"registry"},{"id":"short","max_input_tokens":200000},{"id":"native","context_window":900000},{"id":"max-only","max_context_window":1000000}]}`}
	repo := &upstreamModelMetadataRepoStub{}
	svc := &AccountTestService{cfg: upstreamModelSyncTestConfig(), httpUpstream: httpClient, accountRepo: repo}
	a := &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformAnthropic, Credentials: map[string]any{"api_key": "key"}}
	for i := 0; i < 2; i++ {
		catalog, err := svc.SyncUpstreamModelCatalog(context.Background(), a)
		require.NoError(t, err)
		require.Equal(t, int64(1000000), catalog.Metadata["registry"].ContextWindow)
		require.Equal(t, int64(200000), catalog.Metadata["short"].ContextWindow)
		require.Equal(t, int64(900000), catalog.Metadata["native"].ContextWindow)
		require.NotContains(t, catalog.Metadata, "max-only")
		require.NotContains(t, catalog.Metadata, "not-listed")
		require.Equal(t, anthropicCatalogBinding(a), a.GetUpstreamModelMetadataSnapshot().Binding)
	}
	require.Equal(t, 1, httpClient.registryCalls)
	require.Contains(t, repo.updates, UpstreamModelMetadataExtraKey)
	a.Credentials["base_url"] = "https://proxy.example"
	require.Empty(t, longContextSelection((&GatewayService{}).anthropicWindowAccounts([]Account{*a}, PlatformAnthropic, nil), nil, "native"))
	catalog, err := svc.SyncUpstreamModelCatalog(context.Background(), a)
	require.NoError(t, err)
	require.NotContains(t, catalog.Metadata, "registry")
	require.Equal(t, 1, httpClient.registryCalls)
	// 新窗口报告替换旧目录；公共站点失败也能继续使用直接报告的型号。
	a.Credentials["base_url"] = "https://api.anthropic.com"
	httpClient.registryStatus = 503
	httpClient.models = `{"data":[{"id":"native","max_input_tokens":300000},{"id":"other"}]}`
	svc = &AccountTestService{cfg: upstreamModelSyncTestConfig(), httpUpstream: httpClient, accountRepo: repo}
	catalog, err = svc.SyncUpstreamModelCatalog(context.Background(), a)
	require.NoError(t, err)
	require.NotEmpty(t, catalog.Warnings)
	require.NotContains(t, a.GetUpstreamModelMetadataSnapshot().Models, "registry")
	require.Equal(t, int64(300000), catalog.Metadata["native"].ContextWindow)
}

// 每次从已发布的长窗口快照开始刷新，验证管理同步只在完整目录就绪后更新存储和选号资格。
func TestAnthropicWindowCatalogRefreshPages(t *testing.T) {
	type page struct {
		after  string
		status int
		body   string
	}
	const (
		oldCatalog = `{"data":[{"id":"old","context_window":1000000}]}`
		firstPage  = `{"data":[{"id":"short","context_window":200000},{"id":"cursor/+1","context_window":100000}],"has_more":true,"last_id":"cursor/+1"}`
		readLimit  = 512
	)
	for _, tc := range []struct {
		name    string
		pages   []page
		windows map[string]int64 // nil 表示同步应失败，保留旧快照。
	}{
		{"empty-data", []page{{"", 200, `{"data":[],"has_more":false,"last_id":null}`}}, map[string]int64{}},
		{"empty-models", []page{{"", 200, `{"models":[]}`}}, map[string]int64{}},
		{"empty-array", []page{{"", 200, `[]`}}, map[string]int64{}},
		{"multiple-pages", []page{
			{"", 200, firstPage},
			{"cursor/+1", 200, `{"data":[{"id":"short","context_window":1000000},{"id":"long","max_input_tokens":1000000}],"has_more":false}`},
		}, map[string]int64{"short": 200000, "cursor/+1": 100000, "long": 1000000}},
		{"models-envelope", []page{{"", 200, `{"models":[{"slug":"legacy","limit":{"context":1000000}}],"last_id":15}`}}, map[string]int64{"legacy": 1000000}},
		{"bare-array", []page{{"", 200, `[{"name":"models/native","context_window":900000}]`}}, map[string]int64{"native": 900000}},
		{"later-http-error", []page{{"", 200, firstPage}, {"cursor/+1", 503, `{"error":"unavailable"}`}}, nil},
		{"later-invalid-envelope", []page{{"", 200, firstPage}, {"cursor/+1", 200, `{"unexpected":[]}`}}, nil},
		{"combined-size-exceeds-limit", []page{
			{"", 200, strings.Repeat(" ", readLimit/2) + firstPage},
			{"cursor/+1", 200, strings.Repeat(" ", readLimit/2) + `{"data":[]}`},
		}, nil},
		{"missing-id", []page{{"", 200, `{"data":[{}]}`}}, nil},
		{"invalid-has-more", []page{{"", 200, `{"data":[{"id":"partial"}],"has_more":"invalid"}`}}, nil},
		{"missing-cursor", []page{{"", 200, `{"data":[{"id":"partial"}],"has_more":true}`}}, nil},
		{"repeated-cursor", []page{{"", 200, firstPage}, {"cursor/+1", 200, firstPage}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{
				ID: 19, Type: AccountTypeAPIKey, Platform: PlatformAnthropic,
				Credentials: map[string]any{"api_key": "key", "base_url": "https://relay.example"},
			}
			repo := &upstreamModelMetadataRepoStub{}
			// 先走实际同步入口建立旧资格，后续响应逐页返回。
			upstream := &queuedHTTPUpstreamStub{}
			pages := append([]page{{"", 200, oldCatalog}}, tc.pages...)
			for _, p := range pages {
				upstream.responses = append(upstream.responses, &http.Response{
					StatusCode: p.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(p.body)),
				})
			}
			upstream.onCall = func(req *http.Request, stub *queuedHTTPUpstreamStub) {
				require.LessOrEqual(t, stub.callCount, len(pages), "游标必须前进，终页不能继续翻页")
				require.Equal(t, pages[stub.callCount-1].after, req.URL.Query().Get("after_id"))
				require.Equal(t, "relay.example", req.URL.Host)
				require.Equal(t, "/v1/models", req.URL.Path)
				require.Equal(t, "key", getHeaderRaw(req.Header, "x-api-key"))
				require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
				require.Nil(t, repo.updates, "尚有页面未完成时不能发布快照")
			}
			cfg := upstreamModelSyncTestConfig()
			cfg.Gateway.ModelsListReadMaxBytes = readLimit
			svc := &AccountTestService{cfg: cfg, httpUpstream: upstream, accountRepo: repo}
			_, err := svc.SyncUpstreamModelCatalog(t.Context(), account)
			require.NoError(t, err)
			old := account.GetUpstreamModelMetadataSnapshot()
			require.Equal(t, int64(1000000), old.Models["old"].ContextWindow)
			repo.updates = nil

			catalog, err := svc.SyncUpstreamModelCatalog(t.Context(), account)
			require.Equal(t, len(pages), upstream.callCount)
			if tc.windows == nil {
				require.Error(t, err)
				require.Nil(t, repo.updates)
				require.Equal(t, old, account.GetUpstreamModelMetadataSnapshot())
				return
			}
			require.NoError(t, err)
			require.NotNil(t, catalog.Models)
			require.Len(t, catalog.Models, len(tc.windows))
			for id, window := range tc.windows {
				require.Contains(t, catalog.Models, id)
				require.Equal(t, window, catalog.Metadata[id].ContextWindow)
			}
			stored := (&Account{Extra: repo.updates}).GetUpstreamModelMetadataSnapshot()
			require.NotNil(t, stored)
			require.NotEmpty(t, stored.Binding)
			require.Equal(t, catalog.Metadata, stored.Models)
			require.Equal(t, stored, account.GetUpstreamModelMetadataSnapshot())
			scopes := (&GatewayService{}).anthropicWindowAccounts([]Account{*account}, PlatformAnthropic, nil)
			selection := longContextSelection(scopes, nil, "old")
			require.NotNil(t, selection, "同步后的空快照必须有别于从未同步")
			require.Empty(t, selection, "旧型号必须退出可选账号集合")
		})
	}
}

// 账号预览同样取完全部页面，只返回型号。即使未提供窗口，也不会查询公共注册表或保存快照。
func TestAnthropicModelPreviewReadsAllPagesWithoutWriting(t *testing.T) {
	upstream := &queuedHTTPUpstreamStub{}
	for _, body := range []string{
		`{"data":[{"id":"first"}],"has_more":true,"last_id":"first"}`,
		`{"data":[{"id":"second"}],"has_more":false}`,
	} {
		upstream.responses = append(upstream.responses, &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)),
		})
	}
	repo := &upstreamModelMetadataRepoStub{}
	account := &Account{ID: 19, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key"}}
	svc := &AccountTestService{cfg: upstreamModelSyncTestConfig(), httpUpstream: upstream, accountRepo: repo}
	models, err := svc.FetchUpstreamSupportedModels(t.Context(), account)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, models)
	require.Equal(t, 2, upstream.callCount)
	require.Nil(t, repo.updates)
	require.Nil(t, account.GetUpstreamModelMetadataSnapshot())
}

// 真实转发、列表、广场及购买预览均走相同的分组/渠道/账号别名。
func TestAnthropicWindowCatalogAndRouting(t *testing.T) {
	group := Group{ID: 21, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
	account := func(id int64, window int64) Account {
		a := Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{group.ID}, AccountGroups: []AccountGroup{{GroupID: group.ID}}, Credentials: map[string]any{"api_key": "key", "model_mapping": map[string]any{"route": "native"}, "model_whitelist": []any{"native"}}}
		if window > 0 {
			a.Extra = map[string]any{UpstreamModelMetadataExtraKey: UpstreamModelMetadataSnapshot{Source: "upstream", Binding: anthropicCatalogBinding(&a), Models: map[string]UpstreamModelMetadata{"native": {ContextWindow: window}, "hidden": {ContextWindow: 1000000}}}}
		}
		return a
	}
	accounts := []Account{account(7, 899999), account(8, 900000), account(9, 0)}
	listRepo := &modelsListAccountRepoStub{all: accounts, byGroup: map[int64][]Account{group.ID: accounts}}
	svc := &GatewayService{accountRepo: listRepo, channelService: newRequestableModelsChannelService(group.ID, group.Platform, Channel{ID: 51, Status: StatusActive, ModelMapping: map[string]map[string]string{PlatformAnthropic: {"client": "route"}}})}
	ctx := context.WithValue(context.Background(), ctxkey.Group, &group)
	result := svc.ResolveRequestableModels(ctx, &group.ID, group.Platform)
	variant, ok := requestableModelByID(result.Models, "client[1m]")
	require.True(t, ok, "%v", result.Models)
	require.NotEqual(t, "client[1m]", variant.PricingModel)
	require.NotContains(t, RequestableModelIDs(result.Models), "hidden[1m]")
	market := &ModelMarketplaceService{gatewayService: svc}
	preview, err := market.ListGroupModelIDs(ctx, []Group{group})
	require.NoError(t, err)
	require.Contains(t, preview[group.ID], "client[1m]")
	require.Contains(t, market.resolveGroupModelsWithAccounts(ctx, &group, accounts), marketplaceModelDef{ID: "client[1m]", DisplayName: "client[1m]", PricingModel: variant.PricingModel})
	repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{group.ID: accounts}}
	svc.accountRepo = repo
	smart := NewSmartRoutingService(svc, nil)
	models, err := smart.ResolveSmartRoutingModels(ctx, &group)
	require.NoError(t, err)
	require.Contains(t, models, "client[1m]")
	available, err := smart.EvaluateSmartRoutingGroup(ctx, &group, "client[1m]", "/v1/messages")
	require.NoError(t, err)
	require.True(t, available.Schedulable)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"client[1M]","messages":[]}`)), PlatformAnthropic)
	require.NoError(t, err)
	require.True(t, parsed.ClaudeLongContext)
	require.Equal(t, "client", parsed.Model)
	selection, err := svc.WithAnthropicWindowSelection(ctx, &group, parsed)
	require.NoError(t, err)
	for _, a := range accounts {
		a.Credentials = map[string]any{"model_mapping": a.Credentials["model_mapping"], "model_whitelist": a.Credentials["model_whitelist"]}
		a.Extra = nil
		require.Equal(t, a.ID == 8, svc.isModelSupportedByAccountWithContext(selection, &a, parsed.Model))
	}
	// 预先粘在短窗口账号，实际选号仍改用长窗口账号。普通请求继续沿用粘性。
	svc.cache = &stickyGatewayCacheHotpathStub{stickyID: 7}
	for _, tc := range []struct {
		ctx  context.Context
		want int64
	}{{selection, 8}, {ctx, 7}} {
		got, err := svc.SelectAccountForModelWithExclusions(context.WithValue(tc.ctx, ctxkey.ForcePlatform, PlatformAnthropic), &group.ID, "session", parsed.Model, nil)
		require.NoError(t, err)
		require.Equal(t, tc.want, got.ID)
	}
	// 相同的上游型号与请求头走原生协议；窗口已经包含在常规能力，不注入额外 beta。
	svc.cfg = upstreamModelSyncTestConfig()
	req, _, err := svc.buildUpstreamRequest(selection, gatewayAnthropicBaseURLContext(t), &accounts[1], []byte(`{"model":"native","messages":[]}`), "key", "apikey", "native", false, false)
	require.NoError(t, err)
	require.NotContains(t, getHeaderRaw(req.Header, "anthropic-beta"), "context-1m")
	group.ModelRoutingEnabled = true
	group.ModelRouting = map[string][]int64{"route": {7}}
	svc.accountRepo = listRepo
	require.NotContains(t, RequestableModelIDs(svc.ResolveRequestableModels(ctx, &group.ID, group.Platform).Models), "client[1m]")
}

func TestAnthropicWindowOAuthBinding(t *testing.T) {
	a := Account{Type: AccountTypeOAuth, Platform: PlatformAnthropic, Credentials: map[string]any{"refresh_token": "first"}}
	first := anthropicCatalogBinding(&a)
	a.Credentials["refresh_token"] = "second"
	require.NotEqual(t, first, anthropicCatalogBinding(&a))
	a.Extra = map[string]any{"account_uuid": "stable", "org_uuid": "org"}
	stable := anthropicCatalogBinding(&a)
	a.Credentials["refresh_token"] = "third"
	require.Equal(t, stable, anthropicCatalogBinding(&a))
	a.Extra["account_uuid"] = "other"
	require.NotEqual(t, stable, anthropicCatalogBinding(&a))
}

// 已同步过的空目录、已删除型号和失效身份都不能使旧 [1m] 自动降级到普通账号。
func TestAnthropicWindowStaleSelection(t *testing.T) {
	group := Group{ID: 25, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
	for _, tc := range []struct {
		name    string
		entries map[string]UpstreamModelMetadata
		binding string
		legacy  bool
	}{
		{"empty", map[string]UpstreamModelMetadata{}, "valid", false},
		{"removed", map[string]UpstreamModelMetadata{"old": {ContextWindow: 1000000}}, "valid", false},
		{"short-with-explicit-suffix", map[string]UpstreamModelMetadata{"short": {ContextWindow: 899999}}, "valid", false},
		{"rebound", map[string]UpstreamModelMetadata{"short": {ContextWindow: 1000000}}, "previous", false},
		{"cleared-by-reauthorization", map[string]UpstreamModelMetadata{}, "", false},
		{"never-synced", nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Account{ID: 93, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{group.ID}, Credentials: map[string]any{"api_key": "key", "model_mapping": map[string]any{"short": "short", "short[1m]": "short"}}}
			if !tc.legacy {
				binding := tc.binding
				if binding == "valid" {
					binding = anthropicCatalogBinding(&a)
				}
				a.Extra = map[string]any{UpstreamModelMetadataExtraKey: UpstreamModelMetadataSnapshot{Source: "upstream", Binding: binding, Models: tc.entries}}
			}
			accounts := []Account{a}
			ctx := context.WithValue(context.Background(), ctxkey.Group, &group)
			byGroup := map[int64][]Account{group.ID: accounts}
			svc := &GatewayService{accountRepo: &modelsListAccountRepoStub{all: accounts, byGroup: byGroup}}
			preview, err := (&ModelMarketplaceService{gatewayService: svc}).ListGroupModelIDs(ctx, []Group{group})
			require.NoError(t, err)
			require.Equal(t, tc.legacy, slices.Contains(preview[group.ID], "short[1m]"))
			svc.accountRepo = &smartRoutingAccountRepo{accounts: byGroup}
			smart := NewSmartRoutingService(svc, nil)
			list, err := smart.ResolveSmartRoutingModels(ctx, &group)
			require.NoError(t, err)
			require.Equal(t, tc.legacy, slices.Contains(list, "short[1m]"))
			availability, err := smart.EvaluateSmartRoutingGroup(ctx, &group, "short[1m]", "/v1/messages")
			require.NoError(t, err)
			require.Equal(t, tc.legacy, availability.Schedulable)
			selected, err := svc.WithAnthropicWindowSelection(ctx, &group, &ParsedRequest{Model: "short", ClaudeLongContext: true})
			require.NoError(t, err)
			require.Equal(t, tc.legacy, svc.isModelSupportedByAccountWithContext(selected, &a, "short"))
			require.True(t, svc.isModelSupportedByAccountWithContext(ctx, &a, "short"))
		})
	}
}

// 后备组必须按自身的上游能力重新选号，未绑定的标准模式也不能引用其它组的能力。
func TestAnthropicWindowGroupBoundaries(t *testing.T) {
	first := Group{ID: 31, Platform: PlatformAnthropic}
	second := Group{ID: 32, Platform: PlatformAnthropic}
	a := Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key"}, GroupIDs: []int64{first.ID}}
	a.Extra = map[string]any{UpstreamModelMetadataExtraKey: UpstreamModelMetadataSnapshot{Source: "upstream", Binding: anthropicCatalogBinding(&a), Models: map[string]UpstreamModelMetadata{"model": {ContextWindow: 1000000}}}}
	b := a
	b.ID = 4
	b.GroupIDs = []int64{second.ID}
	b.Extra = map[string]any{UpstreamModelMetadataExtraKey: UpstreamModelMetadataSnapshot{Source: "upstream"}}
	repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{first.ID: {a}, second.ID: {b}}}
	svc := &GatewayService{accountRepo: repo}
	ctx, err := svc.WithAnthropicWindowSelection(context.Background(), &first, &ParsedRequest{Model: "model", ClaudeLongContext: true})
	require.NoError(t, err)
	require.True(t, svc.isRoutingModelSupportedByAccountWithContext(ctx, &a, "model"))
	ctx, err = svc.refreshAnthropicWindowGroup(ctx, &second, "model")
	require.NoError(t, err)
	require.False(t, svc.isRoutingModelSupportedByAccountWithContext(ctx, &b, "model"))
	require.False(t, svc.isRoutingModelSupportedByAccountWithContext(ctx, &a, "model"))
	require.Empty(t, svc.anthropicWindowAccounts([]Account{a, b}, PlatformAnthropic, nil))
}
