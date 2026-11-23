//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 将公开 Jev 映射到私有部署，仅顶层模型被改写，大小写、路径和响应元数据都可追溯。
func TestTypeSafeForwardMappedSystemOneAndPublicCatalog(t *testing.T) {
	const target = "opencode/Decision-Preview"
	const response = `{"model":"vendor-2026","answers":{"q":{"noul":0}},"usage":{"input_tokens":42,"output_tokens":3}}`
	body := []byte(`{"model":"jev-latest","state":{"model":"jev-latest"},"questions":{"q":{"type":"noul","instructions":"ask jev-latest"}}}`)
	original := string(body)
	account := Account{
		ID: 9, Platform: PlatformTypeSafe, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-key", "base_url": "https://relay.example",
			"model_mapping":   map[string]any{DefaultTypeSafeModel: target},
			"model_whitelist": []any{target},
		},
	}
	upstream := &httpUpstreamRecorder{resp: newJSONResponse(http.StatusOK, response)}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, out := newTestContext()
	ctx := WithAPIKeyModelRedirectTrace(context.Background(), NewAPIKeyModelRedirectTrace("public-alias", "public-alias", DefaultTypeSafeModel))
	result, err := svc.ForwardOpenCodeSystemOne(ctx, c, &account, body)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"opencode/Decision-Preview","state":{"model":"jev-latest"},"questions":{"q":{"type":"noul","instructions":"ask jev-latest"}}}`, string(upstream.lastBody))
	require.Equal(t, original, string(body))
	require.Equal(t, DefaultTypeSafeModel, result.BillingModel)
	require.Equal(t, DefaultTypeSafeModel, result.Model)
	require.Equal(t, target, result.UpstreamModel)
	require.Equal(t, target, c.GetString(OpsUpstreamModelKey))
	require.Equal(t, response, out.Body.String())
	trace, _ := APIKeyModelRedirectTraceFromContext(ctx)
	require.Contains(t, trace.ResponseModels(), target)
	require.True(t, account.IsModelSupported(DefaultTypeSafeModel))
	require.True(t, account.IsModelSupported(target), "白名单中的具体部署名已显式允许直调")
	require.False(t, account.IsModelSupported("jev-1.13-free"))
	groupID := int64(4)
	catalog := &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {account}}}}
	available := catalog.ResolveRequestableModels(ctx, &groupID, PlatformTypeSafe)
	require.ElementsMatch(t, []RequestableModel{{ID: DefaultTypeSafeModel, PricingModel: DefaultTypeSafeModel}, {ID: target, PricingModel: target}}, available.Models)
	delete(account.Credentials, "model_whitelist")
	catalog.accountRepo = &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {account}}}
	available = catalog.ResolveRequestableModels(ctx, &groupID, PlatformTypeSafe)
	require.Equal(t, []RequestableModel{{ID: DefaultTypeSafeModel, PricingModel: DefaultTypeSafeModel}}, available.Models, "只有映射值没有独立声明时不发布部署名")
	account.Credentials["model_whitelist"] = []any{"different-target"}
	require.False(t, account.IsModelSupported(DefaultTypeSafeModel))
	_, err = svc.ForwardOpenCodeSystemOne(ctx, c, &account, body)
	require.Error(t, err)
}

// Key 别名经过渠道中间名与账号映射后仍展示公开 SKU，账号权限定在最终目标上。
func TestTypeSafeSystemOneChannelAndKeyMapping(t *testing.T) {
	const intermediate, target = "channel-decision", "relay/Decision-A"
	groupID := int64(5)
	account := Account{
		ID: 10, Platform: PlatformTypeSafe, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-key", "base_url": "https://relay.example",
			"model_mapping":   map[string]any{intermediate: target},
			"model_whitelist": []any{target},
		},
	}
	channel := Channel{
		ID: 42, Status: StatusActive, BillingModelSource: BillingModelSourceUpstream,
		ModelMapping: map[string]map[string]string{PlatformTypeSafe: {DefaultTypeSafeModel: intermediate}},
	}
	channels := newRequestableModelsChannelService(groupID, PlatformTypeSafe, channel)
	ctx := WithAPIKeyModelRedirectTrace(context.Background(), NewAPIKeyModelRedirectTrace("my-alias", "my-alias", DefaultTypeSafeModel))
	gateway := &GatewayService{
		channelService: channels,
		accountRepo:    &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {account}}},
	}
	available := gateway.ResolveRequestableModels(ctx, &groupID, PlatformTypeSafe)
	require.Contains(t, available.Models, RequestableModel{ID: DefaultTypeSafeModel, PricingModel: DefaultTypeSafeModel})
	forward := &OpenAIGatewayService{channelService: channels, cfg: &config.Config{}}
	mapping, _ := forward.ResolveChannelMappingAndRestrict(ctx, &groupID, DefaultTypeSafeModel)
	require.Equal(t, intermediate, mapping.MappedModel)
	require.True(t, account.IsModelSupported(mapping.MappedModel))
	upstream := &httpUpstreamRecorder{resp: newJSONResponse(200, `{"answers":{},"usage":{"input_tokens":1,"output_tokens":0}}`)}
	forward.httpUpstream = upstream
	body := []byte(`{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul"}}}`)
	c, _ := newTestContext()
	result, err := forward.ForwardOpenCodeSystemOne(ctx, c, &account, body, mapping.MappedModel)
	require.NoError(t, err)
	require.Equal(t, DefaultTypeSafeModel, result.Model)
	require.Equal(t, DefaultTypeSafeModel, result.BillingModel)
	require.Equal(t, target, gjson.GetBytes(upstream.lastBody, "model").String())
	fields := mapping.ToUsageFields(DefaultTypeSafeModel, result.UpstreamModel)
	require.Equal(t, buildModelMappingChain("my-alias", DefaultTypeSafeModel, intermediate, target), fields.ModelMappingChain)
	require.Empty(t, requestableModelsFallback([]string{target}, PlatformTypeSafe).Models)
}

// 即使请求别名、渠道名或实际投递名落在别的价目上，TypeSafe 均使用公开 Jev 价格。
func TestTypeSafeSystemOneRecordUsageAlwaysBillsPublicModel(t *testing.T) {
	for _, source := range []string{BillingModelSourceRequested, BillingModelSourceChannelMapped, BillingModelSourceUpstream} {
		t.Run(source, func(t *testing.T) {
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			groupID := int64(42)
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: "typesafe-" + source,
					Model:     DefaultTypeSafeModel, BillingModel: DefaultTypeSafeModel,
					UpstreamModel: "jev-1.13-free", UpstreamEndpoint: "/v1/systemone",
					Usage: OpenAIUsage{InputTokens: 1000000, OutputTokens: 10},
				},
				Account:            &Account{ID: 6, Platform: PlatformTypeSafe, Type: AccountTypeAPIKey},
				APIKey:             &APIKey{ID: 4, GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformTypeSafe, RateMultiplier: 1}},
				User:               &User{ID: 5},
				ChannelUsageFields: ChannelUsageFields{OriginalModel: "user-alias", ChannelMappedModel: "channel-name", BillingModelSource: source},
			})
			require.NoError(t, err)
			require.NotNil(t, repo.lastLog)
			require.InDelta(t, 0.042, repo.lastLog.InputCost, 1e-9)
			require.Zero(t, repo.lastLog.OutputCost)
			require.Equal(t, "jev-1.13-free", *repo.lastLog.UpstreamModel)
			require.Equal(t, "user-alias", repo.lastLog.RequestedModel)
		})
	}
}

// 新公开型号按自己的渠道价结算。缺价记录原始用量和零成本，不能借用私有投递名或 Jev 默认价格。
func TestTypeSafeConfiguredPublicPricing(t *testing.T) {
	const priced = "jev-1.14"
	const unpriced = "test-unpriced-decision"
	const input, output = 1_000_000, 500_000
	inPrice, outPrice := 2.0/1_000_000, 6.0/1_000_000
	group := &Group{ID: 42, Platform: PlatformTypeSafe, RateMultiplier: 1}
	channel := Channel{ID: 501, Status: StatusActive, ModelPricing: []ChannelModelPricing{
		{Platform: PlatformTypeSafe, Models: []string{priced}, BillingMode: BillingModeToken, InputPrice: &inPrice, OutputPrice: &outPrice},
	}}
	for _, source := range []string{BillingModelSourceRequested, BillingModelSourceChannelMapped, BillingModelSourceUpstream} {
		for _, tc := range []struct {
			public, actual          string
			expectedIn, expectedOut float64
		}{
			{priced, "jev-1.13-free", 2, 3},
			{unpriced, DefaultTypeSafeModel, 0, 0},
		} {
			t.Run(source+"/"+tc.public, func(t *testing.T) {
				repo := &openAIRecordUsageLogRepoStub{inserted: true}
				svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				svc.channelService = newRequestableModelsChannelService(group.ID, group.Platform, channel)
				svc.resolver = NewModelPricingResolver(svc.channelService, svc.billingService)
				err := svc.RecordUsage(t.Context(), &OpenAIRecordUsageInput{
					Result: &OpenAIForwardResult{RequestID: "typesafe-configured-" + source + tc.public, Model: tc.public, BillingModel: tc.public, UpstreamModel: tc.actual, UpstreamEndpoint: "/v1/systemone", Usage: OpenAIUsage{InputTokens: input, OutputTokens: output}},
					APIKey: &APIKey{ID: 4, GroupID: &group.ID, Group: group}, User: &User{ID: 5}, Account: &Account{ID: 6, Platform: PlatformTypeSafe, Type: AccountTypeAPIKey},
					ChannelUsageFields: ChannelUsageFields{OriginalModel: "client-alias", ChannelMappedModel: "channel-alias", BillingModelSource: source},
				})
				require.NoError(t, err)
				require.NotNil(t, repo.lastLog)
				require.Equal(t, tc.public, repo.lastLog.Model)
				require.Equal(t, "client-alias", repo.lastLog.RequestedModel)
				require.Equal(t, tc.actual, *repo.lastLog.UpstreamModel)
				require.InDelta(t, tc.expectedIn, repo.lastLog.InputCost, 1e-9)
				require.InDelta(t, tc.expectedOut, repo.lastLog.OutputCost, 1e-9)
				require.InDelta(t, tc.expectedIn+tc.expectedOut, repo.lastLog.ActualCost, 1e-9)
			})
		}
	}
}

// 手动自动选协议和显式 System One 均通过账号映射发送真实部署名，事件回报同一型号。
func TestTypeSafeMappedSystemOneAccountProbe(t *testing.T) {
	for _, endpoint := range []string{"auto", APIProtocolSystemOne} {
		t.Run(endpoint, func(t *testing.T) {
			account := &Account{ID: 19, Platform: PlatformTypeSafe, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "test-key", "base_url": "https://relay.example/custom", "model_mapping": map[string]any{DefaultTypeSafeModel: "Private-Model"},
			}}
			svc, upstream := adaptiveCNAccountTestService(account, newJSONResponse(http.StatusOK, `{"answers":{"test":{}}}`))
			svc.openAIGatewayService = &OpenAIGatewayService{cfg: svc.cfg}
			c, out := newTestContext()
			require.NoError(t, svc.TestAccountConnectionWithOptions(c, account.ID, "", "custom state", AccountTestTypeText, AccountTestModeDefault, endpoint, AccountTestOptions{}))
			require.Equal(t, "https://relay.example/custom/v1/systemone", upstream.lastReq.URL.String())
			require.Equal(t, "Private-Model", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Contains(t, out.Body.String(), `"model":"Private-Model"`)
		})
	}
}

// 管理员仍可探测诊断配置；用户直调白名单名称时，中间映射的最终目标也须通过白名单。
func TestTypeSafeAdminProbeNamesDoNotExpandPublicAdmission(t *testing.T) {
	account := &Account{
		ID: 42, Platform: PlatformTypeSafe, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-key", "base_url": "https://relay.example",
			"model_mapping":   map[string]any{"custom-alias": "upstream-model"},
			"model_whitelist": []any{"custom-alias", "explicit-only"},
		},
	}
	require.ElementsMatch(t, []string{"custom-alias", "explicit-only"}, account.GetConfiguredRequestModels())
	for name, target := range map[string]string{"custom-alias": "upstream-model", "explicit-only": "explicit-only"} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, name == "explicit-only", account.IsModelSupported(name))
			svc, upstream := adaptiveCNAccountTestService(account, newJSONResponse(http.StatusOK, `{"answers":{"test":{}}}`))
			svc.openAIGatewayService = &OpenAIGatewayService{cfg: svc.cfg}
			c, _ := newTestContext()
			require.NoError(t, svc.TestAccountConnectionWithOptions(c, account.ID, name, "diagnostic", AccountTestTypeText, AccountTestModeDefault, APIProtocolSystemOne, AccountTestOptions{}))
			require.Equal(t, target, gjson.GetBytes(upstream.lastBody, "model").String())
		})
	}
}

// 智能 Key 的目录与选组必须认可相同的具体白名单。通配映射只能改写已声明的名字。
func TestTypeSafeSmartRoutingRespectsConfiguredModels(t *testing.T) {
	group := &Group{ID: 18, Platform: PlatformTypeSafe, Status: StatusActive}
	account := Account{
		ID: 8, Platform: PlatformTypeSafe, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{group.ID},
		Credentials: map[string]any{
			"model_whitelist": []any{"jev-1.14", "team-decision"},
			"model_mapping":   map[string]any{"jev-latest": "jev-1.14", "team-*": "jev-1.14"},
		},
	}
	repo := &smartRoutingAccountRepo{accounts: map[int64][]Account{group.ID: {account}}}
	svc := NewSmartRoutingService(&GatewayService{accountRepo: repo}, nil)
	list, err := svc.ResolveSmartRoutingModels(t.Context(), group)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"jev-latest", "jev-1.14", "team-decision"}, list)
	for _, model := range list {
		result, err := svc.EvaluateSmartRoutingGroup(t.Context(), group, model, "/v1/systemone")
		require.NoError(t, err)
		require.True(t, result.Schedulable, model)
	}
	result, err := svc.EvaluateSmartRoutingGroup(t.Context(), group, "team-unlisted", "/v1/systemone")
	require.NoError(t, err)
	require.False(t, result.HasModel)
}
