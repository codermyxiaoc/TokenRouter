//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

// 真实数据库中64个领取者只能有一个取得发送权；切换账号和重启不更新预占或总截止。
func TestCreativeImageRoutingConcurrentFenceAndSingleSettlement(t *testing.T) {
	f := newCreativeMJIntegrationFixture(t)
	ctx := context.Background()
	run, err := f.svc.CreateRun(ctx, f.scope, f.params(), "routing-two-account-budget")
	require.NoError(t, err)
	require.NoError(t, f.svc.MarkRunning(ctx, run.ID, 0))
	attempts := f.svc.Repo.(service.CreativeImageAttemptRepository)
	start := make(chan struct{})
	errs := make(chan error, 64)
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			<-start
			_, claimErr := attempts.BeginCreativeImageAttempt(ctx, run.ID, f.accountID, 5*time.Minute, time.Now())
			errs <- claimErr
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	winners := 0
	for claimErr := range errs {
		if claimErr == nil {
			winners++
		} else {
			require.ErrorIs(t, claimErr, service.ErrCreativeInvalidTransition)
		}
	}
	require.Equal(t, 1, winners)
	first, err := f.svc.Repo.GetCreativeRunByRunID(ctx, run.ID)
	require.NoError(t, err)
	f.assertMoney(t, 0, 1.36)
	// 模拟进程重建：未知受理必须继续阻止领取，只有原账号明确拒绝才能打开栅栏。
	fresh := NewCreativeRunRepository(integrationEntClient).(service.CreativeImageAttemptRepository)
	_, err = fresh.BeginCreativeImageAttempt(ctx, run.ID, f.accountID, time.Hour, time.Now())
	require.ErrorIs(t, err, service.ErrCreativeInvalidTransition)
	require.NoError(t, fresh.AuthorizeCreativeImageRetry(ctx, run.ID, f.accountID))
	secondAccount := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "image-fallback", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	second, err := fresh.BeginCreativeImageAttempt(ctx, run.ID, secondAccount.ID, time.Hour, time.Now())
	require.NoError(t, err)
	require.Equal(t, first.ImageExecutionDeadlineAt, second.ImageExecutionDeadlineAt)
	require.Equal(t, []int64{f.accountID, secondAccount.ID}, second.ImageAttemptAccountIDs)
	require.ErrorIs(t, fresh.AuthorizeCreativeImageRetry(ctx, run.ID, f.accountID), service.ErrCreativeInvalidTransition)
	_, err = f.svc.SucceedRun(ctx, run.ID, secondAccount.ID, []service.CreativeOutputResult{{Index: 0, Success: true, Bytes: []byte("local-image"), Mime: "image/png"}})
	require.NoError(t, err)
	creativeMJParallel(t, 64, func() error { return f.svc.SettleRun(ctx, run.ID) })
	creativeMJParallel(t, 64, func() error { return f.svc.ReleaseRun(ctx, run.ID) })
	f.assertMoney(t, 0.34, 0)
	var count int
	var billedAccount int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*),MAX(account_id) FROM usage_logs WHERE request_id=$1`, service.CreativeSettlementRequestID(run.ID)).Scan(&count, &billedAccount))
	require.Equal(t, 1, count)
	require.Equal(t, secondAccount.ID, billedAccount)
}

// 历史重试缺少受理证据，升级只收紧未完成图片栅栏，不影响音频、已完成记录或资金。
func TestMigration320PreservesBalancesAndQuarantinesLegacyUnknownImages(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	_, err := tx.ExecContext(ctx, `CREATE TEMP TABLE creative_runs(id BIGINT PRIMARY KEY,account_id BIGINT,operation TEXT,status TEXT,provisioning_phase TEXT,hold_amount NUMERIC) ON COMMIT DROP;
INSERT INTO creative_runs VALUES (1,11,'generate','running','provider_retry',2),(2,12,'edit','running','provider_inflight',3),(3,13,'tts','running','provider_retry',4),(4,14,'generate','succeeded','complete',0);`)
	require.NoError(t, err)
	sql, err := migrations.FS.ReadFile("320_creative_image_attempt_budget.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(sql))
	require.NoError(t, err)
	rows, err := tx.QueryContext(ctx, `SELECT id,provisioning_phase,hold_amount,image_attempt_account_ids::text FROM creative_runs ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var phases []string
	var money []int
	var ids []string
	for rows.Next() {
		var id, cost int
		var phase, list string
		require.NoError(t, rows.Scan(&id, &phase, &cost, &list))
		phases = append(phases, phase)
		money = append(money, cost)
		ids = append(ids, list)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"provider_inflight", "provider_inflight", "provider_retry", "complete"}, phases)
	require.Equal(t, []int{2, 3, 4, 0}, money)
	require.Equal(t, []string{"[11]", "[12]", "[]", "[]"}, ids)
}
