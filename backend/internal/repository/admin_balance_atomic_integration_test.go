//go:build integration

package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 复用真实仓储与事务，只在指定持久化边界注入错误，验证资金与流水不会部分成功。
type adminBalanceFailingRedeemRepo struct {
	service.RedeemCodeRepository
	err error
}

func (r adminBalanceFailingRedeemRepo) CreateUsage(ctx context.Context, usage *service.RedeemCodeUsage) error {
	if err := r.RedeemCodeRepository.CreateUsage(ctx, usage); err != nil {
		return err
	}
	return r.err
}

type adminBalanceFailingReadRepo struct {
	service.UserRepository
	err error
}

func (r adminBalanceFailingReadRepo) GetByID(context.Context, int64) (*service.User, error) {
	return nil, r.err
}

type adminBalanceCommitObserver struct {
	onInvalidate func(int64)
}

func (o adminBalanceCommitObserver) InvalidateAuthCacheByUserID(_ context.Context, id int64) {
	o.onInvalidate(id)
}
func (adminBalanceCommitObserver) InvalidateAuthCacheByKey(context.Context, string)    {}
func (adminBalanceCommitObserver) InvalidateAuthCacheByGroupID(context.Context, int64) {}

func newAdminBalanceTestService(users service.UserRepository, codes service.RedeemCodeRepository, invalidator service.APIKeyAuthCacheInvalidator) service.AdminService {
	return service.NewAdminService(users, nil, nil, nil, nil, codes, nil, nil, nil, nil, nil,
		invalidator, integrationEntClient, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}

func TestAdminBalancePostgresAdjustmentCommitsHistoryAndCredit(t *testing.T) {
	activity, uid, cfg := dailyActivityFixture(t)
	cfg.IncludeAdminBalance = true
	_, err := activity.SaveConfig(context.Background(), *cfg)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE users SET frozen_balance=1 WHERE id=$1`, uid)
	require.NoError(t, err)
	users := NewUserRepository(integrationEntClient, integrationDB)
	var observed []float64
	svc := newAdminBalanceTestService(users, NewRedeemCodeRepository(integrationEntClient), adminBalanceCommitObserver{onInvalidate: func(id int64) {
		// 独立连接能看到新余额，证明缓存副作用在事务提交之后执行。
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var balance float64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, id).Scan(&balance))
		observed = append(observed, balance)
	}})
	for _, step := range []struct {
		op           string
		amount, want float64
	}{{"add", 3, 8}, {"set", 12, 12}, {"subtract", 2, 10}, {"set", 10, 10}} {
		user, err := svc.UpdateUserBalance(context.Background(), uid, step.amount, step.op, "原子调账测试")
		require.NoError(t, err)
		require.Equal(t, step.want, user.Balance)
	}
	_, err = svc.UpdateUserBalance(context.Background(), uid, 99, "subtract", "不能扩大欠费")
	require.ErrorContains(t, err, "balance cannot be negative")
	require.Equal(t, []float64{8, 12, 10}, observed)
	var count int
	var historyTotal, frozen, totalRecharged float64
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*),sum(rc.value) FROM redeem_code_usages ru JOIN redeem_codes rc ON rc.id=ru.redeem_code_id WHERE ru.user_id=$1`, uid).Scan(&count, &historyTotal))
	require.Equal(t, 3, count)
	require.Equal(t, 5.0, historyTotal)
	require.NoError(t, integrationDB.QueryRow(`SELECT frozen_balance,total_recharged FROM users WHERE id=$1`, uid).Scan(&frozen, &totalRecharged))
	require.Equal(t, 1.0, frozen)
	require.Zero(t, totalRecharged)
	state, err := activity.Status(context.Background(), uid)
	require.NoError(t, err)
	// 正数新增 3+4 计入；减款与无变更的 set 不增加或重置已保存资格。
	require.Equal(t, 7.0, state.Activities[0].EligibleAmount)
	require.Equal(t, 7.0, state.Activities[1].EligibleAmount)
}

func TestAdminBalancePostgresHistoryOrReadFailureRollsBackFunds(t *testing.T) {
	for _, stage := range []string{"usage", "return_snapshot"} {
		t.Run(stage, func(t *testing.T) {
			activity, uid, cfg := dailyActivityFixture(t)
			cfg.IncludeAdminBalance = true
			_, err := activity.SaveConfig(context.Background(), *cfg)
			require.NoError(t, err)
			failure := errors.New("injected adjustment failure")
			users := NewUserRepository(integrationEntClient, integrationDB)
			codes := NewRedeemCodeRepository(integrationEntClient)
			if stage == "usage" {
				codes = adminBalanceFailingRedeemRepo{codes, failure}
			} else {
				users = adminBalanceFailingReadRepo{users, failure}
			}
			invalidations := 0
			svc := newAdminBalanceTestService(users, codes, adminBalanceCommitObserver{onInvalidate: func(int64) { invalidations++ }})
			user, err := svc.UpdateUserBalance(context.Background(), uid, 7, "add", "必须回滚")
			require.ErrorIs(t, err, failure)
			require.Nil(t, user)
			require.Zero(t, invalidations)
			var balance float64
			var codesCount, usagesCount, creditCount int
			require.NoError(t, integrationDB.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
			require.Equal(t, 5.0, balance)
			require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM redeem_codes WHERE used_by=$1`, uid).Scan(&codesCount))
			require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM redeem_code_usages WHERE user_id=$1`, uid).Scan(&usagesCount))
			require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM daily_activity_credit_events WHERE user_id=$1`, uid).Scan(&creditCount))
			require.Zero(t, codesCount)
			require.Zero(t, usagesCount)
			require.Zero(t, creditCount)
		})
	}
}

func TestAdminBalancePostgresConcurrentSetDoesNotInflateCredit(t *testing.T) {
	activity, uid, cfg := dailyActivityFixture(t)
	cfg.IncludeAdminBalance = true
	_, err := activity.SaveConfig(context.Background(), *cfg)
	require.NoError(t, err)
	svc := newAdminBalanceTestService(NewUserRepository(integrationEntClient, integrationDB), NewRedeemCodeRepository(integrationEntClient), nil)
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.UpdateUserBalance(context.Background(), uid, 10, "set", "并发设置")
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var balance, credit float64
	var count int
	require.NoError(t, integrationDB.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*),sum(amount) FROM daily_activity_credit_events WHERE user_id=$1`, uid).Scan(&count, &credit))
	require.Equal(t, 10.0, balance)
	require.Equal(t, 1, count)
	require.Equal(t, 5.0, credit)
}

func TestAdminBalancePostgresSetReportsLockedPreviousBalance(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{Balance: 5})
	repo := NewUserRepository(client, integrationDB)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = repo.AdjustBalance(dbent.NewTxContext(ctx, tx), u.ID, 3)
	require.NoError(t, err)
	type outcome struct {
		change service.BalanceChange
		err    error
	}
	result := make(chan outcome, 1)
	go func() {
		change, err := repo.SetBalance(ctx, u.ID, 10)
		result <- outcome{change, err}
	}()
	// 确认 set 已持有旧语句快照并等待用户锁，再提交并发加款。
	require.Eventually(t, func() bool {
		var blocked bool
		err := integrationDB.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%RETURNING prev.balance, u.balance%')`).Scan(&blocked)
		return err == nil && blocked
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, tx.Commit())
	select {
	case got := <-result:
		require.NoError(t, got.err)
		require.Equal(t, service.BalanceChange{Old: 8, New: 10}, got.change)
	case <-time.After(5 * time.Second):
		t.Fatal("set balance did not resume after concurrent commit")
	}
}

func TestAdminBalancePostgresTransactionReadsOwnBalanceAndGroups(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{Balance: 5})
	repo := NewUserRepository(client, integrationDB)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	_, err = repo.AdjustBalance(txCtx, u.ID, 2)
	require.NoError(t, err)
	allowed, err := tx.Client().Group.Create().SetName("事务内授权组").Save(txCtx)
	require.NoError(t, err)
	disabled, err := tx.Client().Group.Create().SetName("事务内禁用组").Save(txCtx)
	require.NoError(t, err)
	require.NoError(t, repo.AddGroupToAllowedGroups(txCtx, u.ID, allowed.ID))
	_, err = tx.Client().UserDisabledPublicGroup.Create().SetUserID(u.ID).SetGroupID(disabled.ID).Save(txCtx)
	require.NoError(t, err)
	inside, err := repo.GetByID(txCtx, u.ID)
	require.NoError(t, err)
	require.Equal(t, 7.0, inside.Balance)
	require.Equal(t, []int64{allowed.ID}, inside.AllowedGroups)
	require.Equal(t, []int64{disabled.ID}, inside.DisabledPublicGroups)
	// 事务外仍只能看到已提交状态，不把事务内资金和权限泄露给其他请求。
	outside, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, 5.0, outside.Balance)
	require.Empty(t, outside.AllowedGroups)
	require.Empty(t, outside.DisabledPublicGroups)
	require.NoError(t, tx.Rollback())
	after, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, outside, after)
}
