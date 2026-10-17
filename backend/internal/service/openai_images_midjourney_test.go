//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 图片品牌不新增调度平台，也不得误进入 GPT 原生或 OAuth 通路。
func TestCompatibleImagesMidjourneyModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"MJ-v7", "MJ-v8.1", "MJ-v8.2", " mj_v7 ", "Midjourney-v8.2", "MJ", "mj-v", "mj-v8..2", "mj-v8.2-chat", "mj_custom", " MjFast "} {
		t.Run(model, func(t *testing.T) {
			require.True(t, IsMidjourneyImageGenerationModel(model))
			require.False(t, IsGPTImageGenerationModel(model))
			require.False(t, isOpenAIImageGenerationModel(model))
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw a sailboat","size":"1024x1024","n":1}`, model))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, bytes.NewReader(body))
			parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			require.Equal(t, OpenAIImagesCapabilityAPIKey, parsed.RequiredCapability)
			require.Equal(t, strings.TrimSpace(model), parsed.Model)
			count, err := parsed.ExpectedOutputCountForAccount(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, parsed.Model)
			require.NoError(t, err)
			require.Equal(t, 4, count)
			require.True(t, (&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}).SupportsOpenAIImageCapability(parsed.RequiredCapability))
			for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
				require.False(t, (&Account{Platform: PlatformOpenAI, Type: typ}).SupportsOpenAIImageCapability(parsed.RequiredCapability))
			}
		})
	}
	for _, model := range []string{"", "prefix-mj-v7", "midjourney-text", "gpt-image-2"} {
		require.False(t, IsMidjourneyImageGenerationModel(model), model)
	}
	for _, model := range []string{"prefix-mj-v7", "midjourney-text"} {
		require.Error(t, validateCompatibleImagesModel(model), model)
	}
}

// 未验证的上传、编辑和流式路径必须在模型映射之后仍然失败关闭，其他模型规则不变。
func TestCompatibleImagesMidjourneySupportedRequestBoundary(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-image-2": "MJ-v8.2"}}}
	for _, request := range []OpenAIImagesRequest{
		{Model: "gpt-image-2", N: 1, Endpoint: openAIImagesGenerationsEndpoint, Multipart: true},
		{Model: "gpt-image-2", N: 1, Endpoint: openAIImagesEditsEndpoint},
		{Model: "gpt-image-2", N: 1, Endpoint: openAIImagesGenerationsEndpoint, Stream: true},
	} {
		require.NoError(t, request.ValidateRoutingModel("gpt-image-2"))
		require.ErrorContains(t, request.ValidateRoutingModel("MJ-v8.2"), "non-streaming JSON")
		_, err := request.ExpectedOutputCountForAccount(account, "gpt-image-2")
		require.ErrorContains(t, err, "non-streaming JSON")
	}
}

// n=1 返回四张且 Token 为零时，完整转发与账务入口仍按四张结算一次。
func TestCompatibleImagesMidjourneyFourOutputsChargeActualCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, format := range []string{"url", "b64_json"} {
		for _, source := range []string{PricingSourceGroup, PricingSourceChannel} {
			for _, mode := range []BillingMode{BillingModeImage, BillingModePerRequest} {
				t.Run(fmt.Sprintf("%s/%s/%s", format, source, mode), func(t *testing.T) {
					var responseBody string
					if format == "url" {
						responseBody = `{"data":[{"url":"https://media.example/1.png"},{"url":"https://media.example/2.png"},{"url":"https://media.example/3.png"},{"url":"https://media.example/4.png"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`
					} else {
						responseBody = `{"data":[{"b64_json":"MQ=="},{"b64_json":"Mg=="},{"b64_json":"Mw=="},{"b64_json":"NA=="}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`
					}
					requestBody := []byte(`{"model":"MJ-v8.2","prompt":"draw a sailboat","n":1}`)
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, bytes.NewReader(requestBody))
					c.Request.Header.Set("Content-Type", "application/json")
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK,
						Header: http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"mj-four-output"}},
						Body:   io.NopCloser(strings.NewReader(responseBody))}}
					usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
					svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
					svc.httpUpstream = upstream
					groupID := int64(501)
					price := 0.4
					pricing := ChannelModelPricing{Models: []string{"MJ-v8.2"}, BillingMode: mode, PerRequestPrice: &price}
					group := &Group{ID: groupID, RateMultiplier: 3, ImageRateIndependent: true, ImageRateMultiplier: 0.85}
					if source == PricingSourceGroup {
						group.ModelPricing = []ChannelModelPricing{pricing}
						svc.resolver = NewModelPricingResolver(nil, svc.billingService)
					} else {
						cache := newEmptyChannelCache()
						cache.pricingByGroupModel[channelModelKey{groupID: groupID, model: "mj-v8.2"}] = &pricing
						cache.channelByGroupID[groupID] = &Channel{ID: groupID, Status: StatusActive}
						cache.groupPlatform[groupID] = ""
						cache.loadedAt = time.Now()
						channels := &ChannelService{}
						channels.cache.Store(cache)
						svc.resolver = NewModelPricingResolver(channels, svc.billingService)
					}
					account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
						"api_key": "test-key", "base_url": "https://compatible.example/v1",
					}}
					parsed, err := svc.ParseOpenAIImagesRequest(c, requestBody)
					require.NoError(t, err)
					result, err := svc.ForwardImages(context.Background(), c, account, requestBody, parsed, "")
					require.NoError(t, err)
					require.Len(t, upstream.requests, 1)
					require.Equal(t, int64(1), gjson.GetBytes(upstream.lastBody, "n").Int())
					require.Equal(t, 4, result.ImageCount)
					require.Equal(t, int64(4), gjson.Get(rec.Body.String(), "data.#").Int())
					require.Zero(t, result.Usage.InputTokens)
					require.Zero(t, result.Usage.OutputTokens)
					err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
						Result: result, APIKey: &APIKey{ID: 2, GroupID: &groupID, Group: group},
						User: &User{ID: 1}, Account: account,
						InboundEndpoint: openAIImagesGenerationsEndpoint, UpstreamEndpoint: openAIImagesGenerationsEndpoint,
					})
					require.NoError(t, err)
					require.Equal(t, 4, usageRepo.lastLog.ImageCount)
					require.InDelta(t, 1.6, usageRepo.lastLog.TotalCost, 1e-12)
					require.InDelta(t, 1.36, usageRepo.lastLog.ActualCost, 1e-12)
					billing := requireOpenAIRecordUsageBillingRepoStub(t, svc)
					require.Equal(t, 1, billing.calls)
					require.InDelta(t, 1.36, billing.lastCmd.BillableAmountUSD, 1e-12)
				})
			}
		}
	}
}

// 用户原始型号、渠道别名与账号映射均沿用已有 Images 透传，不生成额外请求。
func TestCompatibleImagesMidjourneyAPIKeyForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mapping := range []string{"direct", "channel", "account"} {
		t.Run(mapping, func(t *testing.T) {
			requestModel, channelModel := "MJ-v8.2", ""
			credentials := map[string]any{"api_key": "image-test-key", "base_url": "https://compatible.example/v1"}
			if mapping == "channel" {
				requestModel, channelModel = "public-art", "MJ-v8.2"
			}
			if mapping == "account" {
				requestModel = "MJ-v7"
				credentials["model_mapping"] = map[string]any{requestModel: "MJ-v8.2"}
			}
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw a sailboat","n":1,"size":"1024x1024","response_format":"url","custom_field":"preserved"}`, requestModel))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{"created":1,"data":[{"url":"https://media.example/result.png"}]}`)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			parsed, err := svc.ParseOpenAIImagesRequestForRouting(c, body)
			require.NoError(t, err)
			require.NoError(t, parsed.ValidateRoutingModel(firstNonEmptyString(channelModel, requestModel)))
			require.Equal(t, OpenAIImagesCapabilityAPIKey, parsed.RequiredCapability)
			result, err := svc.ForwardImages(context.Background(), c,
				&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: credentials}, body, parsed, channelModel)
			require.NoError(t, err)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "MJ-v8.2", result.UpstreamModel)
			require.Equal(t, 1, result.ImageCount)
			require.Equal(t, "https://compatible.example/v1/images/generations", upstream.lastReq.URL.String())
			require.Equal(t, "MJ-v8.2", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, "preserved", gjson.GetBytes(upstream.lastBody, "custom_field").String())
			require.Equal(t, "url", gjson.GetBytes(upstream.lastBody, "response_format").String())
		})
	}
}

// 即使绕过调度直接调用转发，OAuth 与 Setup Token 也必须在出站前拒绝 MJ。
func TestCompatibleImagesMidjourneyNativeAccountsRejectBeforeForwarding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		for _, mapping := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mapping=%t", typ, mapping), func(t *testing.T) {
				model := "MJ-v8.2"
				account := &Account{Platform: PlatformOpenAI, Type: typ, Credentials: map[string]any{"access_token": "unused"}}
				if mapping {
					account.Credentials["model_mapping"] = map[string]any{"gpt-image-2": model}
					model = "gpt-image-2"
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, nil)
				upstream := &httpUpstreamRecorder{}
				_, err := (&OpenAIGatewayService{httpUpstream: upstream}).ForwardImages(context.Background(), c, account, nil, &OpenAIImagesRequest{Model: model}, "")
				require.ErrorContains(t, err, "images endpoint requires an image model")
				require.Empty(t, upstream.requests)
			})
		}
	}
}
