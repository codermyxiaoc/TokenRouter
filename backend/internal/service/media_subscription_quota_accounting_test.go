//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 零估价只开启已有 Key 计数通路，不能把占位信号变成用户扣费或免费任务的实际金额。
func TestBuildUsageBillingCommandMediaSubscriptionQuotaAccounting(t *testing.T) {
	for _, media := range []BillingMode{BillingModeImage, BillingModeVideo} {
		for _, tc := range []struct {
			name          string
			mutate        func(*usageBillingParams, **UsageLog)
			wantQuota     float64
			wantRateLimit float64
			wantBillable  float64
			wantZeroBase  bool
		}{
			{name: "零估价允许事务内套餐转为付费", wantQuota: 1, wantRateLimit: 1},
			{
				name: "指定订阅仍保留计数通路", wantQuota: 1, wantRateLimit: 1,
				mutate: func(p *usageBillingParams, _ **UsageLog) {
					id := int64(4)
					p.APIKey.BillingMode = APIKeyBillingModeSubscription
					p.APIKey.PreferredSubscriptionID = &id
				},
			},
			{
				name: "关闭当前媒体优先开关不受另一媒体开关影响",
				mutate: func(p *usageBillingParams, _ **UsageLog) {
					if media == BillingModeImage {
						p.APIKey.Group.ImageSubscriptionRatePriority = false
					} else {
						p.APIKey.Group.VideoSubscriptionRatePriority = false
					}
				},
			},
			{
				name: "未启用独立倍率",
				mutate: func(p *usageBillingParams, _ **UsageLog) {
					if media == BillingModeImage {
						p.APIKey.Group.ImageRateIndependent = false
					} else {
						p.APIKey.Group.VideoRateIndependent = false
					}
				},
			},
			{
				name:   "纯令牌费用保留既有零价行为",
				mutate: func(p *usageBillingParams, _ **UsageLog) { p.Cost.BillingMode = string(BillingModeToken) },
			},
			{
				name:   "非媒体用量不启用",
				mutate: func(_ *usageBillingParams, log **UsageLog) { (*log).ImageCount, (*log).VideoCount = 0, 0 },
			},
			{
				name:   "缺少用量记录不启用",
				mutate: func(_ *usageBillingParams, log **UsageLog) { *log = nil },
			},
			{
				name: "免费基础价格不启用", wantZeroBase: true,
				mutate: func(p *usageBillingParams, _ **UsageLog) { p.Cost.TotalCost = 0 },
			},
			{
				name: "显式免费基础价格不回退账号成本", wantZeroBase: true,
				mutate: func(p *usageBillingParams, _ **UsageLog) {
					zero := 0.0
					p.BillingBaseAmountUSD = &zero
				},
			},
			{
				name:   "旧倍率参数不启用",
				mutate: func(p *usageBillingParams, _ **UsageLog) { p.RateMultipliersResolved = false },
			},
			{
				name:   "冻结合同禁止套餐覆盖",
				mutate: func(p *usageBillingParams, _ **UsageLog) { p.DisablePlanGroupRateMultiplier = true },
			},
			{
				name:   "仅余额不启用",
				mutate: func(p *usageBillingParams, _ **UsageLog) { p.APIKey.BillingMode = APIKeyBillingModeBalance },
			},
			{
				name:   "无配额服务不启用",
				mutate: func(p *usageBillingParams, _ **UsageLog) { p.APIKeyService = nil },
			},
			{
				name:   "无分组快照不启用",
				mutate: func(p *usageBillingParams, _ **UsageLog) { p.APIKey.Group = nil },
			},
			{
				name: "无Key限额不产生计数信号",
				mutate: func(p *usageBillingParams, _ **UsageLog) {
					p.APIKey.Quota, p.APIKey.RateLimit5h = 0, 0
				},
			},
			{
				name: "只有总配额", wantQuota: 1,
				mutate: func(p *usageBillingParams, _ **UsageLog) { p.APIKey.RateLimit5h = 0 },
			},
			{
				name: "只有五小时限额", wantRateLimit: 1,
				mutate: func(p *usageBillingParams, _ **UsageLog) { p.APIKey.Quota = 0 },
			},
			{
				name: "只有一日限额", wantRateLimit: 1,
				mutate: func(p *usageBillingParams, _ **UsageLog) {
					p.APIKey.Quota, p.APIKey.RateLimit5h, p.APIKey.RateLimit1d = 0, 0, 10
				},
			},
			{
				name: "只有七日限额", wantRateLimit: 1,
				mutate: func(p *usageBillingParams, _ **UsageLog) {
					p.APIKey.Quota, p.APIKey.RateLimit5h, p.APIKey.RateLimit7d = 0, 0, 10
				},
			},
			{
				name: "简易模式不进入资金补偿通路", wantZeroBase: true,
				mutate: func(p *usageBillingParams, _ **UsageLog) { p.SimpleModeKeyRateLimitOnly = true },
			},
			{
				name: "正预估保留原始计数金额", wantQuota: 0.288, wantRateLimit: 0.288, wantBillable: 0.288,
				mutate: func(p *usageBillingParams, _ **UsageLog) { p.Cost.ActualCost = 0.288 },
			},
		} {
			t.Run(string(media)+"/"+tc.name, func(t *testing.T) {
				groupID := int64(123)
				p := &usageBillingParams{
					User: &User{ID: 1}, Account: &Account{ID: 3},
					APIKey: &APIKey{ID: 2, GroupID: &groupID, BillingMode: APIKeyBillingModeAuto, Quota: 100, RateLimit5h: 10, Group: &Group{
						ID:                   groupID,
						ImageRateIndependent: true, ImageSubscriptionRatePriority: true,
						VideoRateIndependent: true, VideoSubscriptionRatePriority: true,
					}},
					Cost:                            &CostBreakdown{TotalCost: 0.12, BillingMode: string(media)},
					APIKeyService:                   &apiKeyQuotaUpdaterStub{},
					RateMultipliersResolved:         true,
					SubscriptionRateMultiplierScale: 1,
				}
				log := &UsageLog{ImageCount: 1}
				if media == BillingModeVideo {
					log.ImageCount, log.VideoCount = 0, 1
				}
				if tc.mutate != nil {
					tc.mutate(p, &log)
				}
				cmd := buildUsageBillingCommand("media-zero-quote", log, p)
				require.NotNil(t, cmd)
				require.Equal(t, tc.wantQuota, cmd.APIKeyQuotaCost)
				require.Equal(t, tc.wantRateLimit, cmd.APIKeyRateLimitCost)
				require.Equal(t, tc.wantBillable, cmd.BillableAmountUSD)
				require.Zero(t, cmd.AccountQuotaCost)
				require.Zero(t, cmd.BalanceRateMultiplier)
				require.Zero(t, cmd.SubscriptionRateMultiplier)
				if tc.wantZeroBase {
					require.Zero(t, cmd.BaseAmountUSD)
				} else {
					require.Equal(t, 0.12, cmd.BaseAmountUSD)
				}
				// 重复归一化不能丢失启用信号或改变请求指纹，供仓储幂等重放使用。
				fingerprint := cmd.RequestFingerprint
				cmd.Normalize()
				require.Equal(t, fingerprint, cmd.RequestFingerprint)
				require.Equal(t, tc.wantQuota, cmd.APIKeyQuotaCost)
				require.Equal(t, tc.wantRateLimit, cmd.APIKeyRateLimitCost)
			})
		}
	}
}
