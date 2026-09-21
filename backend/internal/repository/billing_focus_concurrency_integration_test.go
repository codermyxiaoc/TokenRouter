//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// billingFocusConcurrentApply 使用起跑屏障制造真实事务竞争，每个请求独立持有可变命令。
func billingFocusConcurrentApply(t *testing.T, commands []service.UsageBillingCommand) []usageBillingApplyOutcome {
	t.Helper()
	previousMax := integrationDB.Stats().MaxOpenConnections
	// PostgreSQL 默认最多 100 个连接，模拟 64 连接业务池承载 120 个同时到达的请求。
	integrationDB.SetMaxOpenConns(64)
	t.Cleanup(func() { integrationDB.SetMaxOpenConns(previousMax) })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repo := NewUsageBillingRepository(integrationEntClient, integrationDB)
	start := make(chan struct{})
	results := make([]usageBillingApplyOutcome, len(commands))
	var ready, finished sync.WaitGroup
	ready.Add(len(commands))
	finished.Add(len(commands))
	for i := range commands {
		go func(i int) {
			defer finished.Done()
			ready.Done()
			<-start
			results[i].result, results[i].err = repo.Apply(ctx, &commands[i])
		}(i)
	}
	ready.Wait()
	close(start)
	finished.Wait()
	return results
}

// billingFocusAssertDecimal 直接比较 PostgreSQL 的十进制值，避免 float 容差掩盖逐笔差额。
func billingFocusAssertDecimal(t *testing.T, expected, query string, args ...any) {
	t.Helper()
	var actual string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), query, args...).Scan(&actual))
	want, err := decimal.NewFromString(expected)
	require.NoError(t, err)
	got, err := decimal.NewFromString(actual)
	require.NoError(t, err)
	require.True(t, want.Equal(got), "SQL 金额不一致：期望 %s，实际 %s", expected, actual)
}

// billingFocusAssertKey 同时核对资金去重、总配额与三个滚动窗口，不能只验证余额。
func billingFocusAssertKey(t *testing.T, keyID int64, count int, amount string, commands []service.UsageBillingCommand) {
	t.Helper()
	requestIDs := make([]string, len(commands))
	for i := range commands {
		requestIDs[i] = commands[i].RequestID
	}
	var actualCount int
	// 幂等表故意不关联用户外键；用户测试夹具重用 ID 时只统计本批请求，避免混入其它测试账本。
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id = $1 AND request_id = ANY($2)`, keyID, pq.Array(requestIDs)).Scan(&actualCount))
	require.Equal(t, count, actualCount)
	for _, column := range []string{"quota_used", "usage_5h", "usage_1d", "usage_7d"} {
		billingFocusAssertDecimal(t, amount, `SELECT `+column+`::text FROM api_keys WHERE id = $1`, keyID)
	}
}

func TestBillingFocusConcurrentBalanceExactReconciliation(t *testing.T) {
	for _, concurrency := range []int{60, 120} {
		t.Run(fmt.Sprintf("%d_requests_round_half_to_eight_places", concurrency), func(t *testing.T) {
			client := testEntClient(t)
			user := mustCreateUser(t, client, &service.User{Email: "billing-focus-" + uuid.NewString() + "@example.com", Balance: 100})
			key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-focus-" + uuid.NewString(), Quota: 100, RateLimit5h: 100, RateLimit1d: 100, RateLimit7d: 100})
			_, err := integrationDB.ExecContext(context.Background(), `UPDATE users SET frozen_balance = 7 WHERE id = $1`, user.ID)
			require.NoError(t, err)
			commands := make([]service.UsageBillingCommand, concurrency)
			for i := range commands {
				commands[i] = service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID,
					APIKeyBillingMode: service.APIKeyBillingModeBalance, BillableAmountUSD: 0.000078125,
					APIKeyQuotaCost: 0.000078125, APIKeyRateLimitCost: 0.000078125}
			}
			for i, outcome := range billingFocusConcurrentApply(t, commands) {
				require.NoError(t, outcome.err, "第 %d 笔请求", i)
				require.True(t, outcome.result.Applied)
				require.Equal(t, 0.00007813, outcome.result.BalanceAmountUSD)
				require.Zero(t, outcome.result.SubscriptionAmountUSD)
			}
			total := decimal.RequireFromString("0.00007813").Mul(decimal.NewFromInt(int64(concurrency)))
			billingFocusAssertDecimal(t, decimal.NewFromInt(100).Sub(total).String(), `SELECT balance::text FROM users WHERE id = $1`, user.ID)
			billingFocusAssertDecimal(t, "7", `SELECT frozen_balance::text FROM users WHERE id = $1`, user.ID)
			billingFocusAssertKey(t, key.ID, concurrency, total.String(), commands)
			t.Logf("并发=%d，每笔实际扣费=0.00007813，累计=%s，余额/Key 总配额/5h/1d/7d 完全一致", concurrency, total)
		})
	}
}

func TestBillingFocusConcurrentIdempotencyAndConflict(t *testing.T) {
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "billing-dedup-" + uuid.NewString() + "@example.com", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-focus-" + uuid.NewString(), Quota: 100, RateLimit5h: 100, RateLimit1d: 100, RateLimit7d: 100})
	command := service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID,
		APIKeyBillingMode: service.APIKeyBillingModeBalance, BillableAmountUSD: 0.125, APIKeyQuotaCost: 0.125, APIKeyRateLimitCost: 0.125}
	commands := make([]service.UsageBillingCommand, 120)
	for i := range commands {
		commands[i] = command
	}
	applied := 0
	for _, outcome := range billingFocusConcurrentApply(t, commands) {
		require.NoError(t, outcome.err)
		if outcome.result.Applied {
			applied++
		}
	}
	require.Equal(t, 1, applied, "120 个重复完成回调只能扣费一次")
	// 相同幂等键但价格不同必须冲突，而不是再扣费或覆盖之前的账本。
	conflicting := command
	conflicting.BillableAmountUSD = 2
	_, err := NewUsageBillingRepository(client, integrationDB).Apply(context.Background(), &conflicting)
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	billingFocusAssertDecimal(t, "9.875", `SELECT balance::text FROM users WHERE id = $1`, user.ID)
	billingFocusAssertKey(t, key.ID, 1, "0.125", commands)
	t.Log("120 次同请求并发：1 次扣费、119 次去重；不同金额重放冲突，余额=9.875")
}

func TestBillingFocusConcurrentUsersAndKeysAreIsolated(t *testing.T) {
	client := testEntClient(t)
	users := make([]*service.User, 2)
	keys := make([]*service.APIKey, 2)
	for i := range users {
		users[i] = mustCreateUser(t, client, &service.User{Email: "billing-isolation-" + uuid.NewString() + "@example.com", Balance: 100})
		keys[i] = mustCreateApiKey(t, client, &service.APIKey{UserID: users[i].ID, Key: "sk-focus-" + uuid.NewString(), Quota: 100, RateLimit5h: 100, RateLimit1d: 100, RateLimit7d: 100})
	}
	commands := make([]service.UsageBillingCommand, 120)
	sharedPrefix := uuid.NewString()
	for i := range commands {
		owner := i % 2
		amount := float64(owner+1) * 0.125
		// 两名用户故意复用同样的 60 个 request_id，唯一性还必须包含 API Key。
		commands[i] = service.UsageBillingCommand{RequestID: fmt.Sprintf("%s-%d", sharedPrefix, i/2), UserID: users[owner].ID, APIKeyID: keys[owner].ID,
			APIKeyBillingMode: service.APIKeyBillingModeBalance, BillableAmountUSD: amount, APIKeyQuotaCost: amount, APIKeyRateLimitCost: amount}
	}
	for _, outcome := range billingFocusConcurrentApply(t, commands) {
		require.NoError(t, outcome.err)
		require.True(t, outcome.result.Applied)
	}
	for i, expected := range []string{"7.5", "15"} {
		billingFocusAssertDecimal(t, decimal.NewFromInt(100).Sub(decimal.RequireFromString(expected)).String(), `SELECT balance::text FROM users WHERE id = $1`, users[i].ID)
		billingFocusAssertKey(t, keys[i].ID, 60, expected, commands)
	}
	t.Log("120 次并发跨用户：分别扣费 7.5 与 15；相同 request_id 不跨 Key 去重，不串用户")
}

func TestBillingFocusConcurrentSubscriptionAllocation(t *testing.T) {
	for _, mode := range []string{service.APIKeyBillingModeAuto, service.APIKeyBillingModeSubscription, service.APIKeyBillingModeBalance} {
		t.Run(mode, func(t *testing.T) {
			client := testEntClient(t)
			user := mustCreateUser(t, client, &service.User{Email: "billing-sub-focus-" + uuid.NewString() + "@example.com", Balance: 1})
			group := mustCreateGroup(t, client, &service.Group{Name: "billing-focus-" + uuid.NewString(), Platform: service.PlatformOpenAI})
			key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-focus-" + uuid.NewString(), Quota: 100, RateLimit5h: 100, RateLimit1d: 100, RateLimit7d: 100})
			now := time.Now()
			daily := timezone.StartOfDay(now)
			createSubscription := func(limit, multiplier float64, duration time.Duration) *service.UserSubscription {
				plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "billing-focus-" + uuid.NewString(), Price: 10, ValidityDays: 60, ValidityUnit: "day", ForSale: true,
					GroupIDs: []int64{group.ID}, GroupRateMultipliers: map[int64]float64{group.ID: multiplier}})
				return mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(duration),
					DailyWindowStart: &daily, WeeklyWindowStart: &now, MonthlyWindowStart: &now,
					DailyLimitUSD: float64Ptr(limit), WeeklyLimitUSD: float64Ptr(limit), MonthlyLimitUSD: float64Ptr(limit)})
			}
			subA := createSubscription(1, 0.5, 40*24*time.Hour)
			subB := createSubscription(2, 2, 50*24*time.Hour)
			commands := make([]service.UsageBillingCommand, 120)
			for i := range commands {
				commands[i] = service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, GroupID: &group.ID,
					APIKeyBillingMode: mode, BaseAmountUSD: 0.1, BillableAmountUSD: 0.15,
					SubscriptionRateMultiplier: 1, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 1.5,
					APIKeyQuotaCost: 0.15, APIKeyRateLimitCost: 0.15}
				if mode == service.APIKeyBillingModeSubscription {
					commands[i].PreferredSubscriptionID = &subA.ID
				}
			}
			allocationTotals := map[int64]float64{}
			var actualBalance, actualSubscription float64
			for _, outcome := range billingFocusConcurrentApply(t, commands) {
				require.NoError(t, outcome.err)
				require.True(t, outcome.result.Applied)
				actualBalance += outcome.result.BalanceAmountUSD
				actualSubscription += outcome.result.SubscriptionAmountUSD
				var allocationTotal float64
				for _, allocation := range outcome.result.BillingAllocations {
					allocationTotal += allocation.AmountUSD
					if allocation.Type == domain.BillingAllocationTypeSubscription {
						require.NotNil(t, allocation.SubscriptionID)
						allocationTotals[*allocation.SubscriptionID] += allocation.AmountUSD
					}
				}
				require.InDelta(t, outcome.result.SubscriptionAmountUSD+outcome.result.BalanceAmountUSD, allocationTotal, 1e-10)
			}
			wantA, wantB, wantBalance := 1.0, 2.0, 13.5
			if mode == service.APIKeyBillingModeSubscription {
				wantB, wantBalance = 0, 15
			} else if mode == service.APIKeyBillingModeBalance {
				wantA, wantB, wantBalance = 0, 0, 18
			}
			require.InDelta(t, wantA, allocationTotals[subA.ID], 1e-8)
			require.InDelta(t, wantB, allocationTotals[subB.ID], 1e-8)
			require.InDelta(t, wantA+wantB, actualSubscription, 1e-8)
			require.InDelta(t, wantBalance, actualBalance, 1e-8)
			for _, sub := range []struct {
				id    int64
				usage float64
			}{{subA.ID, wantA}, {subB.ID, wantB}} {
				for _, column := range []string{"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd"} {
					billingFocusAssertDecimal(t, decimal.NewFromFloat(sub.usage).String(), `SELECT `+column+`::text FROM user_subscriptions WHERE id = $1`, sub.id)
				}
			}
			billingFocusAssertDecimal(t, decimal.NewFromFloat(1-wantBalance).String(), `SELECT balance::text FROM users WHERE id = $1`, user.ID)
			billingFocusAssertKey(t, key.ID, 120, decimal.NewFromFloat(wantA+wantB+wantBalance).String(), commands)
			t.Logf("120 次并发 mode=%s：基础费用=12，套餐 A(0.5x)=%g，套餐 B(2x)=%g，余额扣费(1.5x)=%g，最终余额=%g", mode, wantA, wantB, wantBalance, 1-wantBalance)
		})
	}
}

func TestBillingFocusInvalidPreferredSubscriptionRollsBack(t *testing.T) {
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "billing-rollback-" + uuid.NewString() + "@example.com", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-focus-" + uuid.NewString(), Quota: 100, RateLimit5h: 100, RateLimit1d: 100, RateLimit7d: 100})
	missing := int64(999999999)
	command := service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID,
		APIKeyBillingMode: service.APIKeyBillingModeSubscription, PreferredSubscriptionID: &missing,
		BillableAmountUSD: 1, APIKeyQuotaCost: 1, APIKeyRateLimitCost: 1}
	commands := make([]service.UsageBillingCommand, 60)
	for i := range commands {
		commands[i] = command
		commands[i].RequestID = uuid.NewString()
	}
	for _, outcome := range billingFocusConcurrentApply(t, commands) {
		require.ErrorIs(t, outcome.err, service.ErrPreferredSubscriptionInsufficient)
	}
	billingFocusAssertDecimal(t, "10", `SELECT balance::text FROM users WHERE id = $1`, user.ID)
	billingFocusAssertKey(t, key.ID, 0, "0", commands)
	// 已回滚的事务不得遗留幂等占位；相同请求改正资金快照后可以正常完成。
	command = commands[0]
	command.APIKeyBillingMode = service.APIKeyBillingModeBalance
	command.RequestFingerprint = ""
	result, err := NewUsageBillingRepository(client, integrationDB).Apply(context.Background(), &command)
	require.NoError(t, err)
	require.True(t, result.Applied)
	billingFocusAssertDecimal(t, "9", `SELECT balance::text FROM users WHERE id = $1`, user.ID)
	billingFocusAssertKey(t, key.ID, 1, "1", commands)
}

func TestBillingFocusConcurrentTeamPayerAndUpstreamCost(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	owner := mustCreateUser(t, client, &service.User{Email: "billing-team-owner-" + uuid.NewString() + "@example.com", Balance: 100})
	teamRepo := NewTeamRepository(integrationDB)
	team, err := teamRepo.Create(ctx, "并发计费测试团队", owner.ID, 5)
	require.NoError(t, err)
	members := make([]*service.User, 2)
	keys := make([]*service.APIKey, 2)
	for i := range members {
		members[i] = mustCreateUser(t, client, &service.User{Email: "billing-member-" + uuid.NewString() + "@example.com", Balance: 20})
		token := uuid.NewString()
		_, err = teamRepo.CreateInvitation(ctx, team.Team.ID, owner.ID, members[i].Email, token, time.Now().Add(time.Hour))
		require.NoError(t, err)
		_, err = teamRepo.ResolveInvitation(ctx, token, members[i].ID, members[i].Email, "accepted", time.Now())
		require.NoError(t, err)
		keys[i] = mustCreateApiKey(t, client, &service.APIKey{UserID: members[i].ID, TeamID: &team.Team.ID, Key: "sk-focus-" + uuid.NewString(),
			Quota: 100, RateLimit5h: 100, RateLimit1d: 100, RateLimit7d: 100})
	}
	account := mustCreateAccount(t, client, &service.Account{Name: "billing-account-focus-" + uuid.NewString(), Type: service.AccountTypeAPIKey,
		Extra: map[string]any{"quota_limit": 100, "quota_daily_limit": 100, "quota_weekly_limit": 100}})
	commands := make([]service.UsageBillingCommand, 120)
	for i := range commands {
		memberIndex := i % 2
		commands[i] = service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: owner.ID, ActorUserID: members[memberIndex].ID,
			TeamID: &team.Team.ID, APIKeyID: keys[memberIndex].ID, APIKeyBillingMode: service.APIKeyBillingModeBalance,
			BillableAmountUSD: 0.125, APIKeyQuotaCost: 0.125, APIKeyRateLimitCost: 0.125,
			AccountID: account.ID, AccountType: service.AccountTypeAPIKey, AccountQuotaCost: 0.01}
	}
	for _, outcome := range billingFocusConcurrentApply(t, commands) {
		require.NoError(t, outcome.err)
		require.True(t, outcome.result.Applied)
	}
	// 同一 owner 的两名成员同时使用共享上游账号，付款、成员用量和上游成本不能混为同一个口径。
	billingFocusAssertDecimal(t, "85", `SELECT balance::text FROM users WHERE id = $1`, owner.ID)
	for i, member := range members {
		billingFocusAssertDecimal(t, "20", `SELECT balance::text FROM users WHERE id = $1`, member.ID)
		billingFocusAssertKey(t, keys[i].ID, 60, "7.5", commands)
		for _, column := range []string{"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd"} {
			billingFocusAssertDecimal(t, "7.5", `SELECT `+column+`::text FROM team_memberships WHERE team_id = $1 AND user_id = $2 AND left_at IS NULL`, team.Team.ID, member.ID)
		}
	}
	for _, field := range []string{"quota_used", "quota_daily_used", "quota_weekly_used"} {
		billingFocusAssertDecimal(t, "1.2", `SELECT extra->>'`+field+`' FROM accounts WHERE id = $1`, account.ID)
	}
	t.Log("120 次团队请求：owner 扣费15、两成员各累计7.5且个人余额不变、共享上游账号总/日/周成本1.2")
}

func TestBillingFocusLateFailureRollsBackAllFinancialEffects(t *testing.T) {
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "billing-late-failure-" + uuid.NewString() + "@example.com", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-focus-" + uuid.NewString(), Quota: 100, RateLimit5h: 100, RateLimit1d: 100, RateLimit7d: 100})
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{Name: "billing-rollback-" + uuid.NewString(), Price: 1, ValidityDays: 60, ValidityUnit: "day", ForSale: true})
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, PlanID: plan.ID, ExpiresAt: time.Now().Add(60 * 24 * time.Hour),
		DailyLimitUSD: float64Ptr(1), WeeklyLimitUSD: float64Ptr(1), MonthlyLimitUSD: float64Ptr(1)})
	command := service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID,
		APIKeyBillingMode: service.APIKeyBillingModeAuto, BillableAmountUSD: 2, APIKeyQuotaCost: 2, APIKeyRateLimitCost: 2,
		AccountID: 999999999, AccountType: service.AccountTypeAPIKey, AccountQuotaCost: 0.1}
	commands := make([]service.UsageBillingCommand, 60)
	for i := range commands {
		commands[i] = command
		commands[i].RequestID = uuid.NewString()
	}
	// 账号成本写入位于订阅、余额和 Key 用量之后，故障必须撤销此前全部 SQL 效果。
	for _, outcome := range billingFocusConcurrentApply(t, commands) {
		require.ErrorIs(t, outcome.err, service.ErrAccountNotFound)
	}
	billingFocusAssertDecimal(t, "10", `SELECT balance::text FROM users WHERE id = $1`, user.ID)
	billingFocusAssertKey(t, key.ID, 0, "0", commands)
	for _, column := range []string{"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd"} {
		billingFocusAssertDecimal(t, "0", `SELECT `+column+`::text FROM user_subscriptions WHERE id = $1`, subscription.ID)
	}
	account := mustCreateAccount(t, client, &service.Account{Name: "billing-rollback-retry-" + uuid.NewString(), Type: service.AccountTypeAPIKey})
	command = commands[0]
	command.AccountID = account.ID
	command.RequestFingerprint = ""
	result, err := NewUsageBillingRepository(client, integrationDB).Apply(context.Background(), &command)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Equal(t, 1.0, result.SubscriptionAmountUSD)
	require.Equal(t, 1.0, result.BalanceAmountUSD)
	billingFocusAssertDecimal(t, "9", `SELECT balance::text FROM users WHERE id = $1`, user.ID)
	billingFocusAssertKey(t, key.ID, 1, "2", commands)
	for _, column := range []string{"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd"} {
		billingFocusAssertDecimal(t, "1", `SELECT `+column+`::text FROM user_subscriptions WHERE id = $1`, subscription.ID)
	}
	t.Log("60 次结算后段失败：订阅、余额、Key、去重占位全部回滚；同请求修正账号后仅结算一次")
}
