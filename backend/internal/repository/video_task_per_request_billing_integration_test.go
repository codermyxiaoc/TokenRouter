//go:build integration

package repository

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 按次价卡走真实渠道仓储往返，验证既有列长度、矩阵及零价无需改写旧秒价。
func TestVideoPerRequestChannelPricingRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewChannelRepository(integrationDB)
	price, free, fallback, imagePrice := 2.125, 0.0, 1.75, .15
	channel := &service.Channel{
		Name: "video-per-request-" + uuid.NewString(), Status: service.StatusActive,
		BillingModelSource: "requested",
		ModelPricing: []service.ChannelModelPricing{
			{Platform: service.PlatformVideo, Models: []string{"request-video"}, BillingMode: service.BillingModeVideoPerRequest,
				VideoPrices:        []service.VideoPriceTier{{Resolution: "720p", Price: &price}, {Resolution: "480p", HasReferenceVideo: true, Price: &free}},
				VideoFallbackPrice: &fallback, VideoImageInputPricing: &service.VideoImageInputPricing{FreeImages: 5, Price: &imagePrice}},
			{Platform: service.PlatformVideo, Models: []string{"legacy-seconds"}, BillingMode: service.BillingModeVideo,
				VideoPrices: []service.VideoPriceTier{{Resolution: "720p", Price: &price}}},
		},
	}
	require.NoError(t, repo.Create(ctx, channel))
	t.Cleanup(func() { require.NoError(t, repo.Delete(context.Background(), channel.ID)) })
	loaded, err := repo.GetByID(ctx, channel.ID)
	require.NoError(t, err)
	require.Len(t, loaded.ModelPricing, 2)
	pricing := loaded.ModelPricing[0]
	require.Equal(t, service.BillingModeVideoPerRequest, pricing.BillingMode)
	require.Len(t, pricing.VideoPrices, 2)
	require.Equal(t, price, *pricing.VideoPrices[0].Price)
	require.True(t, pricing.VideoPrices[1].HasReferenceVideo)
	require.NotNil(t, pricing.VideoPrices[1].Price)
	require.Zero(t, *pricing.VideoPrices[1].Price)
	require.Equal(t, fallback, *pricing.VideoFallbackPrice)
	require.Equal(t, 5, pricing.VideoImageInputPricing.FreeImages)
	require.Equal(t, imagePrice, *pricing.VideoImageInputPricing.Price)
	require.Nil(t, pricing.VideoTokenPrepay)
	require.Equal(t, service.BillingModeVideo, loaded.ModelPricing[1].BillingMode)
	require.Equal(t, price, *loaded.ModelPricing[1].VideoPrices[0].Price)

	// 更新同一条价卡仍保留按次模式；清空图片费不能残留上一版配置。
	pricing.VideoImageInputPricing = nil
	pricing.VideoFallbackPrice = &free
	require.NoError(t, repo.UpdateModelPricing(ctx, &pricing))
	entries, err := repo.ListModelPricing(ctx, channel.ID)
	require.NoError(t, err)
	require.Equal(t, service.BillingModeVideoPerRequest, entries[0].BillingMode)
	require.NotNil(t, entries[0].VideoFallbackPrice)
	require.Zero(t, *entries[0].VideoFallbackPrice)
	require.Nil(t, entries[0].VideoImageInputPricing)
}

// 从正式报价器生成固定次价和图片费，持久化后再由账本使用，避免手写金额掩盖模式接线问题。
func perRequestVideoBillingTask(t *testing.T, f *videoBillingFixture) (*service.VideoTaskRecord, *service.CostBreakdown) {
	t.Helper()
	ctx := context.Background()
	price, imagePrice, imageCount := 2.125, .15, 7
	group := &service.Group{ID: f.groupID, Platform: service.PlatformVideo, RateMultiplier: 3,
		ModelPricing: []service.ChannelModelPricing{{Platform: service.PlatformVideo, Models: []string{"request-video"},
			BillingMode: service.BillingModeVideoPerRequest, VideoFallbackPrice: &price,
			VideoImageInputPricing: &service.VideoImageInputPricing{FreeImages: 5, Price: &imagePrice}}}}
	quote, err := service.NewModelPricingResolver(nil, nil).QuoteVideo(ctx, service.VideoPriceInput{
		Group: group, Model: "request-video", ReferenceImageCount: &imageCount, RateMultiplier: 3,
	})
	require.NoError(t, err)
	// 没有分辨率的统一次价和未知自动时长仍是一次，不依赖秒数或 Token 数量。
	metadata := service.VideoRequestMetadata{DurationSeconds: -1, ReferenceImageCount: &imageCount}
	cost, err := quote.Calculate(metadata.DurationSeconds, nil)
	require.NoError(t, err)
	require.InDelta(t, 2.125, cost.OutputCost, 1e-10)
	require.InDelta(t, .30, cost.ImageInputCost, 1e-10)
	require.InDelta(t, 6.675, cost.ActualCost, 1e-10)
	task := f.task(cost.OutputCost, quote.RateMultiplier, quote.RateMultiplier)
	task.Quote, task.Metadata = quote, metadata
	task.Hold.VideoFixedAmountUSD = cost.ImageInputCost
	require.NoError(t, f.tasks.Save(ctx, task, false))
	price, imagePrice = 99, 99
	stored, err := f.tasks.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, service.BillingModeVideoPerRequest, stored.Quote.Mode)
	require.Equal(t, "request", stored.Quote.Unit)
	restoredCost, err := stored.Quote.Calculate(-1, nil)
	require.NoError(t, err)
	require.Equal(t, cost, restoredCost, "配置变更不能污染已经保存的次价与参考图单价")
	return stored, cost
}

// 同一按次任务的多个完成观察者只能扣一次；实际 PostgreSQL 同时验证固定图费不乘倍率。
func TestVideoTaskPerRequestBillingConcurrentCaptureAndUsage(t *testing.T) {
	f := newVideoBillingFixture(t)
	f.quota(100)
	task, cost := perRequestVideoBillingTask(t, f)
	task = f.reserve(task)
	f.money(1000-cost.ActualCost, cost.ActualCost, cost.ActualCost, 0)
	command := f.command(task, "completed", cost.OutputCost)
	command.VideoActualFixedAmountUSD = cost.ImageInputCost
	command.VideoAccountQuotaCost = cost.TotalCost
	var wg sync.WaitGroup
	var applied atomic.Int64
	errs := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := command
			result, err := f.repo.CaptureBatchImageBalance(context.Background(), &local)
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
	f.money(1000-cost.ActualCost, 0, cost.ActualCost, cost.TotalCost)
	f.quotaUsage(cost.ActualCost, cost.ActualCost, cost.ActualCost)
	stored, err := f.tasks.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "settled", stored.BillingStatus)
	require.InDelta(t, cost.ActualCost, stored.BillingResult.ActualAmountUSD, 1e-8)

	// 写入正式 usage_logs 并按新模式筛选，确保缺少时长和分辨率不会丢失按次账单详情。
	logs := newUsageLogRepositoryWithSQL(integrationEntClient, integrationDB)
	mode := string(service.BillingModeVideoPerRequest)
	log := &service.UsageLog{UserID: f.userID, APIKeyID: f.keyID, AccountID: f.accountID, GroupID: &f.groupID,
		RequestID: command.RequestID, Model: "request-video", BillingMode: &mode, VideoCount: 1,
		OutputCost: cost.OutputCost, ImageInputCost: cost.ImageInputCost, TotalCost: cost.TotalCost,
		ActualCost: cost.ActualCost, BalanceAmountUSD: cost.ActualCost, RateMultiplier: 3, CreatedAt: time.Now()}
	_, err = logs.Create(context.Background(), log)
	require.NoError(t, err)
	rows, _, err := logs.ListWithFilters(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 10},
		UsageLogFilters{UserID: f.userID, BillingMode: mode})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, mode, *rows[0].BillingMode)
	require.NotNil(t, rows[0].VideoBilling)
	require.Equal(t, "request", rows[0].VideoBilling.Unit)
	require.InDelta(t, 2.125, rows[0].VideoBilling.UnitPrice, 1e-10)
	require.Equal(t, 7, *rows[0].VideoBilling.ReferenceImageCount)
	require.Equal(t, 2, *rows[0].VideoBilling.BillableReferenceImageCount)
	require.InDelta(t, .30, *rows[0].VideoBilling.ReferenceImageCost, 1e-10)
	require.Nil(t, rows[0].VideoDurationSeconds)
}

// 失败或确认取消的按次任务退回整个固定费用，重复释放不影响余额与各额度窗口。
func TestVideoTaskPerRequestBillingFailureAndCancelRelease(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			f.quota(100)
			task, cost := perRequestVideoBillingTask(t, f)
			task = f.reserve(task)
			f.money(1000-cost.ActualCost, cost.ActualCost, cost.ActualCost, 0)
			command := f.command(task, status, 0)
			var wg sync.WaitGroup
			var applied atomic.Int64
			errs := make(chan error, 16)
			for range 16 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					local := command
					result, err := f.repo.ReleaseBatchImageBalance(context.Background(), &local)
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
			f.money(1000, 0, 0, 0)
			f.quotaUsage(0, 0, 0)
			stored, err := f.tasks.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.Equal(t, "released", stored.BillingStatus)
		})
	}
}
