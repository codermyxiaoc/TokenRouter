//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// creativeMJIntegrationSettings 只为隔离夹具开启明确配置的 MJ 模型。
type creativeMJIntegrationSettings struct{ groupID int64 }

func (creativeMJIntegrationSettings) IsCreativeEnabled(context.Context) bool { return true }

func (s creativeMJIntegrationSettings) GetCreativeModelSettings(context.Context) []service.CreativeModelSetting {
	return []service.CreativeModelSetting{{GroupID: s.groupID, Model: "MJ-v7", Operations: []string{service.CreativeOperationGenerate}}}
}

// creativeMJIntegrationFixture 使用真实 PostgreSQL/Redis 与业务服务，只有生成产物为本地固定字节。
type creativeMJIntegrationFixture struct {
	svc                        *service.CreativePublicService
	userID, groupID, accountID int64
	scope                      service.CreativeRunScope
}

func newCreativeMJIntegrationFixture(t *testing.T) *creativeMJIntegrationFixture {
	t.Helper()
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "mj-billing-" + uuid.NewString() + "@example.com", Balance: 10})
	group := mustCreateGroup(t, client, &service.Group{Name: "mj-images-" + uuid.NewString(), Platform: service.PlatformOpenAI, RateMultiplier: 1})
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET allow_image_generation=TRUE,image_price_1k=0.34 WHERE id=$1`, group.ID)
	require.NoError(t, err)
	account := mustCreateAccount(t, client, &service.Account{Name: "mj-" + uuid.NewString(), Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Credentials: map[string]any{"model_whitelist": []string{"MJ-v7"}}})
	_, err = client.AccountGroup.Create().SetAccountID(account.ID).SetGroupID(group.ID).Save(ctx)
	require.NoError(t, err)
	rdb := testRedis(t)
	cfg := &config.Config{Creative: config.CreativeConfig{Enabled: true, TransientTTLSeconds: 600, DefaultImageSize: "1K"}}
	keyRepo, ok := NewAPIKeyRepository(client, integrationDB).(service.CreativeManagedKeyRepository)
	require.True(t, ok)
	svc := &service.CreativePublicService{
		Repo: NewCreativeRunRepository(client), ApiKeyRepo: keyRepo, UserRepo: NewUserRepository(client, integrationDB),
		AccountRepo: NewAccountRepository(client, integrationDB, nil), GroupRepo: NewGroupRepository(client, integrationDB),
		Queue: NewCreativeQueue(rdb, cfg), TransientStore: NewCreativeTransientStore(rdb, cfg),
		BillingRepo: NewUsageBillingRepository(client, integrationDB), UsageLogRepo: NewUsageLogRepository(client, integrationDB, nil),
		Outbox: NewCreativeRunOutboxRepository(integrationDB), Settings: creativeMJIntegrationSettings{groupID: group.ID}, Config: cfg,
	}
	insertSettlementPlatformQuota(t, user.ID, service.PlatformOpenAI, float64Ptr(100))
	return &creativeMJIntegrationFixture{svc: svc, userID: user.ID, groupID: group.ID, accountID: account.ID,
		scope: service.CreativeRunScope{UserID: user.ID, WorkspaceID: uuid.NewString()}}
}

func (f *creativeMJIntegrationFixture) params() service.CreateCreativeRunParamsPublic {
	return service.CreateCreativeRunParamsPublic{GroupID: f.groupID, Model: "MJ-v7", Operation: service.CreativeOperationGenerate,
		Prompt: "isolated billing test", ImageSize: "1K", AspectRatio: "1:1", OutputCount: 1}
}

// assertMoney 同时核对钱包、冻结和三个平台窗口，防止任务成功但财务或统计丢失。
func (f *creativeMJIntegrationFixture) assertMoney(t *testing.T, spent, frozen float64) {
	t.Helper()
	var balance, actualFrozen float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT balance,frozen_balance FROM users WHERE id=$1`, f.userID).Scan(&balance, &actualFrozen))
	// 余额列保存可用金额，预占时已转入冻结列；两者之和才是未实扣余额。
	require.InDelta(t, 10-spent-frozen, balance, 1e-8)
	require.InDelta(t, frozen, actualFrozen, 1e-8)
	assertSettlementPlatformQuota(t, f.userID, service.PlatformOpenAI, spent)
}

// creativeMJParallel 执行同一业务动作的并发重放，不在 goroutine 内调用 Fatal。
func creativeMJParallel(t *testing.T, count int, fn func() error) {
	t.Helper()
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- fn()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

// TestCreativeMidjourneyBillingFourImagesAndPartialRecovery 覆盖四张和两张成功时的预扣、实扣与重启重放。
func TestCreativeMidjourneyBillingFourImagesAndPartialRecovery(t *testing.T) {
	for _, successful := range []int{4, 2} {
		t.Run(fmt.Sprintf("successful_%d", successful), func(t *testing.T) {
			f := newCreativeMJIntegrationFixture(t)
			ctx := context.Background()
			run, err := f.svc.CreateRun(ctx, f.scope, f.params(), "four-image-contract")
			require.NoError(t, err)
			require.Equal(t, 4, run.RequestedOutputCount)
			require.InDelta(t, 1.36, run.HoldAmount, 1e-8)
			f.assertMoney(t, 0, 1.36)
			require.NoError(t, f.svc.MarkRunning(ctx, run.ID, f.accountID))
			results := make([]service.CreativeOutputResult, 0, successful)
			for i := 0; i < successful; i++ {
				results = append(results, service.CreativeOutputResult{Index: i, Success: true, Bytes: []byte(fmt.Sprintf("local-output-%d", i)), Mime: "image/png"})
			}
			_, err = f.svc.SucceedRun(ctx, run.ID, f.accountID, results)
			require.NoError(t, err)
			// 价格变更不能修改已冻结的逐张价格合同。
			_, err = integrationDB.ExecContext(ctx, `UPDATE groups SET image_price_1k=99 WHERE id=$1`, f.groupID)
			require.NoError(t, err)
			creativeMJParallel(t, 32, func() error { return f.svc.SettleRun(ctx, run.ID) })
			spent := float64(successful) * 0.34
			f.assertMoney(t, spent, 0)
			fresh, err := f.svc.GetRun(ctx, f.scope, run.ID)
			require.NoError(t, err)
			require.Equal(t, service.CreativeRunStatusSucceeded, fresh.Status)
			require.Len(t, fresh.Outputs, 4)
			for i := 0; i < successful; i++ {
				content, err := f.svc.GetOutputContent(ctx, f.scope, run.ID, i)
				require.NoError(t, err)
				require.Equal(t, []byte(fmt.Sprintf("local-output-%d", i)), content.Content)
			}
			var imageCount, logCount, captureCount int
			var actualCost float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(image_count),0),COALESCE(SUM(actual_cost),0) FROM usage_logs WHERE request_id=$1`, service.CreativeSettlementRequestID(run.ID)).Scan(&logCount, &imageCount, &actualCost))
			require.Equal(t, 1, logCount)
			require.Equal(t, successful, imageCount)
			require.InDelta(t, spent, actualCost, 1e-8)
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1`, service.CreativeCaptureRequestID(run.ID)).Scan(&captureCount))
			require.Equal(t, 1, captureCount)
			// 用新服务和仓储实例模拟重启后的重复结算、迟到退款。
			recovered := &service.CreativePublicService{Repo: NewCreativeRunRepository(integrationEntClient), BillingRepo: NewUsageBillingRepository(integrationEntClient, integrationDB)}
			for i := 0; i < 3; i++ {
				require.NoError(t, recovered.SettleRun(ctx, run.ID))
				require.NoError(t, recovered.ReleaseRun(ctx, run.ID))
			}
			f.assertMoney(t, spent, 0)
		})
	}
}

// TestCreativeMidjourneyBillingConcurrentCreateAndRelease 验证相同幂等请求只能建单并冻结一次。
func TestCreativeMidjourneyBillingConcurrentCreateAndRelease(t *testing.T) {
	f := newCreativeMJIntegrationFixture(t)
	ctx := context.Background()
	creativeMJParallel(t, 64, func() error {
		_, err := f.svc.CreateRun(ctx, f.scope, f.params(), "same-mj-request")
		// 同时越过查询阶段的创建竞争由数据库唯一键拒绝，不允许第二个任务进入预扣。
		if errors.Is(err, service.ErrCreativeRunExists) {
			return nil
		}
		return err
	})
	run, err := f.svc.Repo.GetCreativeRunByIdempotencyKey(ctx, f.scope, "same-mj-request")
	require.NoError(t, err)
	var runs, holds, pending int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM creative_runs WHERE user_id=$1 AND idempotency_key='same-mj-request'`, f.userID).Scan(&runs))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1`, service.CreativeHoldRequestID(run.RunID)).Scan(&holds))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM creative_run_outputs WHERE run_id=$1`, run.RunID).Scan(&pending))
	require.Equal(t, 1, runs)
	require.Equal(t, 1, holds)
	require.Equal(t, 4, pending)
	f.assertMoney(t, 0, 1.36)
	require.NoError(t, f.svc.Repo.TransitionCreativeRunStatus(ctx, run.RunID, service.CreativeRunStatusReleasePending,
		service.CreativeRunTransitionOptions{ReleaseTargetStatus: service.CreativeRunStatusFailed}))
	creativeMJParallel(t, 64, func() error { return f.svc.ReleaseRun(ctx, run.RunID) })
	f.assertMoney(t, 0, 0)
	var releases int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1`, service.CreativeReleaseRequestID(run.RunID)).Scan(&releases))
	require.Equal(t, 1, releases)
	fresh, err := f.svc.Repo.GetCreativeRunByRunID(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, service.CreativeRunStatusFailed, fresh.Status)
	_, err = f.svc.TransientStore.LoadPayload(ctx, run.RunID)
	require.Error(t, err)
	// 再创建不同幂等键必须可以正常获得新的四张预占，旧退款不能释放新任务的冻结额。
	next, err := f.svc.CreateRun(ctx, f.scope, f.params(), "new-mj-request")
	require.NoError(t, err)
	require.NotEqual(t, run.RunID, next.ID)
	require.NoError(t, f.svc.ReleaseRun(ctx, run.RunID))
	f.assertMoney(t, 0, 1.36)
	require.NoError(t, f.svc.FailRun(ctx, next.ID, "TEST_FAILURE", "isolated fixture cleanup"))
	f.assertMoney(t, 0, 0)
}

// TestCreativeMidjourneyBillingInsufficientBalance 四张预占不足时拒绝任务，不能先按一张放行。
func TestCreativeMidjourneyBillingInsufficientBalance(t *testing.T) {
	f := newCreativeMJIntegrationFixture(t)
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `UPDATE users SET balance=0.34 WHERE id=$1`, f.userID)
	require.NoError(t, err)
	_, err = f.svc.CreateRun(ctx, f.scope, f.params(), "insufficient-for-four")
	require.ErrorIs(t, err, service.ErrCreativeInsufficientBalance)
	var balance, frozen float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, f.userID).Scan(&balance, &frozen))
	require.InDelta(t, 0.34, balance, 1e-8)
	require.Zero(t, frozen)
	assertSettlementPlatformQuota(t, f.userID, service.PlatformOpenAI, 0)
	// 创建失败先写释放 outbox，模拟后台补偿后必须终结；没有成功预占时也不能虚增余额。
	run, err := f.svc.Repo.GetCreativeRunByIdempotencyKey(ctx, f.scope, "insufficient-for-four")
	require.NoError(t, err)
	require.Equal(t, service.CreativeRunStatusReleasePending, run.Status)
	require.NoError(t, f.svc.ReleaseRun(ctx, run.RunID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, f.userID).Scan(&balance, &frozen))
	require.InDelta(t, 0.34, balance, 1e-8)
	require.Zero(t, frozen)
	var completed *time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT completed_at FROM creative_runs WHERE user_id=$1`, f.userID).Scan(&completed))
	require.NotNil(t, completed)
}
