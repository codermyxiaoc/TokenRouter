//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// 有界连接池模拟真实服务，避免测试依靠无限创建数据库连接掩盖并发问题。
func dailyActivityStressPool(t *testing.T, size int) {
	t.Helper()
	previous := integrationDB.Stats().MaxOpenConnections
	integrationDB.SetMaxOpenConns(size)
	t.Cleanup(func() { integrationDB.SetMaxOpenConns(previous) })
}

func TestDailyActivityAdversarialHundredConcurrentClaims(t *testing.T) {
	for _, kind := range []string{"daily_gift", "recharge", "consumption"} {
		t.Run(kind, func(t *testing.T) {
			dailyActivityStressPool(t, 32)
			repo, uid, _ := dailyActivityFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			addDailyActivityOrder(t, uid, 10, 0, 0, "COMPLETED", "balance", "alipay", dailyActivityDBNow(t))
			_, err := integrationDB.ExecContext(ctx, `INSERT INTO daily_activity_spending_totals(user_id,amount) VALUES($1,10)`, uid)
			require.NoError(t, err)
			const requests = 128
			start := make(chan struct{})
			results := make(chan error, requests)
			var wg sync.WaitGroup
			for i := 0; i < requests; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, err := repo.Claim(ctx, uid, service.DailyActivityClaimInput{ActivityType: kind, IdempotencyKey: uuid.NewString()})
					results <- err
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				} else {
					require.ErrorIs(t, err, service.ErrDailyActivityUnavailable)
				}
			}
			want := 100
			if kind == "daily_gift" {
				want = 1
			}
			require.Equal(t, want, successes)
			var count, batches int
			var balance, sum float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(amount),0) FROM daily_activity_claims WHERE user_id=$1`, uid).Scan(&count, &sum))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM daily_activity_claim_batches WHERE user_id=$1`, uid).Scan(&batches))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
			require.Equal(t, want, count)
			require.Equal(t, want, batches)
			require.InDelta(t, float64(want)*.1, sum, 1e-8)
			require.InDelta(t, 5+sum, balance, 1e-8)
			t.Logf("%s: %d concurrent requests, %d grants, reward %.2f, balance %.2f", kind, requests, successes, sum, balance)
		})
	}
}

func TestDailyActivityAdversarialHundredSameKeyBatchReplay(t *testing.T) {
	dailyActivityStressPool(t, 32)
	repo, uid, _ := dailyActivityFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addDailyActivityOrder(t, uid, 25, 0, 0, "COMPLETED", "balance", "alipay", dailyActivityDBNow(t))
	input := service.DailyActivityClaimInput{ActivityType: "recharge", All: true, IdempotencyKey: uuid.NewString()}
	const concurrency = 100
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan *service.DailyActivityClaimResult, concurrency)
	errs := make(chan error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; out, err := repo.Claim(ctx, uid, input); results <- out; errs <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	first := 0
	var canonical *service.DailyActivityClaimResult
	for out := range results {
		require.NotNil(t, out)
		if !out.Replayed {
			first++
		}
		if canonical == nil {
			canonical = out
		} else {
			require.Equal(t, canonical.Items, out.Items)
			require.Equal(t, canonical.Balance, out.Balance)
		}
		require.Len(t, out.Items, 100)
		require.Equal(t, int64(150), out.RemainingCount)
	}
	require.Equal(t, 1, first)
	var count, batches int
	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM daily_activity_claims WHERE user_id=$1`, uid).Scan(&count))
	require.Equal(t, 100, count)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM daily_activity_claim_batches WHERE user_id=$1`, uid).Scan(&batches))
	require.Equal(t, 1, batches)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 15.0, balance)
}

func TestDailyActivityAdversarialConcurrentIdempotencyShapeConflict(t *testing.T) {
	dailyActivityStressPool(t, 32)
	repo, uid, _ := dailyActivityFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addDailyActivityOrder(t, uid, 10, 0, 0, "COMPLETED", "balance", "alipay", dailyActivityDBNow(t))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO daily_activity_spending_totals(user_id,amount) VALUES($1,10)`, uid)
	require.NoError(t, err)
	key := uuid.NewString()
	shapes := []service.DailyActivityClaimInput{
		{ActivityType: "daily_gift", IdempotencyKey: key},
		{ActivityType: "recharge", IdempotencyKey: key},
		{ActivityType: "recharge", All: true, IdempotencyKey: key},
		{ActivityType: "consumption", All: true, IdempotencyKey: key},
	}
	type outcome struct {
		shape int
		out   *service.DailyActivityClaimResult
		err   error
	}
	const concurrency = 100
	results := make(chan outcome, concurrency)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		shape := i % len(shapes)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, err := repo.Claim(ctx, uid, shapes[shape])
			results <- outcome{shape: shape, out: out, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winner, originals, successes, conflicts := -1, 0, 0, 0
	for result := range results {
		if result.err != nil {
			require.ErrorIs(t, result.err, service.ErrDailyActivityIdempotency)
			conflicts++
			continue
		}
		require.NotNil(t, result.out)
		if winner < 0 {
			winner = result.shape
		}
		require.Equal(t, winner, result.shape)
		if !result.out.Replayed {
			originals++
		}
		successes++
	}
	require.Equal(t, 1, originals)
	require.Equal(t, 25, successes)
	require.Equal(t, 75, conflicts)
	wantClaims := 1
	if shapes[winner].All {
		wantClaims = 100
	}
	var claims, batches int
	var balance, reward float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(amount),0) FROM daily_activity_claims WHERE user_id=$1`, uid).Scan(&claims, &reward))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM daily_activity_claim_batches WHERE user_id=$1`, uid).Scan(&batches))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, wantClaims, claims)
	require.Equal(t, 1, batches)
	require.InDelta(t, 5+reward, balance, 1e-8)
}

func TestDailyActivityAdversarialLateBatchFailureRollsBackFunds(t *testing.T) {
	repo, uid, _ := dailyActivityFixture(t)
	ctx := context.Background()
	addDailyActivityOrder(t, uid, 1, 0, 0, "COMPLETED", "balance", "alipay", dailyActivityDBNow(t))
	// 初始空结果允许写入，在更新余额后持久化返回结果时注入失败，覆盖事务最末尾的回滚。
	constraint := fmt.Sprintf("daily_activity_test_final_result_%d", uid)
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE daily_activity_claim_batches ADD CONSTRAINT %s CHECK(user_id <> %d OR result = '{}'::jsonb)`, constraint, uid))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `ALTER TABLE daily_activity_claim_batches DROP CONSTRAINT IF EXISTS `+constraint)
	})
	input := service.DailyActivityClaimInput{ActivityType: "recharge", IdempotencyKey: uuid.NewString()}
	_, err = repo.Claim(ctx, uid, input)
	require.ErrorContains(t, err, constraint)
	var claims, batches int
	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM daily_activity_claims WHERE user_id=$1`, uid).Scan(&claims))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM daily_activity_claim_batches WHERE user_id=$1`, uid).Scan(&batches))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, claims)
	require.Zero(t, batches)
	require.Equal(t, 5.0, balance)
	status, err := repo.Status(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(10), status.Activities[1].AvailableCount)
	_, err = integrationDB.ExecContext(ctx, `ALTER TABLE daily_activity_claim_batches DROP CONSTRAINT `+constraint)
	require.NoError(t, err)
	out, err := repo.Claim(ctx, uid, input)
	require.NoError(t, err)
	require.False(t, out.Replayed)
	require.Len(t, out.Items, 1)
	require.Equal(t, 5.1, out.Balance)
}

func TestDailyActivityAdversarialSettingsCreditsAndClaimsRace(t *testing.T) {
	dailyActivityStressPool(t, 32)
	repo, uid, cfg := dailyActivityFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cfg.IncludeRedeemBalance = true
	cfg.IncludeAdminBalance = true
	_, err := repo.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	addDailyActivityOrder(t, uid, .5, 0, 0, "COMPLETED", "balance", "alipay", dailyActivityDBNow(t))
	codes := make([]*service.RedeemCode, 100)
	for i := range codes {
		kind := service.RedeemTypeBalance
		if i%2 == 1 {
			kind = service.AdjustmentTypeAdminBalance
		}
		codes[i] = dailyActivityCreditCode(t, kind, .01, 1)
	}
	type outcome struct {
		source string
		err    error
	}
	results := make(chan outcome, 225)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range codes {
		code := codes[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tx, err := integrationEntClient.Tx(ctx)
			if err != nil {
				results <- outcome{"credit", err}
				return
			}
			defer tx.Rollback()
			txCtx := dbent.NewTxContext(ctx, tx)
			err = NewUserRepository(integrationEntClient, integrationDB).UpdateBalance(txCtx, uid, .01)
			if err == nil {
				err = NewRedeemCodeRepository(integrationEntClient).CreateUsage(txCtx, &service.RedeemCodeUsage{RedeemCodeID: code.ID, UserID: uid, UsedAt: time.Now()})
			}
			if err == nil {
				err = tx.Commit()
			}
			results <- outcome{"credit", err}
		}()
	}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.Claim(ctx, uid, service.DailyActivityClaimInput{ActivityType: "recharge", IdempotencyKey: uuid.NewString()})
			results <- outcome{"claim", err}
		}()
	}
	for i := 0; i < 25; i++ {
		index := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			next := *cfg
			next.Enabled = index%3 != 0
			next.Recharge.Enabled = index%2 == 0
			next.IncludeRedeemBalance = index%3 == 0
			next.IncludeAdminBalance = index%2 != 0
			_, err := repo.SaveConfig(ctx, next)
			results <- outcome{"settings", err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for result := range results {
		if result.source == "claim" && (errors.Is(result.err, service.ErrDailyActivityDisabled) || errors.Is(result.err, service.ErrDailyActivityRuleDisabled) || errors.Is(result.err, service.ErrDailyActivityUnavailable)) {
			continue
		}
		require.NoError(t, result.err, "%s", result.source)
	}
	_, err = repo.SaveConfig(ctx, *cfg)
	require.NoError(t, err)
	for {
		_, err = repo.Claim(ctx, uid, service.DailyActivityClaimInput{ActivityType: "recharge", All: true, IdempotencyKey: uuid.NewString()})
		if errors.Is(err, service.ErrDailyActivityUnavailable) {
			break
		}
		require.NoError(t, err)
	}
	var creditAmount, rewardAmount, balance float64
	var creditCount, claimCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(amount),0) FROM daily_activity_credit_events WHERE user_id=$1`, uid).Scan(&creditCount, &creditAmount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(amount),0) FROM daily_activity_claims WHERE user_id=$1`, uid).Scan(&claimCount, &rewardAmount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	expectedClaims := decimal.NewFromFloat(.5).Add(decimal.NewFromFloat(creditAmount)).Div(decimal.NewFromFloat(.1)).Floor().IntPart()
	require.Equal(t, expectedClaims, int64(claimCount))
	require.LessOrEqual(t, creditCount, 100)
	require.InDelta(t, 6+rewardAmount, balance, 1e-8)
	state, err := repo.Status(ctx, uid)
	require.NoError(t, err)
	require.InDelta(t, .5+creditAmount, state.Activities[1].EligibleAmount, 1e-8)
	require.Zero(t, state.Activities[1].AvailableCount)
	require.True(t, cfg.StartedAt.Equal(*state.StartedAt))
	t.Logf("100 credits + 100 claims + 25 config writes: %d included credits, %d grants, balance %.8f", creditCount, claimCount, balance)
}

// 将事务内查询集中到连接池满载时，暴露外层context误用造成的二次借用连接。
type dailyActivityPoolBarrierUserRepo struct {
	service.UserRepository
	capacity int32
	arrivals atomic.Int32
	ready    chan struct{}
}

func (r *dailyActivityPoolBarrierUserRepo) GetByID(ctx context.Context, id int64) (*service.User, error) {
	if dbent.TxFromContext(ctx) == nil {
		if r.arrivals.Add(1) == r.capacity {
			close(r.ready)
		}
		select {
		case <-r.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.UserRepository.GetByID(ctx, id)
}

func TestDailyActivityAdversarialRedeemPoolSaturation(t *testing.T) {
	const concurrency = 16
	dailyActivityStressPool(t, concurrency)
	_, uid, _ := dailyActivityFixture(t)
	base := NewUserRepository(integrationEntClient, integrationDB)
	users := &dailyActivityPoolBarrierUserRepo{UserRepository: base, capacity: concurrency, ready: make(chan struct{})}
	redeemer := service.NewRedeemService(NewRedeemCodeRepository(integrationEntClient), users, nil, nil, nil, integrationEntClient, nil, nil)
	codes := make([]*service.RedeemCode, concurrency)
	for i := range codes {
		codes[i] = dailyActivityCreditCode(t, service.RedeemTypeBalance, 1, 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	errs := make(chan error, concurrency)
	var wg sync.WaitGroup
	for _, code := range codes {
		value := code.Code
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := redeemer.Redeem(ctx, uid, value); errs <- err }()
	}
	close(start)
	wg.Wait()
	close(errs)
	failures := make([]string, 0)
	for err := range errs {
		if err != nil {
			failures = append(failures, err.Error())
		}
	}
	require.Empty(t, failures, fmt.Sprintf("%d concurrent redemptions should not wait for a second database connection", concurrency))
	user, err := base.GetByID(context.Background(), uid)
	require.NoError(t, err)
	require.Equal(t, 21.0, user.Balance)
}
