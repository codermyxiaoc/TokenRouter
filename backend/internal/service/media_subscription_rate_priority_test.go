//go:build unit

package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// 图片与视频分别控制优先级，仅当前订阅对最终分组的有效覆盖可以替换独立倍率。
func TestMediaSubscriptionRatePriorityFallbacks(t *testing.T) {
	for _, media := range []BillingMode{BillingModeImage, BillingModeVideo} {
		for _, tc := range []struct {
			name                  string
			priority, independent bool
			planRate, mediaRate   float64
			otherGroup, balance   bool
			want                  float64
		}{
			{name: "关闭保留独立倍率", independent: true, planRate: 2.4, mediaRate: 1, want: 1},
			{name: "开启优先套餐", priority: true, independent: true, planRate: 2.4, mediaRate: 1, want: 2.4},
			{name: "缺少覆盖回退独立", priority: true, independent: true, mediaRate: 1, want: 1},
			{name: "负覆盖回退独立", priority: true, independent: true, planRate: -1, mediaRate: 1, want: 1},
			{name: "无限覆盖回退独立", priority: true, independent: true, planRate: math.Inf(1), mediaRate: 1, want: 1},
			{name: "非数覆盖回退独立", priority: true, independent: true, planRate: math.NaN(), mediaRate: 1, want: 1},
			{name: "其它分组不覆盖", priority: true, independent: true, planRate: 2.4, mediaRate: 1, otherGroup: true, want: 1},
			{name: "余额保持独立", priority: true, independent: true, planRate: 2.4, mediaRate: 1, balance: true, want: 1},
			{name: "独立免费仍可优先套餐", priority: true, independent: true, planRate: 2.4, want: 2.4},
			{name: "无覆盖保留独立免费", priority: true, independent: true, want: 0},
			{name: "未独立保持有效组倍率", priority: true, planRate: 2.4, mediaRate: 1, want: 7},
		} {
			t.Run(string(media)+"/"+tc.name, func(t *testing.T) {
				group := &Group{ID: 62, RateMultiplier: .3,
					ImageRateIndependent: tc.independent, ImageRateMultiplier: tc.mediaRate, ImageSubscriptionRatePriority: tc.priority,
					VideoRateIndependent: tc.independent, VideoRateMultiplier: tc.mediaRate, VideoSubscriptionRatePriority: tc.priority}
				key := &APIKey{Group: group, GroupID: &group.ID, BillingMode: APIKeyBillingModeAuto}
				if tc.balance {
					key.BillingMode = APIKeyBillingModeBalance
				}
				planGroup := group.ID
				if tc.otherGroup {
					planGroup++
				}
				sub := &UserSubscription{Plan: &SubscriptionPlan{GroupIDs: []int64{planGroup}, GroupRateMultipliers: map[int64]float64{planGroup: tc.planRate}}}
				resolve := resolveImageSubscriptionRateMultiplier
				if media == BillingModeVideo {
					resolve = resolveVideoSubscriptionRateMultiplier
				}
				require.InDelta(t, tc.want, resolve(key, sub, 7), 1e-12)
				if tc.independent {
					require.Equal(t, tc.mediaRate, resolve(key, nil, 7))
				}
			})
		}
	}
}

// 真实 Grok 报价冻结套餐优先结果，后续修改开关或套餐不能改变已保存扣费命令。
func TestLegacyVideoQuoteFreezesSubscriptionPriority(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		priority              bool
		planRate, independent float64
		want                  float64
	}{
		{"旧默认仍为一倍", false, 2.4, 1, 1},
		{"优先套餐二点四倍", true, 2.4, 1, 2.4},
		{"未配置套餐回退独立", true, 0, 1, 1},
		{"独立免费允许套餐覆盖", true, 2.4, 0, 2.4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			price := .2
			group := &Group{ID: 62, Platform: PlatformGrok, RateMultiplier: .3, VideoPrice720P: &price,
				VideoRateIndependent: true, VideoRateMultiplier: tc.independent, VideoSubscriptionRatePriority: tc.priority}
			sub := &UserSubscription{ID: 2105, UserID: 1, Plan: &SubscriptionPlan{ID: 7, GroupIDs: []int64{62}, GroupRateMultipliers: map[int64]float64{62: tc.planRate}}}
			key := &APIKey{ID: 2, UserID: 1, GroupID: &group.ID, Group: group, BillingMode: APIKeyBillingModeAuto}
			input := &OpenAIRecordUsageInput{PrepareLegacyVideoQuote: true, LegacyVideoTaskID: 240, SubscriptionScopeID: &sub.ID,
				Result: &OpenAIForwardResult{RequestID: "grok-video:priority", Model: "grok-imagine-video-1.5", VideoCount: 1, VideoResolution: "720p", VideoDurationSeconds: 5},
				APIKey: key, User: &User{ID: 1}, Account: &Account{ID: 9, Platform: PlatformGrok}, Subscription: sub}
			require.NoError(t, svc.RecordUsage(context.Background(), input))
			encoded, err := json.Marshal(input.LegacyVideoQuote)
			require.NoError(t, err)
			group.VideoSubscriptionRatePriority, group.VideoRateMultiplier = !tc.priority, 99
			sub.Plan.GroupRateMultipliers[62] = 88
			var quote LegacyVideoBillingQuote
			require.NoError(t, json.Unmarshal(encoded, &quote))
			require.InDelta(t, 1, quote.Command.BaseAmountUSD, 1e-12)
			require.InDelta(t, tc.want, quote.Cost.ActualCost, 1e-12)
			require.InDelta(t, tc.want, quote.Command.SubscriptionRateMultiplier, 1e-12)
			require.InDelta(t, tc.independent, quote.Command.BalanceRateMultiplier, 1e-12)
			require.True(t, quote.Command.DisablePlanGroupRateMultiplier)
			require.Zero(t, logs.calls)
		})
	}
}

// Video 的三种费用合同都保存同一优先级，预留后重启不会重新读取分组开关。
func TestVideoTaskPreservesSubscriptionPriorityPolicy(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
		for _, priority := range []bool{false, true} {
			svc, key, _, _, _ := newVideoLifecycleFixture(mode)
			key.BillingMode = APIKeyBillingModeAuto
			key.Group.VideoRateIndependent, key.Group.VideoRateMultiplier, key.Group.VideoSubscriptionRatePriority = true, 1, priority
			task, _, created, err := svc.prepareTask(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model"}`)}, "priority-hash")
			require.NoError(t, err)
			require.True(t, created)
			key.Group.VideoSubscriptionRatePriority = !priority
			saved := cloneVideoLifecycleTask(task)
			require.Equal(t, !priority, saved.Hold.DisablePlanGroupRateMultiplier)
			require.Equal(t, 1.0, saved.Hold.SubscriptionRateMultiplier)
			require.Equal(t, 1.0, saved.Hold.BalanceRateMultiplier)
		}
	}
}

// 图片预占把独立默认值和优先规则交给锁内资金分配，随后捕获必须保留原合同。
func TestAsyncImageReservationPreservesSubscriptionPriorityPolicy(t *testing.T) {
	for _, priority := range []bool{false, true} {
		svc, repo, key, account, ctx := newImageQuoteTestService(t)
		key.Group.ImageRateIndependent, key.Group.ImageRateMultiplier, key.Group.ImageSubscriptionRatePriority = true, 1, priority
		handle, err := svc.PrepareAsyncImageBilling(ctx, key, account, nil, "gpt-image-2", 1, "1K", ChannelMappingResult{})
		require.NoError(t, err)
		require.Equal(t, !priority, repo.reserve.Hold.DisablePlanGroupRateMultiplier)
		require.Equal(t, 1.0, repo.reserve.Hold.SubscriptionRateMultiplier)
		require.Equal(t, 1.0, repo.reserve.Hold.BalanceRateMultiplier)
		key.Group.ImageSubscriptionRatePriority, key.Group.ImageRateMultiplier = !priority, 99
		require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{APIKey: key, User: key.User, Account: account,
			Result:     &OpenAIForwardResult{Model: "gpt-image-2", RequestID: "result", ImageSize: "1K", ImageCount: 1},
			ImageQuote: handle.Quote, ImageReservation: handle.Reservation}))
		require.Equal(t, !priority, repo.capture.DisablePlanGroupRateMultiplier)
		require.Equal(t, 1.0, repo.capture.BalanceRateMultiplier)
	}
}
