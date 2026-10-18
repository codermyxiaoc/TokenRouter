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
	"github.com/stretchr/testify/require"
)

// 所有竞态仅使用 integration_harness 的隔离 PostgreSQL/Redis，不启动 provider worker。
type creativeProvisionRaceFixture struct {
	svc    *service.CreativePublicService
	userID int64
	hold   float64
	start  func(context.Context) (string, error)
}

func newCreativeProvisionRaceFixture(t *testing.T, kind string) creativeProvisionRaceFixture {
	t.Helper()
	if kind == "audio" {
		f := newCreativeAudioIntegrationFixture(t, float64Ptr(20))
		params := f.params()
		params.Voice, params.AudioFormat, params.Speed = "leo", "wav", 1.25
		e, err := f.canvas.CreateExecution(context.Background(), f.userID, f.scope.WorkspaceID, "audio", "audio", params, "provision-race")
		require.NoError(t, err)
		return creativeProvisionRaceFixture{svc: f.svc, userID: f.userID, hold: 0.06, start: func(ctx context.Context) (string, error) {
			f.canvas.Reconcile(ctx)
			fresh, err := f.canvasRepo.GetExecution(context.Background(), f.userID, e.ID)
			if err != nil {
				return "", err
			}
			if fresh.TaskID == "" {
				return "", fmt.Errorf("canvas task not attached: %s %s", fresh.ErrorCode, fresh.ErrorMessage)
			}
			return fresh.TaskID, nil
		}}
	}
	f := newCreativeMJIntegrationFixture(t)
	return creativeProvisionRaceFixture{svc: f.svc, userID: f.userID, hold: 1.36, start: func(ctx context.Context) (string, error) {
		run, err := f.svc.CreateRun(ctx, f.scope, f.params(), "provision-race")
		if err != nil {
			return "", err
		}
		return run.ID, nil
	}}
}

type creativeProvisionRaceGate struct {
	entered chan string
	resume  chan struct{}
	once    sync.Once
}

func newCreativeProvisionRaceGate() *creativeProvisionRaceGate {
	return &creativeProvisionRaceGate{entered: make(chan string, 1), resume: make(chan struct{})}
}
func (g *creativeProvisionRaceGate) unblock() { g.once.Do(func() { close(g.resume) }) }
func (g *creativeProvisionRaceGate) wait(ctx context.Context, runID string) error {
	select {
	case g.entered <- runID:
	default:
	}
	select {
	case <-g.resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// 建行已提交而调用者尚未返回时拦住，专门覆盖 Ensure 之前已有 outbox 的窗口。
type creativeProvisionBlockingRepo struct {
	service.CreativeRunRepository
	gate *creativeProvisionRaceGate
}

func (r creativeProvisionBlockingRepo) CreateCreativeRun(ctx context.Context, p service.CreateCreativeRunParams) (*service.CreativeRun, error) {
	run, err := r.CreativeRunRepository.CreateCreativeRun(ctx, p)
	if err != nil {
		return nil, err
	}
	if err = r.gate.wait(ctx, run.RunID); err != nil {
		return nil, err
	}
	return run, nil
}
func (r creativeProvisionBlockingRepo) SetCreativeRunAllowanceReserved(ctx context.Context, id string, reserved bool) error {
	return r.CreativeRunRepository.(service.CreativeRunAllowanceMarker).SetCreativeRunAllowanceReserved(ctx, id, reserved)
}

type creativeProvisionBlockingBilling struct {
	service.UsageBillingRepository
	gate  *creativeProvisionRaceGate
	after bool
}

func (r creativeProvisionBlockingBilling) ReserveBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	if !r.after {
		if err := r.gate.wait(ctx, cmd.BatchID); err != nil {
			return nil, err
		}
	}
	result, err := r.UsageBillingRepository.ReserveBatchImageBalance(ctx, cmd)
	if err == nil && r.after {
		err = r.gate.wait(ctx, cmd.BatchID)
	}
	return result, err
}

type creativeProvisionBlockingStore struct {
	service.CreativeTransientStore
	gate *creativeProvisionRaceGate
}

func (s creativeProvisionBlockingStore) SavePayload(ctx context.Context, id string, payload *service.CreativeRunPayload) error {
	if err := s.gate.wait(ctx, id); err != nil {
		return err
	}
	return s.CreativeTransientStore.SavePayload(ctx, id, payload)
}

type creativeProvisionStartResult struct {
	id  string
	err error
}

// 注册的清理先解阻并等待创建结束，之后才允许公共夹具截断隔离测试表。
func startCreativeProvisionBlocked(t *testing.T, f creativeProvisionRaceFixture, gate *creativeProvisionRaceGate, ctx context.Context) (<-chan creativeProvisionStartResult, string) {
	t.Helper()
	finished := make(chan creativeProvisionStartResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		id, err := f.start(ctx)
		finished <- creativeProvisionStartResult{id: id, err: err}
	}()
	t.Cleanup(func() {
		gate.unblock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("创建协程未在清理前退出")
		}
	})
	select {
	case id := <-gate.entered:
		return finished, id
	case <-time.After(5 * time.Second):
		t.Fatal("创建未到达预期阻塞阶段")
		return finished, ""
	}
}

func awaitCreativeProvisionStart(t *testing.T, finished <-chan creativeProvisionStartResult) creativeProvisionStartResult {
	t.Helper()
	select {
	case result := <-finished:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("创建未在解阻后结束")
		return creativeProvisionStartResult{}
	}
}

func requireCreativeProvisionMoney(t *testing.T, userID int64, frozen float64) {
	t.Helper()
	var balance, held float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT balance,frozen_balance FROM users WHERE id=$1`, userID).Scan(&balance, &held))
	require.InDelta(t, 10-frozen, balance, 1e-8)
	require.InDelta(t, frozen, held, 1e-8)
}

func requireCreativeProvisionLockReleased(t *testing.T, q service.CreativeRunQueue, runID string) {
	t.Helper()
	lock, owned, err := q.TryAcquireJobLock(context.Background(), runID, time.Second)
	require.NoError(t, err)
	require.True(t, owned, "创建或恢复结束后必须释放任务锁")
	require.NotNil(t, lock)
	require.NoError(t, lock.Release(context.Background()))
}

func TestCreativeProvisionOutboxCannotInterruptActiveCreateIntegration(t *testing.T) {
	for _, kind := range []string{"audio", "image"} {
		for _, stage := range []string{"row_committed_before_ensure", "reserve_before", "reserve_after", "payload"} {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				f := newCreativeProvisionRaceFixture(t, kind)
				baseRepo, baseQueue, baseStore, baseBilling := f.svc.Repo, f.svc.Queue, f.svc.TransientStore, f.svc.BillingRepo
				gate := newCreativeProvisionRaceGate()
				switch stage {
				case "row_committed_before_ensure":
					f.svc.Repo = creativeProvisionBlockingRepo{CreativeRunRepository: baseRepo, gate: gate}
				case "reserve_before", "reserve_after":
					f.svc.BillingRepo = creativeProvisionBlockingBilling{UsageBillingRepository: baseBilling, gate: gate, after: stage == "reserve_after"}
				case "payload":
					f.svc.TransientStore = creativeProvisionBlockingStore{CreativeTransientStore: baseStore, gate: gate}
				}
				finished, runID := startCreativeProvisionBlocked(t, f, gate, context.Background())
				// 第二个服务实例只共享真实 PG/Redis，不能靠进程内 mutex 隐藏竞态。
				recoverer := &service.CreativePublicService{Repo: baseRepo, Queue: baseQueue, TransientStore: baseStore, BillingRepo: baseBilling, Outbox: f.svc.Outbox}
				processed, err := recoverer.ReconcileCreativeOutboxOnce(context.Background())
				require.Error(t, err, "持锁创建必须让 provision 重试而不是处理")
				require.Zero(t, processed)
				fresh, err := baseRepo.GetCreativeRunByRunID(context.Background(), runID)
				require.NoError(t, err)
				require.Equal(t, service.CreativeRunStatusQueued, fresh.Status)
				require.Nil(t, fresh.ErrorCode)
				blockedHold := 0.0
				if stage == "reserve_after" || stage == "payload" {
					blockedHold = f.hold
				}
				requireCreativeProvisionMoney(t, f.userID, blockedHold)
				var releases int
				require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1`, service.CreativeReleaseRequestID(runID)).Scan(&releases))
				require.Zero(t, releases)
				gate.unblock()
				result := awaitCreativeProvisionStart(t, finished)
				require.NoError(t, result.err)
				require.Equal(t, runID, result.id)
				requireCreativeProvisionMoney(t, f.userID, f.hold)
				var holds int
				require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1`, service.CreativeHoldRequestID(runID)).Scan(&holds))
				require.Equal(t, 1, holds)
				requireCreativeProvisionLockReleased(t, baseQueue, runID)
				payload, err := baseStore.LoadPayload(context.Background(), runID)
				require.NoError(t, err)
				if kind == "audio" {
					require.Equal(t, service.CreativeOperationTTS, payload.Operation)
					require.Equal(t, "leo", payload.Voice)
					require.Equal(t, "wav", payload.AudioFormat)
					require.Equal(t, 1.25, payload.Speed)
				}
				// 等待重试不依赖真实时钟：只移动本隔离任务的 outbox 到期时间。
				_, err = integrationDB.ExecContext(context.Background(), `UPDATE creative_run_outbox SET available_at=NOW() WHERE run_id=$1`, runID)
				require.NoError(t, err)
				processed, err = recoverer.ReconcileCreativeOutboxOnce(context.Background())
				require.NoError(t, err)
				require.Equal(t, 1, processed)
				fresh, err = baseRepo.GetCreativeRunByRunID(context.Background(), runID)
				require.NoError(t, err)
				require.Equal(t, service.CreativeRunStatusQueued, fresh.Status)
				require.Equal(t, service.CreativeProvisioningPhaseComplete, fresh.ProvisioningPhase)
				queued, err := baseQueue.Reserve(context.Background(), 100*time.Millisecond)
				require.NoError(t, err)
				require.Equal(t, runID, queued.RunID)
				require.NoError(t, baseQueue.Ack(context.Background(), runID, queued.LeaseToken))
				require.NoError(t, f.svc.FailRun(context.Background(), runID, "TEST_CLEANUP", "isolated test cleanup"))
				requireCreativeProvisionMoney(t, f.userID, 0)
			})
		}
	}
}

type creativeProvisionRejectQueue struct {
	service.CreativeRunQueue
	rejectedRunID *string
}

func (q creativeProvisionRejectQueue) TryAcquireJobLock(_ context.Context, id string, _ time.Duration) (service.CreativeRunJobLock, bool, error) {
	*q.rejectedRunID = id
	return nil, false, nil
}

func TestCreativeProvisionLockFailureDoesNotCreateRunIntegration(t *testing.T) {
	for _, kind := range []string{"audio", "image"} {
		t.Run(kind, func(t *testing.T) {
			f := newCreativeProvisionRaceFixture(t, kind)
			var rejectedRunID string
			f.svc.Queue = creativeProvisionRejectQueue{CreativeRunQueue: f.svc.Queue, rejectedRunID: &rejectedRunID}
			_, err := f.start(context.Background())
			require.Error(t, err)
			require.NotEmpty(t, rejectedRunID)
			var runs, holds int
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FROM creative_runs WHERE user_id=$1`, f.userID).Scan(&runs))
			// 共享测试 schema 会重用用户/Key 序列，必须按本次唯一 runID 排除历史去重账本。
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1`, service.CreativeHoldRequestID(rejectedRunID)).Scan(&holds))
			require.Zero(t, runs)
			require.Zero(t, holds)
			requireCreativeProvisionMoney(t, f.userID, 0)
		})
	}
}

func TestCreativeProvisionInterruptedCreateRecoveredOnceIntegration(t *testing.T) {
	for _, stage := range []string{"created", "hold_reserved"} {
		t.Run(stage, func(t *testing.T) {
			f := newCreativeProvisionRaceFixture(t, "audio")
			baseRepo, baseStore := f.svc.Repo, f.svc.TransientStore
			gate := newCreativeProvisionRaceGate()
			if stage == "created" {
				f.svc.Repo = creativeProvisionBlockingRepo{CreativeRunRepository: baseRepo, gate: gate}
			} else {
				f.svc.TransientStore = creativeProvisionBlockingStore{CreativeTransientStore: baseStore, gate: gate}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished, runID := startCreativeProvisionBlocked(t, f, gate, ctx)
			cancel()
			result := awaitCreativeProvisionStart(t, finished)
			require.Error(t, result.err)
			requireCreativeProvisionLockReleased(t, f.svc.Queue, runID)
			fresh, err := baseRepo.GetCreativeRunByRunID(context.Background(), runID)
			require.NoError(t, err)
			require.Equal(t, stage, fresh.ProvisioningPhase)
			require.Equal(t, service.CreativeRunStatusQueued, fresh.Status)
			_, err = baseStore.LoadPayload(context.Background(), runID)
			require.Error(t, err)
			recoverer := &service.CreativePublicService{Repo: baseRepo, Queue: f.svc.Queue, TransientStore: baseStore, BillingRepo: f.svc.BillingRepo, Outbox: f.svc.Outbox}
			_, err = recoverer.ReconcileCreativeOutboxOnce(context.Background())
			require.NoError(t, err)
			fresh, err = baseRepo.GetCreativeRunByRunID(context.Background(), runID)
			require.NoError(t, err)
			require.Equal(t, service.CreativeRunStatusFailed, fresh.Status)
			requireCreativeProvisionMoney(t, f.userID, 0)
			// 已失败任务的迟到 provision 事件不得重新入队或重复退款。
			for i := 0; i < 3; i++ {
				_, err = integrationDB.ExecContext(context.Background(), `UPDATE creative_run_outbox SET status='pending',available_at=NOW(),lease_token=NULL,lease_until=NULL WHERE run_id=$1`, runID)
				require.NoError(t, err)
				_, err = recoverer.ReconcileCreativeOutboxOnce(context.Background())
				require.NoError(t, err)
			}
			var releases int
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1`, service.CreativeReleaseRequestID(runID)).Scan(&releases))
			require.Equal(t, 1, releases)
			requireCreativeProvisionMoney(t, f.userID, 0)
			_, err = f.svc.Queue.Reserve(context.Background(), 100*time.Millisecond)
			require.ErrorIs(t, err, service.ErrCreativeQueueEmpty)
		})
	}
}

type creativeProvisionUnavailableStore struct{ service.CreativeTransientStore }

func (s creativeProvisionUnavailableStore) LoadPayload(context.Context, string) (*service.CreativeRunPayload, error) {
	return nil, service.ErrCreativeTransientUnavailable
}

func TestCreativeProvisionTransientUnavailableRetainsHoldIntegration(t *testing.T) {
	f := newCreativeProvisionRaceFixture(t, "audio")
	runID, err := f.start(context.Background())
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(context.Background(), `UPDATE creative_runs SET provisioning_phase='transient_saved' WHERE run_id=$1`, runID)
	require.NoError(t, err)
	recoverer := &service.CreativePublicService{Repo: f.svc.Repo, Queue: f.svc.Queue, TransientStore: creativeProvisionUnavailableStore{CreativeTransientStore: f.svc.TransientStore}, BillingRepo: f.svc.BillingRepo, Outbox: f.svc.Outbox}
	processed, err := recoverer.ReconcileCreativeOutboxOnce(context.Background())
	require.Error(t, err)
	require.True(t, errors.Is(err, service.ErrCreativeTransientUnavailable))
	require.Zero(t, processed)
	fresh, err := f.svc.Repo.GetCreativeRunByRunID(context.Background(), runID)
	require.NoError(t, err)
	require.Equal(t, service.CreativeRunStatusQueued, fresh.Status)
	requireCreativeProvisionMoney(t, f.userID, f.hold)
	requireCreativeProvisionLockReleased(t, f.svc.Queue, runID)
	require.NoError(t, f.svc.FailRun(context.Background(), runID, "TEST_CLEANUP", "isolated test cleanup"))
}
