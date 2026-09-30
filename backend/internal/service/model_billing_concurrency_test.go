//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 共用目录、渠道指针和区间价卡，交错运行不同型号与档位，防止请求修改共享价格。
func TestModelBillingConcurrentSharedPricing(t *testing.T) {
	type scenario struct {
		name  string
		input CostInput
		want  [4]float64
	}
	for source, catalog := range newSeptemberModelPricingSources(t) {
		t.Run(source, func(t *testing.T) {
			billing := NewBillingService(&config.Config{}, catalog)
			resolver := NewModelPricingResolver(nil, billing)
			var cases []scenario
			var shared []*ResolvedPricing
			for _, model := range []struct {
				name                       string
				input, output, write, read float64
				long                       bool
			}{
				{"gpt-6.1-sol", 2e-6, 10e-6, 2.5e-6, .1e-6, true},
				{"gpt-6-sol", 2e-6, 10e-6, 2.5e-6, .2e-6, true},
				{"gpt-6-luna", .1e-6, .5e-6, .125e-6, .01e-6, true},
				{"claude-sonnet-5-5", 2e-6, 10e-6, 2.5e-6, .2e-6, false},
				{"claude-opus-5-5", 4e-6, 20e-6, 5e-6, .2e-6, false},
			} {
				for _, inputTokens := range []int{271950, 271951} {
					for _, tier := range []struct {
						name   string
						factor float64
					}{{"default", 1}, {"priority", 2}, {"flex", .5}} {
						inFactor, outFactor := tier.factor, tier.factor
						if model.long && inputTokens+50 > 272000 {
							inFactor *= 2
							outFactor *= 1.5
						}
						cases = append(cases, scenario{
							name: source + "/" + model.name + "/" + tier.name,
							input: CostInput{Ctx: context.Background(), Model: model.name,
								Tokens:         UsageTokens{InputTokens: inputTokens, OutputTokens: 100, CacheCreationTokens: 20, CacheReadTokens: 30},
								RateMultiplier: 1.75, ServiceTier: tier.name, Resolver: resolver},
							want: [4]float64{float64(inputTokens) * model.input * inFactor, 100 * model.output * outFactor, 20 * model.write * inFactor, 30 * model.read * inFactor},
						})
					}
				}
			}
			for _, fast := range []float64{0, 3} {
				// 使用真正的渠道覆盖入口，并保留显式免费输出及缓存写入价格。
				base, err := billing.GetModelPricingWithChannel("gpt-6.1-sol", &ChannelModelPricing{
					BillingMode: BillingModeToken, InputPrice: float64Ptr(.006), OutputPrice: float64Ptr(0),
					CacheWritePrice: float64Ptr(0), CacheReadPrice: float64Ptr(.003),
					FastModeMultiplier: float64Ptr(fast), FlexMultiplier: float64Ptr(.25),
				})
				require.NoError(t, err)
				boundary := 300000
				for _, intervals := range [][]PricingInterval{nil, {
					{MaxTokens: &boundary, InputPrice: float64Ptr(.001), OutputPrice: float64Ptr(0), CacheWritePrice: float64Ptr(0), CacheReadPrice: float64Ptr(.002)},
					{MinTokens: boundary, InputPrice: float64Ptr(0), OutputPrice: float64Ptr(.004), CacheWritePrice: float64Ptr(.005), CacheReadPrice: float64Ptr(0)},
				}} {
					resolved := &ResolvedPricing{Mode: BillingModeToken, Source: PricingSourceChannel, BasePricing: base, Intervals: intervals, longContextPricingEnabled: true}
					shared = append(shared, resolved)
					for _, model := range []string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6-sol", "claude-sonnet-5-5"} {
						for _, total := range []int{300000, 300001} {
							for _, tier := range []struct {
								name   string
								factor float64
							}{{"default", 1}, {"fast", fast}, {"flex", .25}, {"ultrafast", 6}} {
								if tier.name == "ultrafast" && model != "gpt-6-astra" {
									continue
								}
								inPrice, outPrice, writePrice, readPrice := .006, 0.0, 0.0, .003
								if len(intervals) > 0 {
									inPrice, readPrice = .001, .002
									if total > boundary {
										inPrice, outPrice, writePrice, readPrice = 0, .004, .005, 0
									}
								} else {
									// 无区间时沿用共享基础价卡的长上下文政策；区间价不得再次溢价。
									inPrice, writePrice, readPrice = inPrice*2, writePrice*2, readPrice*2
									outPrice *= 1.5
								}
								cases = append(cases, scenario{
									name:  fmt.Sprintf("channel/%s/%s/fast=%g/interval=%t/total=%d", model, tier.name, fast, len(intervals) > 0, total),
									input: CostInput{Ctx: context.Background(), Model: model, Tokens: UsageTokens{InputTokens: total - 50, OutputTokens: 100, CacheCreationTokens: 20, CacheReadTokens: 30}, RateMultiplier: 1.75, ServiceTier: tier.name, Resolver: resolver, Resolved: resolved},
									want:  [4]float64{float64(total-50) * inPrice * tier.factor, 100 * outPrice * tier.factor, 20 * writePrice * tier.factor, 30 * readPrice * tier.factor},
								})
							}
						}
					}
				}
			}
			before, err := json.Marshal(shared)
			require.NoError(t, err)
			var catalogBefore []byte
			if catalog != nil {
				catalogBefore, err = json.Marshal(catalog.pricingData)
				require.NoError(t, err)
			}
			const workers, iterations = 128, 128
			start := make(chan struct{})
			errors := make(chan error, workers)
			totals := make(chan [2]float64, workers)
			var wg sync.WaitGroup
			for worker := 0; worker < workers; worker++ {
				wg.Add(1)
				go func(worker int) {
					defer wg.Done()
					<-start
					var sums [2]float64
					for n := 0; n < iterations; n++ {
						tc := cases[(worker*iterations+n)%len(cases)]
						cost, err := billing.CalculateCostUnified(tc.input)
						if err != nil {
							errors <- fmt.Errorf("%s: %w", tc.name, err)
							return
						}
						actual := [4]float64{cost.InputCost, cost.OutputCost, cost.CacheCreationCost, cost.CacheReadCost}
						wantTotal := 0.0
						for bucket, want := range tc.want {
							wantTotal += want
							if math.Abs(actual[bucket]-want) > 1e-9 {
								errors <- fmt.Errorf("%s bucket %d: got %.12f want %.12f", tc.name, bucket, actual[bucket], want)
								return
							}
						}
						if math.Abs(cost.TotalCost-wantTotal) > 1e-9 || math.Abs(cost.ActualCost-wantTotal*1.75) > 1e-9 {
							errors <- fmt.Errorf("%s: total=%g actual=%g want=%g", tc.name, cost.TotalCost, cost.ActualCost, wantTotal)
							return
						}
						sums[0] += cost.ActualCost
						sums[1] += wantTotal * 1.75
					}
					totals <- sums
				}(worker)
			}
			close(start)
			wg.Wait()
			close(errors)
			close(totals)
			for err := range errors {
				t.Error(err)
			}
			var sums [2]float64
			completed := 0
			for pair := range totals {
				completed++
				sums[0] += pair[0]
				sums[1] += pair[1]
			}
			require.Equal(t, workers, completed)
			require.InDelta(t, sums[1], sums[0], 1e-6)
			after, err := json.Marshal(shared)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "并发定价不得修改渠道及区间价卡")
			if catalog != nil {
				after, err = json.Marshal(catalog.pricingData)
				require.NoError(t, err)
				require.Equal(t, string(catalogBefore), string(after), "并发定价不得修改目录价格")
			}
			t.Logf("workers=%d calculations=%d cases=%d aggregate_actual_usd=%.8f", workers, workers*iterations, len(cases), sums[0])
		})
	}
}

// 直接连接四条流式桥和实际 RecordUsage，断言互斥缓存桶进入同一结算命令且终态不叠加。
func TestModelBillingConcurrentStreamUsageSettlement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	billing := NewBillingService(&config.Config{}, nil)
	for _, adapter := range []string{"anthropic_chat", "anthropic_responses", "native_chat", "native_responses"} {
		for _, tc := range []struct {
			name, start, delta         string
			input, output, write, read int
		}{
			{"late_full_cache", `"input_tokens":1200,"prompt_tokens":1200`, `"output_tokens":30,"prompt_tokens":1200,"cache_read_input_tokens":1200`, 0, 30, 0, 1200},
			{"late_partial_cache", `"input_tokens":1200,"prompt_tokens":1200`, `"output_tokens":30,"prompt_tokens":1200,"cache_read_input_tokens":800,"cache_creation_input_tokens":100`, 300, 30, 100, 800},
			{"independent_input", `"input_tokens":308`, `"input_tokens":0,"output_tokens":49,"cache_read_input_tokens":300,"cache_creation_input_tokens":17`, 308, 49, 17, 300},
			{"deepseek_buckets", `"input_tokens":0`, `"input_tokens":1200,"output_tokens":30,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400`, 400, 30, 0, 800},
		} {
			for _, terminal := range []string{"stop", "eof"} {
				for _, model := range []string{"claude-sonnet-5-5", "claude-opus-5-5"} {
					t.Run(adapter+"/"+tc.name+"/"+terminal+"/"+model, func(t *testing.T) {
						t.Parallel()
						for iteration := 0; iteration < 8; iteration++ {
							sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_local\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"" + model + "\",\"content\":[],\"usage\":{" + tc.start + "}}}\n\n"
							// 上游重复累计用量不应成为增量；明确终态和 EOF 两条收尾路径都覆盖。
							for repeat := 0; repeat < 2; repeat++ {
								sse += "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{" + tc.delta + "}}\n\n"
							}
							if terminal == "stop" {
								sse += "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
							}
							rec := httptest.NewRecorder()
							c, _ := gin.CreateTestContext(rec)
							c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
							response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(sse))}
							logRepo := &openAIRecordUsageLogRepoStub{inserted: true}
							ledger := &openAIRecordUsageBillingRepoStub{}
							account := &Account{ID: 301, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
							if strings.HasPrefix(adapter, "native") {
								svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logRepo, ledger, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
								svc.billingService = billing
								var result *OpenAIForwardResult
								var err error
								if adapter == "native_chat" {
									result, err = svc.handleCCStreamingFromNativeAnthropic(response, c, model, model, model, nil, time.Now())
								} else {
									result, err = svc.handleResponsesStreamingFromNativeAnthropic(response, c, model, model, model, nil, time.Now(), apicompat.ResponsesClientToolMapping{})
								}
								require.NoError(t, err)
								err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 101}, User: &User{ID: 201}, Account: account})
								require.NoError(t, err)
							} else {
								account.Platform = PlatformAnthropic
								svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logRepo, ledger, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
								svc.billingService = billing
								var result *ForwardResult
								var err error
								if adapter == "anthropic_chat" {
									result, err = svc.handleCCStreamingFromAnthropic(response, c, model, model, nil, time.Now())
								} else {
									result, err = svc.handleResponsesStreamingResponse(response, c, model, model, nil, time.Now(), apicompat.ResponsesClientToolMapping{})
								}
								require.NoError(t, err)
								err = svc.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: &APIKey{ID: 101}, User: &User{ID: 201}, Account: account})
								require.NoError(t, err)
							}
							require.Equal(t, 1, logRepo.calls)
							require.Equal(t, 1, ledger.calls)
							require.Equal(t, tc.input, logRepo.lastLog.InputTokens)
							require.Equal(t, tc.output, logRepo.lastLog.OutputTokens)
							require.Equal(t, tc.write, logRepo.lastLog.CacheCreationTokens)
							require.Equal(t, tc.read, logRepo.lastLog.CacheReadTokens)
							inputPrice, outputPrice, writePrice := 2e-6, 10e-6, 2.5e-6
							if model == "claude-opus-5-5" {
								inputPrice, outputPrice, writePrice = 4e-6, 20e-6, 5e-6
							}
							want := float64(tc.input)*inputPrice + float64(tc.output)*outputPrice + float64(tc.write)*writePrice + float64(tc.read)*.2e-6
							require.InDelta(t, want, logRepo.lastLog.TotalCost, 1e-12)
							require.InDelta(t, want*1.1, logRepo.lastLog.ActualCost, 1e-12)
							require.InDelta(t, math.Round(want*1.1*1e8)/1e8, ledger.lastCmd.BillableAmountUSD, 1e-12)
						}
					})
				}
			}
		}
	}
}

// 检查实际入账入口使用权威的最终档位，OAuth 的 default 回显不能把 Ultrafast 改为普通价。
func TestModelBillingAstraTierSettlement(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth, AccountTypeSetupToken} {
		for _, observed := range []string{"", "ultrafast", "priority", "fast", "default", "flex"} {
			t.Run(accountType+"/"+observed, func(t *testing.T) {
				t.Parallel()
				logRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				ledger := &openAIRecordUsageBillingRepoStub{}
				svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logRepo, ledger, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				tier, wantTier, factor := "ultrafast", "ultrafast", 6.0
				switch observed {
				case "priority", "fast":
					wantTier, factor = observed, 2
				case "flex":
					wantTier, factor = observed, .5
				case "default":
					if accountType == AccountTypeAPIKey {
						wantTier, factor = observed, 1
					}
				}
				err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
					Result: &OpenAIForwardResult{RequestID: "astra-tier-local", Model: "gpt-6-astra", ServiceTier: &tier, UpstreamResponseServiceTier: observed,
						Usage: OpenAIUsage{InputTokens: 150, OutputTokens: 40, CacheCreationInputTokens: 20, CacheReadInputTokens: 30}},
					APIKey: &APIKey{ID: 101}, User: &User{ID: 201}, Account: &Account{ID: 301, Platform: PlatformOpenAI, Type: accountType},
				})
				require.NoError(t, err)
				require.Equal(t, 1, ledger.calls)
				require.Equal(t, wantTier, *logRepo.lastLog.ServiceTier)
				want := (100*10e-6 + 40*50e-6 + 20*12.5e-6 + 30*1e-6) * factor
				require.InDelta(t, want, logRepo.lastLog.TotalCost, 1e-12)
				require.InDelta(t, math.Round(want*1.1*1e8)/1e8, ledger.lastCmd.BillableAmountUSD, 1e-12)
			})
		}
	}
}

// 四条协议桥都必须在映射后拒绝 Sonnet 非默认温度，不能进入上游或结算阶段。
func TestModelBillingMappedSonnetBridgeValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, adapter := range []string{"anthropic_chat", "anthropic_responses", "native_chat", "native_responses"} {
		t.Run(adapter, func(t *testing.T) {
			body := []byte(`{"model":"alias","input":"hello","messages":[{"role":"user","content":"hello"}],"temperature":0.5}`)
			account := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"alias": "claude-sonnet-5-5"}}}
			upstream := &httpUpstreamRecorder{}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			var err error
			switch adapter {
			case "anthropic_chat":
				_, err = (&GatewayService{httpUpstream: upstream}).ForwardAsChatCompletions(context.Background(), c, account, body, nil)
			case "anthropic_responses":
				_, err = (&GatewayService{httpUpstream: upstream}).ForwardAsResponses(context.Background(), c, account, body, nil)
			case "native_chat":
				_, err = (&OpenAIGatewayService{httpUpstream: upstream}).forwardChatCompletionsViaNativeAnthropic(context.Background(), c, account, body, "")
			case "native_responses":
				_, err = (&OpenAIGatewayService{httpUpstream: upstream}).forwardResponsesViaNativeAnthropic(context.Background(), c, account, body, "")
			}
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), "non-default temperature")
			require.Equal(t, OpsClientBusinessLimitedReasonLocalPolicyDenied, OpsClientBusinessLimitedReason(c))
			require.Nil(t, upstream.lastReq)
		})
	}
}
