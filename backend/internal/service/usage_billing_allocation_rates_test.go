package service

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// 覆盖共享/独立媒体倍率、免费价和高峰组合，防止从订阅展示金额推导余额价格。
func TestResolveUsageBillingAllocationRates(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		mode, media          BillingMode
		group                *Group
		balance, peak        float64
		wantSub, wantBalance float64
		wantScale            float64
		wantDisablePlan      bool
	}{
		{name: "共享图片无高峰", mode: BillingModeImage, media: BillingModeImage, balance: 1, peak: 3, wantSub: 4, wantBalance: 1, wantScale: 1},
		{name: "图片按次无高峰", mode: BillingModePerRequest, media: BillingModeImage, balance: 1, peak: 3, wantSub: 4, wantBalance: 1, wantScale: 1},
		{name: "共享图片余额免费", mode: BillingModeImage, media: BillingModeImage, balance: 0, peak: 3, wantSub: 4, wantBalance: 0, wantScale: 1},
		{name: "独立图片", mode: BillingModeImage, media: BillingModeImage, group: &Group{ImageRateIndependent: true, ImageRateMultiplier: 0.5}, balance: 1, peak: 3, wantSub: 0.5, wantBalance: 0.5, wantScale: 1, wantDisablePlan: true},
		{name: "独立图片免费", mode: BillingModeImage, media: BillingModeImage, group: &Group{ImageRateIndependent: true}, balance: 1, peak: 3, wantScale: 1, wantDisablePlan: true},
		{name: "图片令牌不套独立图片倍率", mode: BillingModeToken, media: BillingModeImage, group: &Group{ImageRateIndependent: true, ImageRateMultiplier: 0.5}, balance: 1, peak: 3, wantSub: 12, wantBalance: 3, wantScale: 3},
		{name: "共享视频无高峰", mode: BillingModeVideo, media: BillingModeVideo, balance: 1, peak: 3, wantSub: 4, wantBalance: 1, wantScale: 1},
		{name: "独立视频", mode: BillingModeVideo, media: BillingModeVideo, group: &Group{VideoRateIndependent: true, VideoRateMultiplier: 0.25}, balance: 1, peak: 3, wantSub: 0.25, wantBalance: 0.25, wantScale: 1, wantDisablePlan: true},
		{name: "视频令牌保留高峰", mode: BillingModeToken, media: BillingModeVideo, group: &Group{VideoRateIndependent: true, VideoRateMultiplier: 0.25}, balance: 1, peak: 3, wantSub: 12, wantBalance: 3, wantScale: 3},
		{name: "搜索及音频无高峰", mode: BillingModePerRequest, media: BillingModePerRequest, balance: 1, peak: 3, wantSub: 4, wantBalance: 1, wantScale: 1},
		{name: "普通令牌", mode: BillingModeToken, balance: 1, peak: 3, wantSub: 12, wantBalance: 3, wantScale: 3},
		{name: "普通令牌余额免费", mode: BillingModeToken, balance: 0, peak: 3, wantSub: 12, wantBalance: 0, wantScale: 3},
		{name: "免费高峰不能恢复套餐价格", mode: BillingModeToken, balance: 1, peak: 0, wantDisablePlan: true},
		{name: "图片不受免费高峰影响", mode: BillingModeImage, media: BillingModeImage, balance: 1, peak: 0, wantSub: 4, wantBalance: 1, wantScale: 1},
		{name: "普通按次保留既有倍率因子", mode: BillingModePerRequest, balance: 1, peak: 3, wantSub: 12, wantBalance: 3, wantScale: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rates := resolveUsageBillingAllocationRates(&APIKey{Group: tc.group}, string(tc.mode), tc.media, 4, tc.balance, tc.peak)
			require.InDelta(t, tc.wantSub, rates.SubscriptionRateMultiplier, 1e-12)
			require.InDelta(t, tc.wantBalance, rates.BalanceRateMultiplier, 1e-12)
			require.InDelta(t, tc.wantScale, rates.SubscriptionRateMultiplierScale, 1e-12)
			require.Equal(t, tc.wantDisablePlan, rates.DisablePlanGroupRateMultiplier)
			cmd := buildUsageBillingCommand("funding-rates", nil, &usageBillingParams{
				User: &User{ID: 1}, APIKey: &APIKey{ID: 2}, Account: &Account{ID: 3},
				Cost:                       &CostBreakdown{TotalCost: 0.12, ActualCost: 0.72, BillingMode: string(tc.mode)},
				SubscriptionRateMultiplier: rates.SubscriptionRateMultiplier, SubscriptionRateMultiplierScale: rates.SubscriptionRateMultiplierScale,
				BalanceRateMultiplier: rates.BalanceRateMultiplier, RateMultipliersResolved: true, DisablePlanGroupRateMultiplier: rates.DisablePlanGroupRateMultiplier,
			})
			require.Equal(t, rates.SubscriptionRateMultiplier, cmd.SubscriptionRateMultiplier)
			require.Equal(t, rates.BalanceRateMultiplier, cmd.BalanceRateMultiplier)
			require.Equal(t, rates.DisablePlanGroupRateMultiplier, cmd.DisablePlanGroupRateMultiplier)
		})
	}
}

// 同时覆盖两条普通网关入口，订阅存在时仍把真实余额倍率传入事务，并保留图片独立免费价。
func TestRecordUsageImageFundingRatesStaySeparate(t *testing.T) {
	for _, gateway := range []string{"openai", "gateway"} {
		for _, tc := range []struct {
			name                 string
			userRate             float64
			independent          bool
			imageRate            float64
			wantSub, wantBalance float64
			wantQuotedCost       float64
		}{
			{name: "共享图片订阅六倍余额一倍", userRate: 1, wantSub: 4, wantBalance: 1, wantQuotedCost: 0.72},
			{name: "共享图片余额免费", userRate: 0, wantSub: 4, wantBalance: 0, wantQuotedCost: 0.72},
			{name: "独立图片半倍", userRate: 1, independent: true, imageRate: 0.5, wantSub: 0.5, wantBalance: 0.5, wantQuotedCost: 0.06},
			{name: "独立图片免费", userRate: 1, independent: true, wantSub: 0, wantBalance: 0, wantQuotedCost: 0},
		} {
			t.Run(gateway+"/"+tc.name, func(t *testing.T) {
				groupID := int64(123)
				price := 0.12
				key := &APIKey{ID: 456, GroupID: &groupID, BillingMode: APIKeyBillingModeAuto, Group: &Group{
					ID: groupID, RateMultiplier: 4, ImagePrice1K: &price,
					ImageRateIndependent: tc.independent, ImageRateMultiplier: tc.imageRate,
					PeakRateEnabled: true, PeakStart: "00:00", PeakEnd: "23:59", PeakRateMultiplier: 3,
				}}
				sub := &UserSubscription{ID: 789, Plan: &SubscriptionPlan{ID: 999, GroupIDs: []int64{groupID}, GroupRateMultipliers: map[int64]float64{groupID: 6}}}
				rateRepo := &openAIUserGroupRateRepoStub{rate: &tc.userRate}
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				billRepo := &openAIRecordUsageBillingRepoStub{}
				at := time.Date(2026, 10, 5, 12, 0, 0, 0, timezone.Location())
				if gateway == "openai" {
					svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, rateRepo)
					svc.usageBillingNow = func() time.Time { return at }
					require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
						Result: &OpenAIForwardResult{RequestID: "funding-image", Model: "gpt-image-2", ImageCount: 1, ImageSize: "1K"},
						APIKey: key, User: &User{ID: 42}, Account: &Account{ID: 7}, Subscription: sub,
					}))
				} else {
					svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
					svc.userGroupRateResolver = newUserGroupRateResolver(rateRepo, nil, time.Minute, nil, "test")
					svc.usageBillingNow = func() time.Time { return at }
					require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
						Result: &ForwardResult{RequestID: "funding-image", Model: "gemini-3-pro-image-preview", ImageCount: 1, ImageSize: "1K"},
						APIKey: key, User: &User{ID: 42}, Account: &Account{ID: 7}, Subscription: sub,
					}))
				}
				require.Equal(t, 1, rateRepo.calls)
				require.NotNil(t, billRepo.lastCmd)
				require.InDelta(t, 0.12, billRepo.lastCmd.BaseAmountUSD, 1e-12)
				require.InDelta(t, tc.wantQuotedCost, billRepo.lastCmd.BillableAmountUSD, 1e-12)
				require.InDelta(t, tc.wantSub, billRepo.lastCmd.SubscriptionRateMultiplier, 1e-12)
				require.InDelta(t, tc.wantBalance, billRepo.lastCmd.BalanceRateMultiplier, 1e-12)
				require.Equal(t, 1.0, billRepo.lastCmd.SubscriptionRateMultiplierScale)
				require.Equal(t, tc.independent, billRepo.lastCmd.DisablePlanGroupRateMultiplier)
			})
		}
	}
}

// 真实成本分支必须按最终模式选择倍率，不能因为输出是图片就给令牌价格套独立媒体倍率。
func TestOpenAIRecordUsageFundingRatesUseFinalBillingMode(t *testing.T) {
	for _, tc := range []struct {
		name, model                 string
		image, video, search, token bool
		independent                 bool
		peak                        float64
		wantMode                    BillingMode
		wantSub, wantBalance, scale float64
		wantDisablePlan             bool
	}{
		{name: "图片令牌", model: "gpt-image-2", image: true, token: true, independent: true, peak: 3, wantMode: BillingModeToken, wantSub: 12, wantBalance: 3, scale: 3},
		{name: "视频共享", model: "grok-imagine-video", video: true, peak: 3, wantMode: BillingModeVideo, wantSub: 4, wantBalance: 1, scale: 1},
		{name: "视频独立", model: "grok-imagine-video", video: true, independent: true, peak: 3, wantMode: BillingModeVideo, wantSub: 0.5, wantBalance: 0.5, scale: 1, wantDisablePlan: true},
		{name: "视频令牌", model: "grok-imagine-video", video: true, token: true, independent: true, peak: 3, wantMode: BillingModeToken, wantSub: 12, wantBalance: 3, scale: 3},
		{name: "独立搜索无高峰", model: "web-search", search: true, peak: 3, wantMode: BillingModePerRequest, wantSub: 4, wantBalance: 1, scale: 1},
		{name: "普通令牌高峰", model: "gpt-5.1", peak: 3, wantMode: BillingModeToken, wantSub: 12, wantBalance: 3, scale: 3},
		{name: "普通令牌免费高峰", model: "gpt-5.1", peak: 0, wantMode: BillingModeToken, wantDisablePlan: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groupID := int64(808)
			balanceRate := 1.0
			videoPrice := 0.12
			rateRepo := &openAIUserGroupRateRepoStub{rate: &balanceRate}
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, rateRepo)
			if tc.token {
				svc.resolver = newOpenAITokenImageChannelPricingResolverForTest(t, groupID, tc.model)
			}
			svc.usageBillingNow = func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, timezone.Location()) }
			result := &OpenAIForwardResult{RequestID: "mode-funding-" + tc.model, Model: tc.model, BillingModel: tc.model, Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 200}}
			if tc.image {
				result.ImageCount, result.ImageSize = 1, "1K"
			}
			if tc.video {
				result.VideoCount, result.VideoDurationSeconds, result.VideoResolution = 1, 5, VideoBillingResolution720P
			}
			if tc.search {
				result.WebSearchCalls = 1
			}
			key := &APIKey{ID: 809, GroupID: &groupID, Group: &Group{
				ID: groupID, RateMultiplier: 4, VideoPrice720P: &videoPrice,
				ImageRateIndependent: tc.independent, ImageRateMultiplier: 0.5,
				VideoRateIndependent: tc.independent, VideoRateMultiplier: 0.5,
				PeakRateEnabled: true, PeakStart: "00:00", PeakEnd: "23:59", PeakRateMultiplier: tc.peak,
			}}
			sub := &UserSubscription{ID: 810, Plan: &SubscriptionPlan{ID: 811, GroupIDs: []int64{groupID}, GroupRateMultipliers: map[int64]float64{groupID: 6}}}
			require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: key, User: &User{ID: 812}, Account: &Account{ID: 813}, Subscription: sub}))
			cmd := requireOpenAIRecordUsageBillingRepoStub(t, svc).lastCmd
			require.NotNil(t, cmd)
			require.NotNil(t, usageRepo.lastLog.BillingMode)
			require.Equal(t, string(tc.wantMode), *usageRepo.lastLog.BillingMode)
			require.InDelta(t, tc.wantSub, cmd.SubscriptionRateMultiplier, 1e-12)
			require.InDelta(t, tc.wantBalance, cmd.BalanceRateMultiplier, 1e-12)
			require.InDelta(t, tc.scale, cmd.SubscriptionRateMultiplierScale, 1e-12)
			require.Equal(t, tc.wantDisablePlan, cmd.DisablePlanGroupRateMultiplier)
		})
	}
}

// 只改变套餐覆盖规则会改变实际账务，必须拒绝以同一请求指纹重放另一种规则。
func TestUsageBillingFingerprintIncludesDisabledPlanRate(t *testing.T) {
	base := UsageBillingCommand{RequestID: "image-price", APIKeyID: 1, UserID: 2, APIKeyBillingMode: APIKeyBillingModeAuto, BaseAmountUSD: 0.12, SubscriptionRateMultiplier: 1, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 1}
	original := buildUsageBillingFingerprint(&base)
	same := base
	same.DisablePlanGroupRateMultiplier = false
	require.Equal(t, original, buildUsageBillingFingerprint(&same))
	same.DisablePlanGroupRateMultiplier = true
	require.NotEqual(t, original, buildUsageBillingFingerprint(&same))
	same.Normalize()
	first := same.RequestFingerprint
	same.Normalize()
	require.Equal(t, first, same.RequestFingerprint)
}

// 捕获通知以数据库最终余额和实际净消费计算阈值，不读受理时旧余额或整笔预占金额。
func TestResolveOldBalanceImageCaptureUsesFinalNetCost(t *testing.T) {
	final := 0.88
	old := resolveOldBalance(&usageBillingParams{User: &User{Balance: 100}, ImageReservation: &ImageBillingReservation{ID: "reserved"}}, &UsageBillingApplyResult{NewBalance: &final, BalanceAmountUSD: 0.12})
	require.InDelta(t, 1, old, 1e-12)
}
