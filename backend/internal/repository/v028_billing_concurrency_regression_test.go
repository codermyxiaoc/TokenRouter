//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// v028BillingRun 记录结算事务并发的排队和耗时；这不是模型上游吞吐量测试。
func v028BillingRun(t *testing.T, stage string, commands []service.UsageBillingCommand) []usageBillingApplyOutcome {
	t.Helper()
	before := integrationDB.Stats()
	started := time.Now()
	results := billingFocusConcurrentApply(t, commands)
	after := integrationDB.Stats()
	applied, duplicate, conflicts, failed := 0, 0, 0, 0
	for _, result := range results {
		switch {
		case errors.Is(result.err, service.ErrUsageBillingRequestConflict):
			conflicts++
		case result.err != nil:
			failed++
		case result.result.Applied:
			applied++
		default:
			duplicate++
		}
	}
	t.Logf("stage=%s submitted=%d applied=%d duplicate=%d conflicts=%d unexpected_failures=%d pool_limit=64 pool_waits=%d pool_wait_total=%s elapsed=%s", stage, len(commands), applied, duplicate, conflicts, failed, after.WaitCount-before.WaitCount, after.WaitDuration-before.WaitDuration, time.Since(started))
	return results
}

// 三种结算方式使用不同套餐倍率，每笔同时提交三份；归档后仍必须保留幂等和指纹冲突保护。
func TestV028BillingConcurrentModesDuplicatesAndArchive(t *testing.T) {
	for _, concurrency := range []int{60, 120, 300} {
		for _, mode := range []string{service.APIKeyBillingModeBalance, service.APIKeyBillingModeSubscription, service.APIKeyBillingModeAuto} {
			t.Run(fmt.Sprintf("%s_%d", mode, concurrency), func(t *testing.T) {
				ctx := context.Background()
				client := testEntClient(t)
				user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", Balance: 100})
				group := mustCreateGroup(t, client, &service.Group{Name: "并发回归-" + uuid.NewString(), Platform: service.PlatformOpenAI})
				key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-regression-" + uuid.NewString(), Quota: 1000, RateLimit5h: 1000, RateLimit1d: 1000, RateLimit7d: 1000})
				now := time.Now()
				createSubscription := func(multiplier float64, days int) *service.UserSubscription {
					plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "倍率回归-" + uuid.NewString(), Price: 1, ValidityDays: 60, ValidityUnit: "day", ForSale: true, GroupIDs: []int64{group.ID}, GroupRateMultipliers: map[int64]float64{group.ID: multiplier}})
					return mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: now.Add(-time.Hour), ExpiresAt: now.AddDate(0, 0, days), DailyWindowStart: &now, WeeklyWindowStart: &now, MonthlyWindowStart: &now, DailyLimitUSD: float64Ptr(2), WeeklyLimitUSD: float64Ptr(2), MonthlyLimitUSD: float64Ptr(2)})
				}
				subA, subB := createSubscription(0.5, 40), createSubscription(2, 50)
				uniqueCount := concurrency / 3
				commands := make([]service.UsageBillingCommand, concurrency)
				requestIDs := make([]string, uniqueCount)
				for i := 0; i < uniqueCount; i++ {
					requestIDs[i] = uuid.NewString()
					command := service.UsageBillingCommand{RequestID: requestIDs[i], UserID: user.ID, APIKeyID: key.ID, GroupID: &group.ID, APIKeyBillingMode: mode, BaseAmountUSD: 0.5, BillableAmountUSD: 0.75, SubscriptionRateMultiplier: 1, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 1.5, APIKeyQuotaCost: 0.75, APIKeyRateLimitCost: 0.75}
					if mode == service.APIKeyBillingModeSubscription {
						command.PreferredSubscriptionID = &subA.ID
					}
					for copyIndex := 0; copyIndex < 3; copyIndex++ {
						commands[i+copyIndex*uniqueCount] = command
					}
				}
				applied := 0
				actualBalance, actualSubscription := decimal.Zero, decimal.Zero
				for _, outcome := range v028BillingRun(t, "initial", commands) {
					require.NoError(t, outcome.err)
					if outcome.result.Applied {
						applied++
						actualBalance = actualBalance.Add(decimal.NewFromFloat(outcome.result.BalanceAmountUSD))
						actualSubscription = actualSubscription.Add(decimal.NewFromFloat(outcome.result.SubscriptionAmountUSD))
					}
				}
				require.Equal(t, uniqueCount, applied)
				wantBalance := decimal.NewFromInt(int64(uniqueCount)).Mul(decimal.RequireFromString("0.75"))
				wantA, wantB := decimal.Zero, decimal.Zero
				if mode != service.APIKeyBillingModeBalance {
					wantA = decimal.NewFromInt(2)
					wantBalance = wantBalance.Sub(decimal.NewFromInt(6))
				}
				if mode == service.APIKeyBillingModeAuto {
					wantB = decimal.NewFromInt(2)
					wantBalance = wantBalance.Sub(decimal.RequireFromString("1.5"))
				}
				wantKey := wantBalance.Add(wantA).Add(wantB)
				require.True(t, wantBalance.Equal(actualBalance), "余额扣费不一致：want=%s actual=%s", wantBalance, actualBalance)
				require.True(t, wantA.Add(wantB).Equal(actualSubscription))
				assertFunds := func() {
					billingFocusAssertDecimal(t, decimal.NewFromInt(100).Sub(wantBalance).String(), `SELECT balance::text FROM users WHERE id=$1`, user.ID)
					for _, column := range []string{"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd"} {
						billingFocusAssertDecimal(t, wantA.String(), `SELECT `+column+`::text FROM user_subscriptions WHERE id=$1`, subA.ID)
						billingFocusAssertDecimal(t, wantB.String(), `SELECT `+column+`::text FROM user_subscriptions WHERE id=$1`, subB.ID)
					}
					for _, column := range []string{"quota_used", "usage_5h", "usage_1d", "usage_7d"} {
						billingFocusAssertDecimal(t, wantKey.String(), `SELECT `+column+`::text FROM api_keys WHERE id=$1`, key.ID)
					}
				}
				assertFunds()
				billingFocusAssertKey(t, key.ID, uniqueCount, wantKey.String(), commands)
				_, err := integrationDB.ExecContext(ctx, `UPDATE usage_billing_dedup SET created_at=$1 WHERE api_key_id=$2 AND request_id=ANY($3)`, now.AddDate(0, 0, -400), key.ID, pq.Array(requestIDs))
				require.NoError(t, err)
				require.NoError(t, newDashboardAggregationRepositoryWithSQL(integrationDB).CleanupUsageBillingDedup(ctx, now.AddDate(0, 0, -365)))
				var archiveCount int
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup_archive WHERE api_key_id=$1 AND request_id=ANY($2)`, key.ID, pq.Array(requestIDs)).Scan(&archiveCount))
				require.Equal(t, uniqueCount, archiveCount)
				for _, outcome := range v028BillingRun(t, "archived_replay", commands) {
					require.NoError(t, outcome.err)
					require.False(t, outcome.result.Applied)
				}
				mixed := make([]service.UsageBillingCommand, 60)
				for i := range mixed {
					mixed[i] = commands[i%len(commands)]
					if i%2 == 0 {
						mixed[i].RequestFingerprint = ""
						mixed[i].BillableAmountUSD += 1
					}
				}
				for i, outcome := range v028BillingRun(t, "archived_conflict_mix", mixed) {
					if i%2 == 0 {
						require.ErrorIs(t, outcome.err, service.ErrUsageBillingRequestConflict)
					} else {
						require.NoError(t, outcome.err)
						require.False(t, outcome.result.Applied)
					}
				}
				assertFunds()
				t.Logf("精确对账 unique=%d subscription_A=%s subscription_B=%s balance_charge=%s key_total=%s balance_remaining=%s", uniqueCount, wantA, wantB, wantBalance, wantKey, decimal.NewFromInt(100).Sub(wantBalance))
			})
		}
	}
}

// Redis 使用真实 Lua 原子更新；同时过期的三个窗口只能清零一次，不得丢掉其它并发请求。
func TestV028BillingRedisConcurrentBalanceAndExpiredWindows(t *testing.T) {
	for _, concurrency := range []int{60, 120, 300} {
		t.Run(fmt.Sprintf("%d", concurrency), func(t *testing.T) {
			ctx := context.Background()
			rdb := testRedis(t)
			cache := NewBillingCache(rdb)
			require.NoError(t, cache.SetUserBalance(ctx, 101, 100))
			old := time.Now().Add(-8 * 24 * time.Hour).Unix()
			require.NoError(t, cache.SetAPIKeyRateLimit(ctx, 202, &service.APIKeyRateLimitCacheData{Usage5h: 99, Usage1d: 99, Usage7d: 99, Window5h: old, Window1d: old, Window7d: old}))
			started := time.Now()
			start := make(chan struct{})
			results := make(chan error, concurrency)
			var ready, done sync.WaitGroup
			ready.Add(concurrency)
			done.Add(concurrency)
			for range concurrency {
				go func() {
					defer done.Done()
					ready.Done()
					<-start
					if err := cache.DeductUserBalance(ctx, 101, 0.125); err != nil {
						results <- err
						return
					}
					results <- cache.UpdateAPIKeyRateLimitUsage(ctx, 202, 0.125)
				}()
			}
			ready.Wait()
			close(start)
			done.Wait()
			close(results)
			for err := range results {
				require.NoError(t, err)
			}
			balance, err := cache.GetUserBalance(ctx, 101)
			require.NoError(t, err)
			expected := float64(concurrency) * 0.125
			require.Equal(t, 100-expected, balance)
			usage, err := cache.GetAPIKeyRateLimit(ctx, 202)
			require.NoError(t, err)
			require.Equal(t, expected, usage.Usage5h)
			require.Equal(t, expected, usage.Usage1d)
			require.Equal(t, expected, usage.Usage7d)
			require.GreaterOrEqual(t, usage.Window5h, started.Unix())
			require.GreaterOrEqual(t, usage.Window1d, started.Unix())
			require.GreaterOrEqual(t, usage.Window7d, started.Unix())
			t.Logf("Redis submitted=%d failures=0 balance=%g usage_5h/1d/7d=%g elapsed=%s", concurrency, balance, expected, time.Since(started))
		})
	}
}

// 在真实 PostgreSQL 制造一次锁环，验证计费共用的重试器会整笔回滚并重建事务，而不会重复扣余额。
func TestV028BillingRealDeadlockWholeTransactionRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := testEntClient(t)
	users := []*service.User{
		mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", Balance: 100}),
		mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", Balance: 100}),
	}
	attempts := make([]int, 2)
	errorsByTransaction := make([]error, 2)
	var ready, finished sync.WaitGroup
	ready.Add(2)
	finished.Add(2)
	started := time.Now()
	for i := range users {
		go func(i int) {
			defer finished.Done()
			_, errorsByTransaction[i] = retryPostgresDeadlock(ctx, "v028_regression_deliberate_deadlock", 2, func() (bool, error) {
				attempts[i]++
				tx, err := integrationDB.BeginTx(ctx, nil)
				if err != nil {
					return false, err
				}
				defer func() { _ = tx.Rollback() }()
				if _, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance-0.125 WHERE id=$1`, users[i].ID); err != nil {
					return false, err
				}
				if attempts[i] == 1 {
					ready.Done()
					ready.Wait()
				}
				if _, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance-0.25 WHERE id=$1`, users[1-i].ID); err != nil {
					return false, err
				}
				return true, tx.Commit()
			})
		}(i)
	}
	finished.Wait()
	for _, err := range errorsByTransaction {
		require.NoError(t, err)
	}
	require.Equal(t, 3, attempts[0]+attempts[1], "一次锁环只应产生一次完整事务重试")
	for _, user := range users {
		billingFocusAssertDecimal(t, "99.625", `SELECT balance::text FROM users WHERE id=$1`, user.ID)
	}
	t.Logf("真实锁环 transactions=2 attempts=%d deadlock_retries=1 failures=0 每个用户扣费=0.375 elapsed=%s", attempts[0]+attempts[1], time.Since(started))
}

// 团队代付同时检查重复提交、成员限额和上游成本，个人余额不能被团队请求扣除。
func TestV028BillingTeamDuplicateConcurrency(t *testing.T) {
	for _, concurrency := range []int{60, 120, 300} {
		t.Run(fmt.Sprintf("%d", concurrency), func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			owner := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", Balance: 100})
			member := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", Balance: 20})
			teamRepo := NewTeamRepository(integrationDB)
			team, err := teamRepo.Create(ctx, "并发幂等回归团队", owner.ID, 5)
			require.NoError(t, err)
			token := uuid.NewString()
			_, err = teamRepo.CreateInvitation(ctx, team.Team.ID, owner.ID, member.Email, token, time.Now().Add(time.Hour))
			require.NoError(t, err)
			_, err = teamRepo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
			require.NoError(t, err)
			key := mustCreateApiKey(t, client, &service.APIKey{UserID: member.ID, TeamID: &team.Team.ID, Key: "sk-regression-" + uuid.NewString(), Quota: 1000, RateLimit5h: 1000, RateLimit1d: 1000, RateLimit7d: 1000})
			account := mustCreateAccount(t, client, &service.Account{Name: "团队回归-" + uuid.NewString(), Type: service.AccountTypeAPIKey, Extra: map[string]any{"quota_limit": 100, "quota_daily_limit": 100, "quota_weekly_limit": 100}})
			commands := make([]service.UsageBillingCommand, concurrency)
			uniqueCount := concurrency / 2
			for i := range uniqueCount {
				command := service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: owner.ID, ActorUserID: member.ID, TeamID: &team.Team.ID, APIKeyID: key.ID, APIKeyBillingMode: service.APIKeyBillingModeBalance, BillableAmountUSD: 0.125, APIKeyQuotaCost: 0.125, APIKeyRateLimitCost: 0.125, AccountID: account.ID, AccountType: service.AccountTypeAPIKey, AccountQuotaCost: 0.01}
				commands[i], commands[i+uniqueCount] = command, command
			}
			applied := 0
			for _, outcome := range v028BillingRun(t, "team", commands) {
				require.NoError(t, outcome.err)
				if outcome.result.Applied {
					applied++
				}
			}
			require.Equal(t, uniqueCount, applied)
			amount := decimal.NewFromInt(int64(uniqueCount)).Mul(decimal.RequireFromString("0.125"))
			upstreamCost := decimal.NewFromInt(int64(uniqueCount)).Mul(decimal.RequireFromString("0.01"))
			billingFocusAssertDecimal(t, decimal.NewFromInt(100).Sub(amount).String(), `SELECT balance::text FROM users WHERE id=$1`, owner.ID)
			billingFocusAssertDecimal(t, "20", `SELECT balance::text FROM users WHERE id=$1`, member.ID)
			billingFocusAssertKey(t, key.ID, uniqueCount, amount.String(), commands)
			for _, column := range []string{"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd"} {
				billingFocusAssertDecimal(t, amount.String(), `SELECT `+column+`::text FROM team_memberships WHERE team_id=$1 AND user_id=$2 AND left_at IS NULL`, team.Team.ID, member.ID)
			}
			for _, field := range []string{"quota_used", "quota_daily_used", "quota_weekly_used"} {
				billingFocusAssertDecimal(t, upstreamCost.String(), `SELECT extra->>'`+field+`' FROM accounts WHERE id=$1`, account.ID)
			}
			t.Logf("团队精确对账 unique=%d owner_charge=%s member_personal_balance=20 member_usage=%s upstream_cost=%s", uniqueCount, amount, amount, upstreamCost)
		})
	}
}
