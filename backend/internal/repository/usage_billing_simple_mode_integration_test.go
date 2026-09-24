//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 真实数据库验证 60 并发与重复提交：只累计窗口，不动任何资金来源。
func TestUsageBillingSimpleMode60ConcurrentDeduplicatedWindows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-test-" + uuid.NewString(), Name: "simple-window", Quota: 1, RateLimit5h: 100, RateLimit1d: 100, RateLimit7d: 100})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Type: service.AccountTypeAPIKey, Extra: map[string]any{"quota_used": 2.0}})
	start := make(chan struct{})
	results := make(chan usageBillingApplyOutcome, 120)
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for retry := 0; retry < 2; retry++ {
				cmd := &service.UsageBillingCommand{RequestID: fmt.Sprintf("simple-60-%d-%d", key.ID, i), APIKeyID: key.ID, UserID: user.ID, AccountID: account.ID, AccountType: account.Type, APIKeyRateLimitCost: 0.125}
				result, err := repo.Apply(ctx, cmd)
				results <- usageBillingApplyOutcome{result: result, err: err}
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	applied := 0
	for outcome := range results {
		require.NoError(t, outcome.err)
		require.NotNil(t, outcome.result)
		if outcome.result.Applied {
			applied++
		}
		require.Zero(t, outcome.result.BalanceAmountUSD)
		require.Zero(t, outcome.result.SubscriptionAmountUSD)
	}
	require.Equal(t, 60, applied)
	var balance, quota, usage5h, usage1d, usage7d, accountUsed float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", user.ID).Scan(&balance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used,usage_5h,usage_1d,usage_7d FROM api_keys WHERE id=$1", key.ID).Scan(&quota, &usage5h, &usage1d, &usage7d))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT (extra->>'quota_used')::numeric FROM accounts WHERE id=$1", account.ID).Scan(&accountUsed))
	require.Equal(t, 10.0, balance)
	require.Zero(t, quota)
	require.Equal(t, 7.5, usage5h)
	require.Equal(t, 7.5, usage1d)
	require.Equal(t, 7.5, usage7d)
	require.Equal(t, 2.0, accountUsed)
}
