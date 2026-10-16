//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 请求适配与真实报价之后才构造数据库任务，防止集成测试绕过发生少预扣的字段解析环节。
func seedanceFramesBillingTask(t *testing.T, f *videoBillingFixture, mode videoProfileBillingCase) *service.VideoTaskRecord {
	t.Helper()
	ctx := context.Background()
	target := service.VideoUpstreamTarget{Version: 2, ProtocolAdapter: service.VideoProtocolAdapterSeedance, Endpoint: service.VideoEndpointSeedance,
		AccountID: f.accountID, BaseURL: "https://frozen.example", CreatePath: "/api/v3/contents/generations/tasks", Model: "doubao-seedance-1-0-pro-250528"}
	body, metadata, err := service.PrepareVideoProtocolRequest([]byte(`{"model":"public-model","resolution":"720p","duration":2,"frames":289,"content":[{"type":"text","text":"隔离账务测试"},{"type":"image_url","image_url":{"url":"https://example.com/reference.png"},"role":"first_frame"}]}`), "", target)
	require.NoError(t, err)
	require.Equal(t, int64(2), gjson.GetBytes(body, "duration").Int())
	require.Equal(t, int64(289), gjson.GetBytes(body, "frames").Int())
	require.InDelta(t, 289.0/24, metadata.DurationSeconds, 1e-10)
	price, imagePrice, prepay := 1.0, .15, .3
	pricing := service.ChannelModelPricing{Platform: service.PlatformVideo, Models: []string{target.Model}, BillingMode: mode.mode,
		VideoPrices: []service.VideoPriceTier{{Resolution: "720p", Price: &price}}, VideoImageInputPricing: &service.VideoImageInputPricing{Price: &imagePrice}}
	if mode.mode == service.BillingModeVideoToken && !mode.deferToken {
		pricing.VideoTokenPrepay = &service.VideoTokenPrepayConfig{PricePerSecond: &prepay}
	}
	group := &service.Group{ID: f.groupID, Platform: service.PlatformVideo, RateMultiplier: 3, ModelPricing: []service.ChannelModelPricing{pricing}}
	quote, err := service.NewModelPricingResolver(nil, nil).QuoteVideo(ctx, service.VideoPriceInput{Group: group, Model: target.Model,
		Resolution: metadata.Resolution, ReferenceImageCount: metadata.ReferenceImageCount, RateMultiplier: 3})
	require.NoError(t, err)
	zero := int64(0)
	estimate, err := quote.Calculate(metadata.DurationSeconds, &zero)
	require.NoError(t, err)
	base := estimate.OutputCost
	if quote.TokenPrepay != nil {
		base = metadata.DurationSeconds * *quote.TokenPrepay.PricePerSecond
	}
	task := f.task(base, 3, 3)
	task.Target, task.Metadata, task.Quote, task.AccountRateMultiplier = target, metadata, quote, 1
	task.RequestedModel, task.InternalModel, task.InboundPath = "public-model", target.Model, target.CreatePath
	task.Hold.VideoFixedAmountUSD = estimate.ImageInputCost
	task.Hold.VideoDeferredBilling = mode.deferToken
	task.Hold.VideoTokenPrepay = quote.TokenPrepay != nil
	if task.Hold.VideoTokenPrepay {
		task.Hold.VideoPrepayDurationSeconds = metadata.DurationSeconds
	}
	require.NoError(t, f.tasks.Save(ctx, task, false))
	return task
}

// 分数秒预扣在重启重载和并发结算后不重复累计，失败释放仍退还全部视频及固定图片预算。
func TestVideoSeedanceFramesBillingConcurrentRecovery(t *testing.T) {
	for _, mode := range videoProfileModes() {
		for _, subscription := range []bool{false, true} {
			for _, status := range []string{"completed", "failed"} {
				t.Run(fmt.Sprintf("%s/deferred_%t/subscription_%t/%s", mode.mode, mode.deferToken, subscription, status), func(t *testing.T) {
					ctx := context.Background()
					f := newVideoBillingFixture(t)
					f.quota(1000)
					var sub *service.UserSubscription
					if subscription {
						sub = fixedVideoSubscription(t, f, 1000, .5)
					}
					task := f.reserve(seedanceFramesBillingTask(t, f, mode))
					rate := 3.0
					if subscription {
						rate = .5
					}
					wantHold, actualBase := (289.0/24)*rate+.15, 241.0/24
					if mode.mode == service.BillingModeVideoPerRequest {
						wantHold, actualBase = rate+.15, 1
					} else if mode.mode == service.BillingModeVideoToken {
						wantHold, actualBase = (289.0/24)*.3+.15, 20
						if mode.deferToken {
							wantHold = 0
						}
					}
					require.InDelta(t, wantHold, task.Hold.HoldAmount, 1e-8)
					videoProfileMoney(t, f, sub, wantHold, wantHold, 0)
					frozenTarget, frozenQuote := task.Target, *task.Quote
					// 管理员改配置后重新建立仓储，只能继续使用原冻结协议和价格。
					_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=$2::jsonb WHERE id=$1`, f.accountID,
						`{"base_url":"https://changed.example","video_endpoints":["seedance"],"video_endpoint_adapters":{"seedance":"legacy_v1"}}`)
					require.NoError(t, err)
					task, err = NewVideoTaskRepository(integrationDB).Get(ctx, task.ID)
					require.NoError(t, err)
					require.Equal(t, frozenTarget, task.Target)
					require.Equal(t, frozenQuote, *task.Quote)
					require.InDelta(t, 289.0/24, task.Metadata.DurationSeconds, 1e-10)
					response, err := service.ExtractVideoProtocolResponse(task.Target, 200, []byte(`{"id":"frames-task","status":"succeeded","resolution":"720p","frames":241,"framespersecond":24,"content":{"video_url":"https://example.com/video.mp4"},"usage":{"completion_tokens":20000000}}`), "frames-task")
					require.NoError(t, err)
					require.Empty(t, response.MetadataError)
					require.InDelta(t, 241.0/24, response.Metadata.DurationSeconds, 1e-10)
					command, cost := videoProfileFinalCommand(t, f, task, status, response.Metadata.DurationSeconds, *response.Metadata.Tokens)
					require.InDelta(t, actualBase, cost.OutputCost, 1e-8)
					var applied atomic.Int64
					var wg sync.WaitGroup
					errs := make(chan error, 24)
					for range 24 {
						wg.Add(1)
						go func() {
							defer wg.Done()
							local := command
							apply := f.repo.CaptureBatchImageBalance
							if status == "failed" {
								apply = f.repo.ReleaseBatchImageBalance
							}
							result, err := apply(ctx, &local)
							if err == nil && result.Applied {
								applied.Add(1)
							}
							errs <- err
						}()
					}
					wg.Wait()
					close(errs)
					for err := range errs {
						require.NoError(t, err)
					}
					require.EqualValues(t, 1, applied.Load())
					due, accountCost, billingStatus := actualBase*rate+.15, actualBase+.15, "settled"
					if status == "failed" {
						due, accountCost, billingStatus = 0, 0, "released"
					}
					videoProfileMoney(t, f, sub, due, 0, accountCost)
					fresh, err := NewVideoTaskRepository(integrationDB).Get(ctx, task.ID)
					require.NoError(t, err)
					require.Equal(t, billingStatus, fresh.BillingStatus)
					require.Equal(t, frozenTarget, fresh.Target)
					require.Equal(t, frozenQuote, *fresh.Quote)
					if fresh.Hold.VideoTokenPrepay {
						require.InDelta(t, 289.0/24, fresh.Hold.VideoPrepayDurationSeconds, 1e-10, "真实短时长不能改写创建时的固定预扣秒数")
					}
					var rows int
					require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1 AND request_id=$2`, f.keyID, command.RequestID).Scan(&rows))
					require.Equal(t, 1, rows)
				})
			}
		}
	}
}
