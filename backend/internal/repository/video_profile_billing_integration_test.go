//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 四象限沿用正式适配器和报价器；此层只模拟上游终态已持久化，不发送生成请求。
type videoProfileBillingCase struct {
	profile    string
	image      bool
	mode       service.BillingMode
	deferToken bool
}

func videoProfileBillingTask(t *testing.T, f *videoBillingFixture, c videoProfileBillingCase) *service.VideoTaskRecord {
	t.Helper()
	body := map[string]any{"model": "profile-video", "prompt": "隔离账务测试", "seconds": "8", "size": "1280x720"}
	wantResolution := "1280x720"
	if c.image {
		body["input_reference"] = map[string]any{"image_url": "https://example.com/reference.png"}
	}
	if c.profile == service.VideoOpenAIProfileMetasoH3 {
		body = map[string]any{"model": "profile-video", "prompt": "隔离账务测试", "duration": 8, "resolution": "768p", "ratio": "16:9"}
		wantResolution = "768p"
		if c.image {
			body["images"] = []string{"https://example.com/reference.png"}
		}
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	prepared, metadata, err := service.PrepareVideoOpenAIRequest(raw, "", c.profile)
	require.NoError(t, err)
	require.Equal(t, wantResolution, metadata.Resolution)
	require.Equal(t, "1280x720", metadata.Size)
	require.Equal(t, 8.0, metadata.DurationSeconds)
	require.NotNil(t, metadata.ReferenceImageCount)
	wantImages := 0
	if c.image {
		wantImages = 1
	}
	require.Equal(t, wantImages, *metadata.ReferenceImageCount)
	if c.profile == service.VideoOpenAIProfilePassthrough {
		require.Equal(t, raw, prepared, "默认配置必须保留原始请求")
	}
	price := .2
	if c.mode == service.BillingModeVideoToken {
		price = 10
	} else if c.mode == service.BillingModeVideoPerRequest {
		price = 1.25
	}
	imagePrice, priceMultiplier, prepay := .15, 2.0, .3
	pricing := service.ChannelModelPricing{Platform: service.PlatformVideo, Models: []string{"profile-video"}, BillingMode: c.mode,
		PriceMultiplier: &priceMultiplier, VideoPrices: []service.VideoPriceTier{{Resolution: wantResolution, Price: &price}},
		VideoImageInputPricing: &service.VideoImageInputPricing{FreeImages: 0, Price: &imagePrice}}
	if c.mode == service.BillingModeVideoToken && !c.deferToken {
		pricing.VideoTokenPrepay = &service.VideoTokenPrepayConfig{PricePerSecond: &prepay}
	}
	group := &service.Group{ID: f.groupID, Platform: service.PlatformVideo, RateMultiplier: 3, ModelPricing: []service.ChannelModelPricing{pricing}}
	quote, err := service.NewModelPricingResolver(nil, nil).QuoteVideo(context.Background(), service.VideoPriceInput{
		Group: group, Model: metadata.Model, Resolution: metadata.Resolution, ReferenceImageCount: metadata.ReferenceImageCount, RateMultiplier: 3})
	require.NoError(t, err)
	zero := int64(0)
	estimate, err := quote.Calculate(metadata.DurationSeconds, &zero)
	require.NoError(t, err)
	base := estimate.OutputCost
	if c.mode == service.BillingModeVideoToken && !c.deferToken {
		base = metadata.DurationSeconds * *quote.TokenPrepay.PricePerSecond
	}
	id, now, lease := "vid_"+uuid.NewString(), time.Now().UTC(), uuid.NewString()
	task := &service.VideoTaskRecord{ID: id, UserID: f.userID, APIKeyID: f.keyID, GroupID: f.groupID, AccountID: f.accountID,
		PayloadHash: uuid.NewString(), Status: "prepared", BillingStatus: "pending", CreatedAt: now,
		LeaseToken: lease, LeaseUntil: now.Add(10 * time.Minute), Metadata: metadata, Quote: quote, AccountRateMultiplier: 1,
		Target: service.VideoUpstreamTarget{Version: 1, Endpoint: service.VideoEndpointOpenAIVideos, OpenAIProfile: c.profile, CreatePath: "/v1/videos", Model: metadata.Model, AccountID: f.accountID}}
	task.Hold = service.BatchImageBalanceHoldCommand{RequestID: "video_hold:" + id, BatchID: id, UserID: f.userID, ActorUserID: f.userID,
		APIKeyID: f.keyID, GroupID: &f.groupID, APIKeyBillingMode: service.APIKeyBillingModeAuto, PricingSnapshotVersion: 3,
		BaseAmountUSD: base, SubscriptionRateMultiplier: 3, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 3,
		SettlementRateScale: 1, ReservedAt: now, VideoEntity: true, VideoAccountID: f.accountID, VideoLeaseToken: lease}
	task.RequestedModel, task.InternalModel, task.InboundPath = metadata.Model, metadata.Model, "/v1/videos"
	task.Hold.VideoFixedAmountUSD = estimate.ImageInputCost
	task.Hold.VideoDeferredBilling = c.deferToken
	task.Hold.VideoTokenPrepay = c.mode == service.BillingModeVideoToken && !c.deferToken
	if task.Hold.VideoTokenPrepay {
		task.Hold.VideoPrepayDurationSeconds = metadata.DurationSeconds
	}
	_, created, err := f.tasks.Create(context.Background(), task, 1000)
	require.NoError(t, err)
	require.True(t, created)
	loaded, err := f.tasks.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, c.profile, loaded.Target.OpenAIProfile)
	require.Equal(t, wantResolution, loaded.Quote.Resolution)
	require.Equal(t, wantImages, *loaded.Quote.ReferenceImageCount)
	return loaded
}

// 预期金额由公开计费合同列出，不复用报价器输出作为断言的期望值。
func videoProfileExpected(c videoProfileBillingCase, subscription bool) (hold, due, account float64) {
	rate, fixed := 3.0, 0.0
	if subscription {
		rate = .5
	}
	if c.image {
		fixed = .15
	}
	switch c.mode {
	case service.BillingModeVideo:
		return 3.2*rate + fixed, 2*rate + fixed, 2 + fixed
	case service.BillingModeVideoPerRequest:
		return 2.5*rate + fixed, 2.5*rate + fixed, 2.5 + fixed
	default:
		if !c.deferToken {
			hold = 2.4 + fixed
		}
		return hold, 2*rate + fixed, 2 + fixed
	}
}

func videoProfileModes() []videoProfileBillingCase {
	return []videoProfileBillingCase{{mode: service.BillingModeVideo}, {mode: service.BillingModeVideoToken},
		{mode: service.BillingModeVideoPerRequest}, {mode: service.BillingModeVideoToken, deferToken: true}}
}

func videoProfileFinalCommand(t *testing.T, f *videoBillingFixture, task *service.VideoTaskRecord, status string, seconds float64, tokens int64) (service.BatchImageBalanceHoldCommand, *service.CostBreakdown) {
	t.Helper()
	cost, err := task.Quote.Calculate(seconds, &tokens)
	require.NoError(t, err)
	task.Metadata.DurationSeconds, task.Metadata.Tokens = seconds, &tokens
	require.NoError(t, f.tasks.Save(context.Background(), task, false))
	command := f.command(task, status, cost.OutputCost)
	command.VideoActualFixedAmountUSD, command.VideoAccountQuotaCost = cost.ImageInputCost, cost.TotalCost
	return command, cost
}

func videoProfileMoney(t *testing.T, f *videoBillingFixture, sub *service.UserSubscription, charge, frozen, account float64) {
	t.Helper()
	if sub != nil {
		fixedVideoAssertSubscription(t, sub.ID, charge)
		f.money(1000, 0, charge, account)
		f.quotaUsage(0, 0, 0)
	} else {
		f.money(1000-charge, frozen, charge, account)
		f.quotaUsage(charge, charge, charge)
	}
}

// 同一套四象限请求同时覆盖余额、订阅、三种最终价格与 Token 预扣开关。
func TestVideoProfileBillingLifecycle(t *testing.T) {
	for _, profile := range []string{service.VideoOpenAIProfilePassthrough, service.VideoOpenAIProfileMetasoH3} {
		for _, image := range []bool{false, true} {
			for _, mode := range videoProfileModes() {
				for _, subscription := range []bool{false, true} {
					for _, status := range []string{"completed", "failed"} {
						c := mode
						c.profile, c.image = profile, image
						name := fmt.Sprintf("%s/image_%t/%s/deferred_%t/subscription_%t/%s", profile, image, mode.mode, mode.deferToken, subscription, status)
						t.Run(name, func(t *testing.T) {
							f := newVideoBillingFixture(t)
							f.quota(1000)
							var sub *service.UserSubscription
							if subscription {
								sub = fixedVideoSubscription(t, f, 1000, .5)
							}
							task := f.reserve(videoProfileBillingTask(t, f, c))
							hold, due, account := videoProfileExpected(c, subscription)
							require.InDelta(t, hold, task.Hold.HoldAmount, 1e-8)
							videoProfileMoney(t, f, sub, hold, hold, 0)
							command, _ := videoProfileFinalCommand(t, f, task, status, 5, 100000)
							apply := f.repo.CaptureBatchImageBalance
							wantStatus := "settled"
							if status == "failed" {
								apply, due, account, wantStatus = f.repo.ReleaseBatchImageBalance, 0, 0, "released"
							}
							result, err := apply(context.Background(), &command)
							require.NoError(t, err)
							require.True(t, result.Applied)
							if status == "completed" {
								require.InDelta(t, due, result.ActualAmountUSD, 1e-8)
								fixed := 0.0
								for _, allocation := range result.BillingAllocations {
									if allocation.Component == service.VideoImageInputBillingComponent {
										require.Equal(t, 1.0, allocation.RateMultiplier)
										fixed += allocation.AmountUSD
									}
								}
								wantFixed := 0.0
								if image {
									wantFixed = .15
								}
								require.InDelta(t, wantFixed, fixed, 1e-8)
							}
							// 多次完成观察和进程重启后的账务重放只能命中同一个结算键。
							for range 3 {
								fresh, err := f.tasks.Get(context.Background(), task.ID)
								require.NoError(t, err)
								require.Equal(t, wantStatus, fresh.BillingStatus)
								replayed, err := apply(context.Background(), &command)
								require.NoError(t, err)
								require.False(t, replayed.Applied)
							}
							videoProfileMoney(t, f, sub, due, 0, account)
						})
					}
				}
			}
		}
	}
}

// Token 可原子补扣；旧按秒合同实际费用超过预留时必须保留预算待核对。
func TestVideoProfileBillingTopUpAndSecondLimit(t *testing.T) {
	for _, profile := range []string{service.VideoOpenAIProfilePassthrough, service.VideoOpenAIProfileMetasoH3} {
		for _, image := range []bool{false, true} {
			for _, subscription := range []bool{false, true} {
				for _, mode := range []service.BillingMode{service.BillingModeVideo, service.BillingModeVideoToken} {
					t.Run(fmt.Sprintf("%s/image_%t/subscription_%t/%s", profile, image, subscription, mode), func(t *testing.T) {
						f := newVideoBillingFixture(t)
						f.quota(1000)
						var sub *service.UserSubscription
						if subscription {
							sub = fixedVideoSubscription(t, f, 1000, .5)
						}
						c := videoProfileBillingCase{profile: profile, image: image, mode: mode}
						task := f.reserve(videoProfileBillingTask(t, f, c))
						command, _ := videoProfileFinalCommand(t, f, task, "completed", 10, 300000)
						result, err := f.repo.CaptureBatchImageBalance(context.Background(), &command)
						if mode == service.BillingModeVideo {
							require.ErrorIs(t, err, service.ErrBatchImageSettlementCostExceedsHold)
							hold, _, _ := videoProfileExpected(c, subscription)
							videoProfileMoney(t, f, sub, hold, hold, 0)
							fresh, err := f.tasks.Get(context.Background(), task.ID)
							require.NoError(t, err)
							require.Equal(t, "reserved", fresh.BillingStatus)
							return
						}
						require.NoError(t, err)
						due, account := 18.0, 6.0
						if subscription {
							due = 3
						}
						if image {
							due, account = due+.15, account+.15
						}
						require.InDelta(t, due, result.ActualAmountUSD, 1e-8)
						videoProfileMoney(t, f, sub, due, 0, account)
					})
				}
			}
		}
	}
}

// 并行器把峰值连接限定为 64/128，但保留所有任务的重复执行和逐项错误。
func videoProfileParallel(count, limit int, run func(int) error) []error {
	errs := make([]error, count)
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			errs[i] = run(i)
		}(i)
	}
	wg.Wait()
	return errs
}

// 混合四象限请求共同争用同一付款人和上游账号，重放还补写真实使用记录。
func TestVideoProfileBillingConcurrentFundingAndProjection(t *testing.T) {
	videoStressConnectionPool(t)
	for _, subscription := range []bool{false, true} {
		t.Run(fmt.Sprintf("subscription_%t", subscription), func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(1000)
			var sub *service.UserSubscription
			if subscription {
				sub = fixedVideoSubscription(t, f, 1000, .5)
			}
			const count, copies = 128, 4
			tasks := make([]*service.VideoTaskRecord, count)
			cases := make([]videoProfileBillingCase, count)
			var held, due, account float64
			var completed int
			for i := range count {
				c := videoProfileModes()[(i/4)%4]
				c.profile, c.image = service.VideoOpenAIProfilePassthrough, i%2 == 1
				if i%4 >= 2 {
					c.profile = service.VideoOpenAIProfileMetasoH3
				}
				cases[i], tasks[i] = c, videoProfileBillingTask(t, f, c)
				h, d, a := videoProfileExpected(c, subscription)
				held += h
				if i%5 != 0 {
					completed++
					due, account = due+d, account+a
				}
			}
			errs := videoProfileParallel(count, 64, func(i int) error {
				_, err := f.repo.ReserveBatchImageBalance(context.Background(), &tasks[i].Hold)
				return err
			})
			for _, err := range errs {
				require.NoError(t, err)
			}
			videoProfileMoney(t, f, sub, held, held, 0)
			commands := make([]service.BatchImageBalanceHoldCommand, count)
			costs := make([]*service.CostBreakdown, count)
			for i := range count {
				var err error
				tasks[i], err = f.tasks.Get(context.Background(), tasks[i].ID)
				require.NoError(t, err)
				status := "completed"
				if i%5 == 0 {
					status = "failed"
				}
				commands[i], costs[i] = videoProfileFinalCommand(t, f, tasks[i], status, 5, 100000)
			}
			logs := newUsageLogRepositoryWithSQL(integrationEntClient, integrationDB)
			var captures, releases, inserted atomic.Int64
			errs = videoProfileParallel(count*copies, 128, func(n int) error {
				i := n % count
				command := commands[i]
				if i%5 == 0 {
					result, err := f.repo.ReleaseBatchImageBalance(context.Background(), &command)
					if err == nil && result.Applied {
						releases.Add(1)
					}
					return err
				}
				result, err := f.repo.CaptureBatchImageBalance(context.Background(), &command)
				if err != nil {
					return err
				}
				if result.Applied {
					captures.Add(1)
				}
				mode, media, resolution, seconds := string(cases[i].mode), "video", tasks[i].Metadata.Resolution, 5
				shownRate := 3.0
				if subscription {
					shownRate = .5
				}
				log := &service.UsageLog{UserID: f.userID, BillingUserID: f.userID, APIKeyID: f.keyID, GroupID: &f.groupID,
					AccountID: f.accountID, RequestID: command.RequestID, Model: "profile-video", RequestedModel: "profile-video",
					BillingMode: &mode, MediaType: &media, VideoCount: 1, VideoResolution: &resolution, VideoDurationSeconds: &seconds,
					OutputCost: costs[i].OutputCost, ImageInputCost: costs[i].ImageInputCost, TotalCost: costs[i].TotalCost,
					ActualCost: result.ActualAmountUSD, BalanceAmountUSD: result.BalanceAmountUSD, SubscriptionAmountUSD: result.SubscriptionAmountUSD,
					BillingAllocations: result.BillingAllocations, RateMultiplier: shownRate, AccountRateMultiplier: &tasks[i].AccountRateMultiplier,
					OutputTokens: 100000, CreatedAt: time.Now()}
				if sub != nil {
					log.BillingType, log.SubscriptionID = 1, &sub.ID
				}
				created, err := logs.Create(context.Background(), log)
				if created {
					inserted.Add(1)
				}
				return err
			})
			for _, err := range errs {
				require.NoError(t, err)
			}
			require.EqualValues(t, completed, captures.Load())
			require.EqualValues(t, count-completed, releases.Load())
			require.EqualValues(t, completed, inserted.Load())
			videoProfileMoney(t, f, sub, due, 0, account)
			rows, _, err := logs.ListWithFilters(context.Background(), pagination.PaginationParams{Page: 1, PageSize: count}, UsageLogFilters{UserID: f.userID})
			require.NoError(t, err)
			require.Len(t, rows, completed)
			var shownCost float64
			for _, row := range rows {
				require.NotNil(t, row.VideoBilling)
				require.Equal(t, 100000, row.OutputTokens)
				require.NotNil(t, row.AccountRateMultiplier)
				require.Equal(t, 1.0, *row.AccountRateMultiplier)
				require.NotNil(t, row.VideoDurationSeconds)
				require.Equal(t, 5, *row.VideoDurationSeconds)
				if subscription {
					require.Equal(t, .5, row.RateMultiplier)
					require.Equal(t, sub.ID, *row.SubscriptionID)
				} else {
					require.Equal(t, 3.0, row.RateMultiplier)
				}
				shownCost += row.ActualCost
				if row.ImageInputCost > 0 {
					require.InDelta(t, .15, row.ImageInputCost, 1e-8)
					require.Equal(t, 1, *row.VideoBilling.ReferenceImageCount)
					require.InDelta(t, .15, *row.VideoBilling.ReferenceImageCost, 1e-8)
				}
			}
			require.InDelta(t, due, shownCost, 1e-8)
			t.Logf("四象限 × 四种预留合同：%d 任务，64 并发预扣、128 并发执行 %d 次结算/释放；实际捕获 %d、释放 %d、使用记录 %d，用户金额 %.8f，账号成本 %.8f", count, count*copies, captures.Load(), releases.Load(), inserted.Load(), due, account)
		})
	}
}
