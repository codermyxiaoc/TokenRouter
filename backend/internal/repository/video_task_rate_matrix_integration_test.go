//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 三种视频计价及两种 Token 资金时序共用真实报价器和 PostgreSQL，逐项验证倍率、固定费和账本归属。
func TestVideoTaskBillingRateAndFundingMatrix(t *testing.T) {
	paths := []struct {
		name   string
		mode   service.BillingMode
		prepay bool
	}{
		{"seconds", service.BillingModeVideo, false},
		{"per_request", service.BillingModeVideoPerRequest, false},
		{"token_deferred", service.BillingModeVideoToken, false},
		{"token_prepay", service.BillingModeVideoToken, true},
	}
	rates := []struct {
		name        string
		inherit     bool
		independent bool
		rate        float64
	}{
		{name: "plan_override", rate: .5},
		{name: "plan_inherits_group", inherit: true, rate: 3},
		{name: "independent_overrides_plan", independent: true, rate: .38},
		{name: "independent_zero_keeps_fixed_fee", independent: true, rate: 0},
	}
	for _, path := range paths {
		for _, rate := range rates {
			for _, funding := range []string{service.APIKeyBillingModeAuto, service.APIKeyBillingModeSubscription, service.APIKeyBillingModeBalance} {
				t.Run(fmt.Sprintf("%s/%s/%s", path.name, rate.name, funding), func(t *testing.T) {
					ctx := context.Background()
					f := newVideoBillingFixture(t)
					f.quota(1000)
					sub := fixedVideoSubscription(t, f, 100, .5)
					if rate.inherit {
						_, err := integrationDB.ExecContext(ctx, `UPDATE subscription_plan_groups SET rate_multiplier=NULL WHERE plan_id=$1 AND group_id=$2`, sub.PlanID, f.groupID)
						require.NoError(t, err)
					}

					// 渠道/分组价卡倍率先折入基础单价，套餐只覆盖分组倍率；参考图费始终按原价计算。
					price, cardRate, imagePrice, prepayPrice, images := 1.0, 2.0, .15, .3, 7
					if path.mode == service.BillingModeVideoPerRequest {
						price = 2
					}
					pricing := service.ChannelModelPricing{Platform: service.PlatformVideo, Models: []string{"rate-matrix-video"},
						BillingMode: path.mode, PriceMultiplier: &cardRate,
						VideoPrices:            []service.VideoPriceTier{{Resolution: "720p", Price: &price}},
						VideoImageInputPricing: &service.VideoImageInputPricing{FreeImages: 5, Price: &imagePrice}}
					if path.prepay {
						pricing.VideoTokenPrepay = &service.VideoTokenPrepayConfig{PricePerSecond: &prepayPrice}
					}
					group := &service.Group{ID: f.groupID, Platform: service.PlatformVideo, RateMultiplier: 3,
						VideoRateIndependent: rate.independent, VideoRateMultiplier: rate.rate,
						ModelPricing: []service.ChannelModelPricing{pricing}}
					quote, err := service.NewModelPricingResolver(nil, nil).QuoteVideo(ctx, service.VideoPriceInput{
						Group: group, Model: "rate-matrix-video", Resolution: "720p", HasReferenceVideo: true,
						ReferenceImageCount: &images, RateMultiplier: 4,
					})
					require.NoError(t, err)
					tokens := int64(2_000_000)
					actual, err := quote.Calculate(2, &tokens)
					require.NoError(t, err)
					require.Equal(t, 4.0, actual.OutputCost)
					require.InDelta(t, .3, actual.ImageInputCost, 1e-10)

					subRate, balanceRate := 3.0, 4.0
					if rate.independent {
						subRate, balanceRate = rate.rate, rate.rate
					}
					base := 16.0
					if path.mode == service.BillingModeVideoPerRequest {
						base = 4
					} else if path.mode == service.BillingModeVideoToken {
						base = 0
						if path.prepay {
							base = 8 * prepayPrice
						}
					}
					task := f.task(base, subRate, balanceRate)
					task.Quote = quote
					task.Hold.APIKeyBillingMode = funding
					if funding == service.APIKeyBillingModeSubscription {
						task.Hold.PreferredSubscriptionID = &sub.ID
					}
					task.Hold.DisablePlanGroupRateMultiplier = rate.independent
					task.Hold.VideoFixedAmountUSD = actual.ImageInputCost
					task.Hold.VideoDeferredBilling = path.mode == service.BillingModeVideoToken && !path.prepay
					task.Hold.VideoTokenPrepay = path.prepay
					if path.prepay {
						task.Hold.VideoPrepayDurationSeconds = 8
					}
					require.NoError(t, f.tasks.Save(ctx, task, false))
					task = f.reserve(task)

					effectiveRate := rate.rate
					if funding == service.APIKeyBillingModeBalance {
						effectiveRate = balanceRate
					}
					wantHold := base*effectiveRate + .3
					if path.prepay {
						wantHold = 2.7
					} else if task.Hold.VideoDeferredBilling {
						wantHold = 0
					}
					require.InDelta(t, wantHold, task.Hold.HoldAmount, 1e-8)
					wantHoldBalance := 0.0
					if funding == service.APIKeyBillingModeBalance {
						wantHoldBalance = wantHold
					}
					f.money(1000-wantHoldBalance, wantHoldBalance, wantHold, 0)
					fixedVideoAssertSubscription(t, sub.ID, wantHold-wantHoldBalance)
					f.quotaUsage(wantHoldBalance, wantHoldBalance, wantHoldBalance)

					command := f.command(task, "completed", actual.OutputCost)
					command.VideoActualFixedAmountUSD = actual.ImageInputCost
					command.VideoAccountQuotaCost = actual.TotalCost * 1.5
					result, err := f.repo.CaptureBatchImageBalance(ctx, &command)
					require.NoError(t, err)
					require.True(t, result.Applied)
					want := 4*effectiveRate + .3
					wantBalance := 0.0
					if funding == service.APIKeyBillingModeBalance {
						wantBalance = want
					}
					require.InDelta(t, want, result.ActualAmountUSD, 1e-8)
					require.InDelta(t, wantBalance, result.BalanceAmountUSD, 1e-8)
					require.InDelta(t, want-wantBalance, result.SubscriptionAmountUSD, 1e-8)
					for _, allocation := range result.BillingAllocations {
						if allocation.Component == service.VideoImageInputBillingComponent {
							require.Equal(t, 1.0, allocation.RateMultiplier)
							require.InDelta(t, .3, allocation.AmountUSD, 1e-8)
						} else {
							require.Equal(t, effectiveRate, allocation.RateMultiplier)
						}
					}
					repeat, err := f.repo.CaptureBatchImageBalance(ctx, &command)
					require.NoError(t, err)
					require.False(t, repeat.Applied)
					require.Equal(t, result.BillingAllocations, repeat.BillingAllocations)
					f.money(1000-wantBalance, 0, want, 6.45)
					fixedVideoAssertSubscription(t, sub.ID, want-wantBalance)
					f.quotaUsage(wantBalance, wantBalance, wantBalance)
					var used5h, used1d, used7d float64
					require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT usage_5h,usage_1d,usage_7d FROM api_keys WHERE id=$1`, f.keyID).Scan(&used5h, &used1d, &used7d))
					for _, used := range []float64{used5h, used1d, used7d} {
						require.InDelta(t, want, used, 1e-8)
					}
					var captures int
					require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1 AND request_id=$2`, f.keyID, command.RequestID).Scan(&captures))
					require.Equal(t, 1, captures)
				})
			}
		}
	}
}
