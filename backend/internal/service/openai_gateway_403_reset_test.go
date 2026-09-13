package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAI403CounterResetStub struct {
	resetCalls []int64
}

func (s *openAI403CounterResetStub) IncrementOpenAI403Count(context.Context, int64, int) (int64, error) {
	return 0, nil
}

func (s *openAI403CounterResetStub) ResetOpenAI403Count(_ context.Context, accountID int64) error {
	s.resetCalls = append(s.resetCalls, accountID)
	return nil
}

func TestOpenAIGatewayServiceRecordUsageResets403CounterForZeroUsage(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformKimi, PlatformZhipu, PlatformDeepseek} {
		t.Run(platform, func(t *testing.T) {
			counter := &openAI403CounterResetStub{}
			rateLimitSvc := NewRateLimitService(nil, nil, nil, nil, nil)
			rateLimitSvc.SetOpenAI403CounterCache(counter)

			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			userRepo := &openAIRecordUsageUserRepoStub{}
			subRepo := &openAIRecordUsageSubRepoStub{}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)
			svc.rateLimitService = rateLimitSvc

			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: "resp_zero_usage_reset_403_" + platform,
					Model:     "gpt-5.1",
				},
				APIKey:  &APIKey{ID: 1001, Group: &Group{RateMultiplier: 1}},
				User:    &User{ID: 2001},
				Account: &Account{ID: 777, Platform: platform},
			})

			require.NoError(t, err)
			require.Equal(t, []int64{777}, counter.resetCalls)
			require.Equal(t, 1, usageRepo.calls)
		})
	}
}
