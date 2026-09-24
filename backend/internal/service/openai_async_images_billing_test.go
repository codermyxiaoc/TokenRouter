package service

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// 异步图片复用普通结算：渠道 Token 价优先，默认按张价不混算，任务取消不取消已产出的扣费。
func TestOpenAIAsyncImageSettlementPreservesChannelPricingAndBillingContext(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		token                     bool
		mode                      BillingMode
		total, actual, multiplier float64
	}{
		{"channel_token", true, BillingModeToken, 0.02967, 0.074175, 2.5},
		{"group_image_price", false, BillingModeImage, 0.67, 0.5025, 0.75},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groupID := int64(501)
			imagePrice := 0.67
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			if tc.token {
				svc.resolver = newOpenAITokenImageChannelPricingResolverForTest(t, groupID, "gpt-image-2")
			} else {
				svc.resolver = newOpenAIImageChannelPricingResolverForTest(t, groupID, "gpt-image-2", 0.25)
			}
			requestID := "async-image-pricing-" + tc.name
			ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, requestID)
			ctx, cancel := context.WithCancel(WithAsyncImageExecutionContext(ctx))
			cancel()
			err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: "upstream-image-response", Model: "gpt-image-2", Duration: time.Second,
					ImageCount: 1, ImageSize: "4K",
					Usage: OpenAIUsage{InputTokens: 1110, OutputTokens: 1756, ImageOutputTokens: 1756},
				},
				APIKey: &APIKey{ID: 502, GroupID: &groupID, Group: &Group{
					ID: groupID, RateMultiplier: 2.5, ImageRateIndependent: true, ImageRateMultiplier: 0.75, ImagePrice4K: &imagePrice,
				}},
				User: &User{ID: 503}, Account: &Account{ID: 504},
				InboundEndpoint: "/v1/images/generations", UpstreamEndpoint: "/v1/images/generations",
			})
			require.NoError(t, err)
			log := usageRepo.lastLog
			require.NotNil(t, log)
			require.NotNil(t, log.BillingMode)
			require.Equal(t, string(tc.mode), *log.BillingMode)
			require.InDelta(t, tc.total, log.TotalCost, 1e-12)
			require.InDelta(t, tc.actual, log.ActualCost, 1e-12)
			require.InDelta(t, tc.multiplier, log.RateMultiplier, 1e-12)
			require.Equal(t, 1, log.ImageCount)
			require.Equal(t, "client:"+requestID, log.RequestID)
			if tc.token {
				require.InDelta(t, 0.00333, log.InputCost, 1e-12)
				require.InDelta(t, 0.02634, log.ImageOutputCost, 1e-12)
				require.Zero(t, log.OutputCost, "图片输出 Token 不应再次算作文字输出 Token")
			}
			billing := requireOpenAIRecordUsageBillingRepoStub(t, svc)
			require.Equal(t, 1, billing.calls)
			require.NoError(t, billing.lastCtxErr)
			require.NoError(t, usageRepo.lastCtxErr)
			require.Equal(t, "client:"+requestID, billing.lastCmd.RequestID)
			require.InDelta(t, tc.actual, billing.lastCmd.BillableAmountUSD, 1e-12)
		})
	}
}
