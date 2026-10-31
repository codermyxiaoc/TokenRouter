package service

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"go.uber.org/zap"
)

// CreativeOutput 是执行器返回的一张输出图片。
type CreativeOutput struct {
	Index int
	Bytes []byte
	Mime  string
}

// CreativeExecuteResult 是任务执行结果，由 CreativeRunExecutor 返回。
type CreativeExecuteResult struct {
	Outputs      []CreativeOutput
	AccountID    int64
	ProviderCost float64
	// Usage 是上游返回的 token 用量；未返回时为 nil（token 模式据此按预扣上限兜底并标记待核对）。
	Usage *UsageTokens
}

// CreativeExecution 是一次已经完成账号调度与账号槽位预占的执行上下文。
// worker 在标记任务 running 前创建它，确保并发未准入的任务仍保持 queued。
type CreativeExecution struct {
	Account       *Account
	UpstreamModel string
	Selection     *AccountSelectionResult
	ReleaseFunc   func()
}

// CreativeRunExecutor 抽象创作台任务的上游执行能力。
// 由按平台分派的 HTTP 执行器实现（openai/grok/gemini），不经过本地 HTTP 回环。
type CreativeRunExecutor interface {
	// Prepare 选择可用账号并预占账号并发槽位；暂时没有槽位时返回 ErrCreativeExecutionPending。
	Prepare(ctx context.Context, run CreativeRun) (*CreativeExecution, error)
	// Execute 使用 Prepare 返回的上下文调用上游，不能在此阶段重新选择账号。
	Execute(ctx context.Context, run CreativeRun, payload CreativeRunPayload, execution *CreativeExecution) (*CreativeExecuteResult, error)
	// IsRetryable 判断瞬时错误是否值得有限重试。
	IsRetryable(err error) bool
}

// ErrCreativeExecutionPending 表示任务暂时没有用户或账号执行槽位，应保留 queued 并重排。
var ErrCreativeExecutionPending = errors.New("creative execution is pending concurrency admission")

const (
	defaultCreativeWorkerLockTTL             = 5 * time.Minute
	defaultCreativeWorkerLockConflictDelay   = 5 * time.Second
	defaultCreativeWorkerErrorRetryDelay     = time.Minute
	defaultCreativeWorkerRequeueDelay        = 30 * time.Second
	defaultCreativeWorkerDelayedPollInterval = 5 * time.Second
	defaultCreativeWorkerRecoveryInterval    = 5 * time.Minute
	defaultCreativeWorkerStaleActiveAfter    = 10 * time.Minute
	// 超过任何正常重排间隔（含栅栏巡检和错误重试）仍未推进，才视为脱离队列。
	defaultCreativeWorkerOrphanRecoveryAfter = 20 * time.Minute
	defaultCreativeWorkerDelayedMoveLimit    = 100
	defaultCreativeWorkerRecoverLimit        = 100
	defaultCreativeWorkerErrorBackoff        = time.Second
	defaultCreativeWorkerReserveBlockTimeout = 5 * time.Second
	defaultCreativeConcurrencyRequeueDelay   = time.Second
	// 栅栏等待期间的巡检间隔；到达原执行时限加宽限期后自动失败并释放冻结。
	defaultCreativeUnconfirmedRequeueDelay = time.Minute
	// 原执行者最晚返回后，留给其结果落库的宽限期。
	defaultCreativeUnconfirmedGrace = 2 * time.Minute
)

// CreativeWorkerOptions 是创作台 worker 的运行参数（全部可由配置覆盖）。
type CreativeWorkerOptions struct {
	ReserveBlockTimeout time.Duration
	JobLockTTL          time.Duration
	LockConflictDelay   time.Duration
	DefaultRequeueDelay time.Duration
	ErrorRetryDelay     time.Duration
	ErrorBackoff        time.Duration
	DelayedPollInterval time.Duration
	RecoveryInterval    time.Duration
	StaleActiveAfter    time.Duration
	// OrphanRecoveryAfter 是数据库兜底扫描判定任务已脱离 Redis 队列的静止时长。
	OrphanRecoveryAfter time.Duration
	DelayedMoveLimit    int
	RecoverLimit        int
	MaxAttempts         int
}

// NewCreativeWorkerOptionsFromConfig 从配置构造 worker 运行参数。
func NewCreativeWorkerOptionsFromConfig(cfg *config.Config) CreativeWorkerOptions {
	if cfg == nil {
		return normalizeCreativeWorkerOptions(CreativeWorkerOptions{})
	}
	return normalizeCreativeWorkerOptions(CreativeWorkerOptions{
		JobLockTTL:          time.Duration(cfg.Creative.JobLockTTLSeconds) * time.Second,
		LockConflictDelay:   time.Duration(cfg.Creative.LockConflictDelaySeconds) * time.Second,
		DefaultRequeueDelay: time.Duration(cfg.Creative.DefaultRequeueDelaySeconds) * time.Second,
		ErrorRetryDelay:     time.Duration(cfg.Creative.ErrorRetryDelaySeconds) * time.Second,
		DelayedPollInterval: time.Duration(cfg.Creative.DelayedMoverIntervalSeconds) * time.Second,
		RecoveryInterval:    time.Duration(cfg.Creative.RecoveryIntervalSeconds) * time.Second,
		StaleActiveAfter:    time.Duration(cfg.Creative.StaleActiveAfterSeconds) * time.Second,
		OrphanRecoveryAfter: time.Duration(cfg.Creative.OrphanRecoveryAfterSeconds) * time.Second,
		DelayedMoveLimit:    cfg.Creative.DelayedMoveLimit,
		RecoverLimit:        cfg.Creative.RecoverLimit,
		MaxAttempts:         cfg.Creative.MaxExecuteAttempts,
	})
}

func normalizeCreativeWorkerOptions(opts CreativeWorkerOptions) CreativeWorkerOptions {
	if opts.ReserveBlockTimeout <= 0 {
		opts.ReserveBlockTimeout = defaultCreativeWorkerReserveBlockTimeout
	}
	if opts.JobLockTTL <= 0 {
		opts.JobLockTTL = defaultCreativeWorkerLockTTL
	}
	if opts.LockConflictDelay <= 0 {
		opts.LockConflictDelay = defaultCreativeWorkerLockConflictDelay
	}
	if opts.DefaultRequeueDelay <= 0 {
		opts.DefaultRequeueDelay = defaultCreativeWorkerRequeueDelay
	}
	if opts.ErrorRetryDelay <= 0 {
		opts.ErrorRetryDelay = defaultCreativeWorkerErrorRetryDelay
	}
	if opts.ErrorBackoff <= 0 {
		opts.ErrorBackoff = defaultCreativeWorkerErrorBackoff
	}
	if opts.DelayedPollInterval <= 0 {
		opts.DelayedPollInterval = defaultCreativeWorkerDelayedPollInterval
	}
	if opts.RecoveryInterval <= 0 {
		opts.RecoveryInterval = defaultCreativeWorkerRecoveryInterval
	}
	if opts.StaleActiveAfter <= 0 {
		opts.StaleActiveAfter = defaultCreativeWorkerStaleActiveAfter
	}
	if opts.OrphanRecoveryAfter <= 0 {
		opts.OrphanRecoveryAfter = defaultCreativeWorkerOrphanRecoveryAfter
	}
	if opts.DelayedMoveLimit <= 0 {
		opts.DelayedMoveLimit = defaultCreativeWorkerDelayedMoveLimit
	}
	if opts.RecoverLimit <= 0 {
		opts.RecoverLimit = defaultCreativeWorkerRecoverLimit
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = defaultCreativeMaxAttempts
	}
	return opts
}

// CreativeProcessResult 是单次任务处理结果：Terminal 表示任务可 Ack，否则按 RequeueAfter 重排。
type CreativeProcessResult struct {
	RequeueAfter time.Duration
	Terminal     bool
}

// CreativeRunWorker 是创作台队列 worker：Reserve → 锁 → 执行 → 结算 → Ack/Requeue。
type CreativeRunWorker struct {
	queue       CreativeRunQueue
	repo        CreativeRunRepository
	store       CreativeTransientStore
	executor    CreativeRunExecutor
	service     *CreativePublicService
	concurrency *ConcurrencyService
	opts        CreativeWorkerOptions
	// busy 记录正在处理任务的 worker 数量，供管理端展示当前使用情况。
	busy atomic.Int32
}

type creativeLeaseStateKey struct{}

type creativeLeaseState struct {
	lost atomic.Bool
}

// NewCreativeRunWorker 创建创作台 worker。
func NewCreativeRunWorker(queue CreativeRunQueue, repo CreativeRunRepository, store CreativeTransientStore, executor CreativeRunExecutor, service *CreativePublicService, opts CreativeWorkerOptions, concurrencyServices ...*ConcurrencyService) *CreativeRunWorker {
	var concurrency *ConcurrencyService
	if len(concurrencyServices) > 0 {
		concurrency = concurrencyServices[0]
	}
	return &CreativeRunWorker{
		queue:       queue,
		repo:        repo,
		store:       store,
		executor:    executor,
		service:     service,
		concurrency: concurrency,
		opts:        normalizeCreativeWorkerOptions(opts),
	}
}

// Run 是 worker 主循环；ctx 取消后退出。
func (w *CreativeRunWorker) Run(ctx context.Context) {
	w.RunUntilStopped(ctx, nil)
}

// RunUntilStopped 运行一个可优雅排空的 worker；stop 关闭后不再领取新任务。
func (w *CreativeRunWorker) RunUntilStopped(ctx context.Context, stop <-chan struct{}) {
	if w == nil {
		return
	}
	for {
		if ctx.Err() != nil || creativeWorkerStopRequested(stop) {
			return
		}
		if err := w.runOnce(ctx, stop); err != nil && ctx.Err() == nil {
			if !sleepOrCreativeWorkerStop(ctx, w.opts.ErrorBackoff, stop) {
				return
			}
		}
	}
}

func creativeWorkerStopRequested(stop <-chan struct{}) bool {
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func sleepOrCreativeWorkerStop(ctx context.Context, delay time.Duration, stop <-chan struct{}) bool {
	if delay <= 0 {
		return ctx.Err() == nil && !creativeWorkerStopRequested(stop)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}

// BusyCount 返回正在处理任务的 worker 数量。
func (w *CreativeRunWorker) BusyCount() int {
	if w == nil {
		return 0
	}
	return int(w.busy.Load())
}

// RunOnce 处理一个队列任务。
func (w *CreativeRunWorker) RunOnce(ctx context.Context) error {
	return w.runOnce(ctx, nil)
}

func (w *CreativeRunWorker) runOnce(ctx context.Context, stop <-chan struct{}) error {
	if w == nil || w.queue == nil || w.repo == nil || w.service == nil || w.executor == nil {
		return nil
	}

	reserved, err := w.queue.Reserve(ctx, w.opts.ReserveBlockTimeout)
	if errors.Is(err, ErrCreativeQueueEmpty) {
		return nil
	}
	if err != nil {
		return err
	}
	if creativeWorkerStopRequested(stop) {
		// 缩容信号在 Reserve 阻塞期间到达时，把刚取出的任务立即放回 ready。
		return w.queue.RequeueAfter(ctx, reserved.RunID, reserved.LeaseToken, 0)
	}

	lock, ok, err := w.queue.TryAcquireJobLock(ctx, reserved.RunID, w.opts.JobLockTTL)
	if err != nil {
		if requeueErr := w.queue.RequeueAfter(ctx, reserved.RunID, reserved.LeaseToken, w.opts.LockConflictDelay); requeueErr != nil {
			return requeueErr
		}
		return err
	}
	if !ok {
		// 锁被其他实例持有：按冲突延迟重新入队，避免任务滞留 active 停摆。
		return w.queue.RequeueAfter(ctx, reserved.RunID, reserved.LeaseToken, w.opts.LockConflictDelay)
	}
	defer func() {
		_ = lock.Release(ctx)
	}()

	// 处理期间持续心跳：刷新 active 时间戳防止 stale 恢复误重投，并续期锁。
	hbStop := make(chan struct{})
	hbDone := make(chan struct{})
	leaseLost := make(chan struct{}, 1)
	processCtx, cancelProcess := context.WithCancel(ctx)
	leaseState := &creativeLeaseState{}
	processCtx = context.WithValue(processCtx, creativeLeaseStateKey{}, leaseState)
	go w.runJobHeartbeat(processCtx, reserved.RunID, reserved.LeaseToken, lock, hbStop, hbDone, leaseLost, cancelProcess, leaseState)

	w.busy.Add(1)
	result, processErr := w.process(processCtx, reserved.RunID)
	w.busy.Add(-1)
	close(hbStop)
	<-hbDone
	cancelProcess()
	select {
	case <-leaseLost:
		// 当前 worker 已失去队列租约，不能再 ACK、重排队或提交任何结果。
		return ErrCreativeLeaseLost
	default:
	}
	if processErr != nil {
		logger.L().Warn("creative.worker_process_failed",
			zap.String("run_id", reserved.RunID),
			zap.Error(processErr),
		)
		return w.queue.RequeueAfter(ctx, reserved.RunID, reserved.LeaseToken, w.opts.ErrorRetryDelay)
	}
	if result.Terminal {
		return w.queue.Ack(ctx, reserved.RunID, reserved.LeaseToken)
	}
	delay := result.RequeueAfter
	if delay <= 0 {
		delay = w.opts.DefaultRequeueDelay
	}
	return w.queue.RequeueAfter(ctx, reserved.RunID, reserved.LeaseToken, delay)
}

// process 处理单个任务：加载载荷 → 执行 → 结算。所有结算动作幂等。
func (w *CreativeRunWorker) process(ctx context.Context, runID string) (CreativeProcessResult, error) {
	run, err := w.repo.GetCreativeRunByRunID(ctx, runID)
	if err != nil {
		if errors.Is(err, ErrCreativeRunNotFound) {
			// 任务已被删除：直接出队。
			return CreativeProcessResult{Terminal: true}, nil
		}
		return CreativeProcessResult{}, err
	}
	if run == nil {
		return CreativeProcessResult{}, errors.New("creative run is unavailable")
	}
	if IsTerminalCreativeRunStatus(run.Status) {
		return CreativeProcessResult{Terminal: true}, nil
	}
	if run.Status == CreativeRunStatusReleasePending {
		if err := w.service.ReleaseRun(ctx, runID); err != nil {
			if _, incErr := w.repo.IncrementCreativeRunReleaseAttempt(ctx, runID); incErr != nil {
				return CreativeProcessResult{}, incErr
			}
			_ = w.repo.SetCreativeRunReconcileError(ctx, runID, sanitizeCreativeMessage(err.Error()), time.Now().Add(w.opts.ErrorRetryDelay))
			return CreativeProcessResult{}, err
		}
		_ = w.repo.SetCreativeRunReconcileError(ctx, runID, "", time.Time{})
		return CreativeProcessResult{Terminal: true}, nil
	}
	if IsCreativeRunSettlementPending(run.Status) {
		if err := w.service.SettleRun(ctx, runID); err != nil {
			attempts, incErr := w.repo.IncrementCreativeRunSettlementAttempt(ctx, runID)
			if incErr != nil {
				return CreativeProcessResult{}, incErr
			}
			_ = w.repo.SetCreativeRunReconcileError(ctx, runID, sanitizeCreativeMessage(err.Error()), time.Now().Add(w.opts.ErrorRetryDelay))
			if attempts >= w.opts.MaxAttempts*2 {
				logger.L().Warn("creative.worker_settlement_pending_retries_exhausted",
					zap.String("run_id", runID),
					zap.Int("attempts", attempts),
					zap.Error(err),
				)
			}
			return CreativeProcessResult{RequeueAfter: w.opts.ErrorRetryDelay}, nil
		}
		_ = w.repo.SetCreativeRunReconcileError(ctx, runID, "", time.Time{})
		return CreativeProcessResult{Terminal: true}, nil
	}
	if recovered, recoverErr := w.recoverPersistedProviderResult(ctx, run); recoverErr != nil {
		return CreativeProcessResult{}, recoverErr
	} else if recovered {
		return CreativeProcessResult{Terminal: true}, nil
	}
	if run.ProvisioningPhase == CreativeProvisioningPhaseProviderInflight {
		return w.deferUnconfirmedExecution(ctx, run)
	}

	if run.Operation != CreativeOperationTTS {
		// 历史任务已有执行计数却缺少账号证据，不能升级后重新获得两次发送资格。
		if run.AttemptCount > 0 && len(run.ImageAttemptAccountIDs) == 0 && (run.AccountID == nil || *run.AccountID <= 0) {
			if err := w.service.MarkRunning(ctx, runID, 0); err != nil {
				return CreativeProcessResult{}, err
			}
			if err := w.repo.SetCreativeRunProvisioningPhase(ctx, runID, CreativeProvisioningPhaseProviderInflight); err != nil {
				return CreativeProcessResult{}, err
			}
			current, err := w.repo.GetCreativeRunByRunID(ctx, runID)
			if err != nil {
				return CreativeProcessResult{}, err
			}
			return w.deferUnconfirmedExecution(ctx, current)
		}
		if len(run.ImageAttemptAccountIDs) >= ImageAttemptMaxSends || run.AttemptCount >= ImageAttemptMaxSends || run.ImageExecutionDeadlineAt != nil && !time.Now().Before(*run.ImageExecutionDeadlineAt) {
			if err := w.service.FailRun(ctx, runID, "IMAGE_EXECUTION_BUDGET_EXHAUSTED", "图片生成的备用账号或总等待时间已用完"); err != nil {
				return CreativeProcessResult{}, err
			}
			return CreativeProcessResult{Terminal: true}, nil
		}
	}

	payload, err := w.loadPayload(ctx, runID)
	if err != nil {
		if errors.Is(err, ErrCreativeTransientUnavailable) {
			// Redis 基础设施故障不能变成终态；保持 active 由调用方重排队。
			return CreativeProcessResult{}, err
		}
		// 载荷/输入过期或损坏：provider 未执行，按 result_lost 处理并释放预占。
		if markErr := w.service.MarkResultLost(ctx, runID, false); markErr != nil {
			logger.L().Warn("creative.worker_mark_result_lost_failed",
				zap.String("run_id", runID),
				zap.Error(markErr),
			)
			return CreativeProcessResult{}, markErr
		}
		return CreativeProcessResult{Terminal: true}, nil
	}
	// 异步任务只在真正执行阶段占用用户并发槽位；未获取槽位时让出 worker 并重排任务。
	var userRelease func()
	if w.concurrency != nil && w.service.UserRepo != nil {
		user, userErr := w.service.UserRepo.GetByID(ctx, run.UserID)
		if userErr != nil {
			if errors.Is(userErr, ErrUserNotFound) {
				_ = w.service.MarkResultLost(ctx, runID, false)
				return CreativeProcessResult{Terminal: true}, nil
			}
			return CreativeProcessResult{}, userErr
		}
		if user == nil {
			return CreativeProcessResult{}, errors.New("creative run user is unavailable")
		}
		acquired, acquireErr := w.concurrency.AcquireUserSlot(ctx, run.UserID, user.Concurrency)
		if acquireErr != nil {
			return CreativeProcessResult{}, acquireErr
		}
		if acquired == nil || !acquired.Acquired {
			return CreativeProcessResult{RequeueAfter: defaultCreativeConcurrencyRequeueDelay}, nil
		}
		userRelease = acquired.ReleaseFunc
		defer func() {
			if userRelease != nil {
				userRelease()
			}
		}()
	}

	execution, err := w.executor.Prepare(ctx, *run)
	if errors.Is(err, ErrCreativeExecutionPending) {
		return CreativeProcessResult{RequeueAfter: defaultCreativeConcurrencyRequeueDelay}, nil
	}
	if err != nil {
		if run.Operation != CreativeOperationTTS {
			var local *CreativeUpstreamError
			if errors.Is(err, ErrCreativeImageBudgetExhausted) || errors.As(err, &local) && !local.Retryable {
				return w.failCreativeImageRun(ctx, runID, err)
			}
			// 本地依赖/选号排队没有发出请求，不增加图片尝试次数。
			return CreativeProcessResult{RequeueAfter: defaultCreativeConcurrencyRequeueDelay}, nil
		}
		return w.handleExecuteError(ctx, runID, err)
	}
	if execution == nil || execution.Account == nil {
		return w.handleExecuteError(ctx, runID, errors.New("creative execution account is unavailable"))
	}
	if execution.ReleaseFunc == nil && execution.Selection != nil {
		execution.ReleaseFunc = execution.Selection.ReleaseFunc
	}
	// 槽位只覆盖上游生图阶段；provider 返回后立即释放，结算/计费不占用并发名额。
	releaseSlots := func() {
		if execution.ReleaseFunc != nil {
			execution.ReleaseFunc()
			execution.ReleaseFunc = nil
		}
		if userRelease != nil {
			userRelease()
			userRelease = nil
		}
	}
	defer releaseSlots()

	// 幂等推进 running；账号已在 Prepare 阶段准入，成功结算时再写入实际账号。
	if err := w.service.MarkRunning(ctx, runID, 0); err != nil {
		return CreativeProcessResult{}, err
	}
	// 先持久化实际账号，再调用 provider，确保结果元数据已落库时可恢复结算。
	if run.Operation == CreativeOperationTTS && execution.Account.ID > 0 {
		if err := w.repo.SetCreativeRunAccountID(ctx, runID, execution.Account.ID, time.Now()); err != nil {
			return CreativeProcessResult{}, err
		}
	}
	// 执行前再次检查：任务已进入 cancelled 则不再调用上游。
	current, err := w.repo.GetCreativeRunByRunID(ctx, runID)
	if err != nil {
		return CreativeProcessResult{}, err
	}
	if current == nil {
		return CreativeProcessResult{}, errors.New("creative run is unavailable")
	}
	if current.Status == CreativeRunStatusCancelled || IsTerminalCreativeRunStatus(current.Status) {
		if err := w.service.CancelRunByWorker(ctx, runID); err != nil {
			return CreativeProcessResult{}, err
		}
		return CreativeProcessResult{Terminal: true}, nil
	}

	// 栅栏必须先于上游请求持久化；即使随后进程退出或成功结果落库失败，也不能再发一次生成。
	if current.Operation == CreativeOperationTTS {
		if err := w.repo.SetCreativeRunProvisioningPhase(ctx, runID, CreativeProvisioningPhaseProviderInflight); err != nil {
			return CreativeProcessResult{}, err
		}
	} else {
		attempts, ok := w.repo.(CreativeImageAttemptRepository)
		if !ok {
			return CreativeProcessResult{}, errors.New("creative image attempt persistence is unavailable")
		}
		claimed, claimErr := attempts.BeginCreativeImageAttempt(ctx, runID, execution.Account.ID, w.creativeImageExecutionBudget(), time.Now())
		if errors.Is(claimErr, ErrCreativeImageBudgetExhausted) {
			return w.failCreativeImageRun(ctx, runID, claimErr)
		}
		if claimErr != nil {
			return CreativeProcessResult{}, claimErr
		}
		current = claimed
	}
	result, err := w.executor.Execute(ctx, *current, *payload, execution)
	releaseSlots()
	if leaseState, _ := ctx.Value(creativeLeaseStateKey{}).(*creativeLeaseState); leaseState != nil && leaseState.lost.Load() {
		if err == nil && result != nil && len(result.Outputs) > 0 {
			// 租约已丢失但成品已到手：先把结果落库交给新的租约持有者结算，不能丢弃用户的成品。
			persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			w.persistCreativeTokenUsage(persistCtx, current, result)
			if _, persistErr := w.service.SucceedRun(persistCtx, runID, result.AccountID, creativeOutputResults(result.Outputs)); persistErr != nil {
				logger.L().Warn("creative.worker_lease_lost_persist_failed",
					zap.String("run_id", runID),
					zap.Error(persistErr),
				)
			}
			cancelPersist()
		}
		return CreativeProcessResult{}, ErrCreativeLeaseLost
	}
	if err != nil {
		if current.Operation != CreativeOperationTTS {
			return w.handleCreativeImageFailure(ctx, current, execution.Account.ID, err)
		}
		if isCreativeResultUnconfirmedError(err) {
			// 上游结果无法确认即视为用户未拿到成品：不重发生成，直接失败并释放冻结。
			return w.releaseUnavailableExecution(ctx, runID)
		}
		if w.executor.IsRetryable(err) {
			// 只有执行器明确返回既有可重试错误时，才授权下一次上游执行。
			if resetErr := w.repo.SetCreativeRunProvisioningPhase(ctx, runID, CreativeProvisioningPhaseProviderRetry); resetErr != nil {
				return CreativeProcessResult{}, resetErr
			}
		}
		return w.handleExecuteError(ctx, runID, err)
	}
	if result == nil || len(result.Outputs) == 0 {
		return w.releaseUnavailableExecution(ctx, runID)
	}
	// token 模式先固定上游用量与扣费金额，再落库输出；之后的结算与恢复都读取这份金额。
	w.persistCreativeTokenUsage(ctx, current, result)
	if _, err := w.service.SucceedRun(ctx, runID, result.AccountID, creativeOutputResults(result.Outputs)); err != nil {
		// provider 已成功：只统计持久化/结算重试，执行栅栏防止再次调用上游。
		_, incErr := w.repo.IncrementCreativeRunSettlementAttempt(ctx, runID)
		if incErr != nil {
			return CreativeProcessResult{}, incErr
		}
		_ = w.repo.SetCreativeRunReconcileError(ctx, runID, sanitizeCreativeMessage(err.Error()), time.Now().Add(w.opts.ErrorRetryDelay))
		return CreativeProcessResult{RequeueAfter: w.opts.ErrorRetryDelay}, nil
	}
	// 优先在当前 lease 内完成结算；失败时 run 已是 provider_succeeded，后续只会重试结算。
	if err := w.service.SettleRun(ctx, runID); err != nil {
		return CreativeProcessResult{}, err
	}
	return CreativeProcessResult{Terminal: true}, nil
}

// persistCreativeTokenUsage 为 token 模式任务保存上游用量与扣费金额；写入失败只记录日志，
// 结算时缺少记录会按预扣上限扣费并标记待核对，不会因此重新调用上游。
func (w *CreativeRunWorker) persistCreativeTokenUsage(ctx context.Context, run *CreativeRun, result *CreativeExecuteResult) {
	if w == nil || w.service == nil || run == nil || result == nil {
		return
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := w.service.recordCreativeTokenUsage(persistCtx, run, result.Usage); err != nil {
		logger.L().Warn("creative.token_usage_persist_failed", zap.String("run_id", run.RunID), zap.Error(err))
	}
}

// deferUnconfirmedExecution 处理启动时发现的执行栅栏：原执行者可能仍在运行，
// 在原执行时限加宽限期内只等待其落库结果；到期仍无结果即视为用户未拿到成品，失败并释放冻结。
// @project-doc docs/domains/creative_studio.md#creative_unconfirmed_release
func (w *CreativeRunWorker) deferUnconfirmedExecution(ctx context.Context, run *CreativeRun) (CreativeProcessResult, error) {
	now := time.Now()
	deadline := w.creativeUnconfirmedDeadline(run)
	if !now.Before(deadline) {
		return w.releaseUnavailableExecution(ctx, run.RunID)
	}
	wait := deadline.Sub(now)
	if wait > defaultCreativeUnconfirmedRequeueDelay {
		wait = defaultCreativeUnconfirmedRequeueDelay
	}
	if err := w.repo.MarkCreativeRunExecutionUnconfirmed(ctx, run.RunID, now.Add(wait)); err != nil {
		return CreativeProcessResult{}, err
	}
	return CreativeProcessResult{RequeueAfter: wait}, nil
}

// creativeUnconfirmedDeadline 只用持久化时间推算原执行者最晚可能返回的时刻，重启或改配置不能无限延长冻结。
func (w *CreativeRunWorker) creativeUnconfirmedDeadline(run *CreativeRun) time.Time {
	if run.Operation != CreativeOperationTTS && run.ImageExecutionDeadlineAt != nil {
		return run.ImageExecutionDeadlineAt.Add(defaultCreativeUnconfirmedGrace)
	}
	start := run.CreatedAt
	if run.StartedAt != nil {
		start = *run.StartedAt
	}
	return start.Add(w.creativeImageExecutionBudget() + defaultCreativeUnconfirmedGrace)
}

// releaseUnavailableExecution 用户没有拿到任何成品时不扣费：先确认没有已落库结果，再失败并释放冻结。
func (w *CreativeRunWorker) releaseUnavailableExecution(ctx context.Context, runID string) (CreativeProcessResult, error) {
	run, err := w.repo.GetCreativeRunByRunID(ctx, runID)
	if err != nil {
		return CreativeProcessResult{}, err
	}
	if recovered, recoverErr := w.recoverPersistedProviderResult(ctx, run); recoverErr != nil {
		return CreativeProcessResult{}, recoverErr
	} else if recovered {
		return CreativeProcessResult{Terminal: true}, nil
	}
	if err := w.service.FailRun(ctx, runID, CreativeResultUnavailableCode, "上游未返回可用结果，未扣费，冻结已释放"); err != nil {
		return CreativeProcessResult{}, err
	}
	return CreativeProcessResult{Terminal: true}, nil
}

// CreativeResultUnavailableCode 表示上游结果超时、未知或不可用，任务已失败且冻结全部释放。
const CreativeResultUnavailableCode = "CREATIVE_RESULT_UNAVAILABLE"

// creativeOutputResults 把执行器输出转换为结果落库参数。
func creativeOutputResults(outputs []CreativeOutput) []CreativeOutputResult {
	results := make([]CreativeOutputResult, 0, len(outputs))
	for _, output := range outputs {
		results = append(results, CreativeOutputResult{
			Index:   output.Index,
			Success: true,
			Bytes:   output.Bytes,
			Mime:    output.Mime,
		})
	}
	return results
}

// loadPayload 从临时存储加载任务载荷与输入字节；缺失即视为结果丢失。
func (w *CreativeRunWorker) loadPayload(ctx context.Context, runID string) (*CreativeRunPayload, error) {
	if w.store == nil {
		return nil, errors.New("creative transient store is not configured")
	}
	payload, err := w.store.LoadPayload(ctx, runID)
	if err != nil {
		return nil, err
	}
	if payload.SourceCount > 0 {
		inputs, err := w.store.LoadInputs(ctx, runID, payload.SourceCount)
		if err != nil {
			return nil, err
		}
		payload.Sources = make([]CreativeInputImage, 0, len(inputs))
		// 载荷中没有逐图 MIME，输入键按字节保存；执行器按魔数嗅探。
		for _, data := range inputs {
			payload.Sources = append(payload.Sources, CreativeInputImage{Bytes: data, Mime: sniffCreativeImageMime(data)})
		}
	}
	if payload.HasMask {
		mask, err := w.store.LoadMask(ctx, runID)
		if err != nil {
			return nil, err
		}
		payload.Mask = &CreativeInputImage{Bytes: mask, Mime: sniffCreativeImageMime(mask)}
	}
	return payload, nil
}

// recoverPersistedProviderResult 检查 provider 已返回但 run 状态写入前进程崩溃的窗口。
// 输出元数据已经落库时无需再次调用 provider，直接补记 provider 成功并进入结算。
func (w *CreativeRunWorker) recoverPersistedProviderResult(ctx context.Context, run *CreativeRun) (bool, error) {
	if w == nil || run == nil || run.Status != CreativeRunStatusRunning {
		return false, nil
	}
	outputs, err := w.repo.ListCreativeRunOutputs(ctx, run.RunID)
	if err != nil {
		return false, err
	}
	if len(outputs) == 0 {
		return false, nil
	}
	successCount := 0
	for _, output := range outputs {
		if output == nil || output.Status == CreativeRunOutputStatusPending {
			return false, nil
		}
		if output.Status == CreativeRunOutputStatusSucceeded || output.Status == CreativeRunOutputStatusAcked {
			successCount++
		}
	}
	if successCount == 0 || run.AccountID == nil || *run.AccountID <= 0 {
		return false, nil
	}
	if err := w.repo.MarkCreativeRunProviderSucceeded(ctx, run.RunID, *run.AccountID, time.Now()); err != nil {
		return false, err
	}
	if err := w.service.ensureCreativeOutbox(ctx, run.RunID, CreativeRunOutboxSettle); err != nil {
		return false, err
	}
	if err := w.service.SettleRun(ctx, run.RunID); err != nil {
		return false, err
	}
	return true, nil
}

// handleExecuteError 处理执行错误：可重试且未达上限 → 递增 attempt 并重排；否则 FailRun 出队。
func (w *CreativeRunWorker) handleExecuteError(ctx context.Context, runID string, execErr error) (CreativeProcessResult, error) {
	if w.executor.IsRetryable(execErr) {
		attempts, err := w.repo.IncrementCreativeRunAttempt(ctx, runID)
		if err != nil {
			return CreativeProcessResult{}, err
		}
		if attempts < w.opts.MaxAttempts {
			return CreativeProcessResult{RequeueAfter: w.opts.ErrorRetryDelay}, nil
		}
		logger.L().Warn("creative.worker_attempts_exhausted",
			zap.String("run_id", runID),
			zap.Int("attempts", attempts),
			zap.Error(execErr),
		)
	}
	code, message := creativeExecuteErrorParts(execErr)
	if err := w.service.FailRun(ctx, runID, code, message); err != nil {
		return CreativeProcessResult{}, err
	}
	return CreativeProcessResult{Terminal: true}, nil
}

// creativeExecuteErrorParts 把执行错误映射为落库的错误码与消息。
func creativeExecuteErrorParts(err error) (string, string) {
	var upstreamErr *CreativeUpstreamError
	if errors.As(err, &upstreamErr) {
		// 只接受本站白名单类别，避免通过新增字段把任意上游文本持久化为公共原因。
		if message := creativeProviderFailureMessage(upstreamErr.Code); message != "" {
			return upstreamErr.Code, message
		}
		code := "PROVIDER_FAILED"
		if upstreamErr.StatusCode > 0 {
			code = "UPSTREAM_STATUS_" + itoaPositive(upstreamErr.StatusCode)
		}
		return code, upstreamErr.Message
	}
	return "PROVIDER_FAILED", sanitizeCreativeMessage(err.Error())
}

func itoaPositive(v int) string {
	if v <= 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

// runJobHeartbeat 处理期间持续心跳：刷新 active 时间戳并续期锁。
func (w *CreativeRunWorker) runJobHeartbeat(ctx context.Context, runID, leaseToken string, lock CreativeRunJobLock, stop <-chan struct{}, done chan<- struct{}, leaseLost chan<- struct{}, cancel context.CancelFunc, leaseState *creativeLeaseState) {
	defer close(done)
	ticker := time.NewTicker(w.heartbeatInterval())
	defer ticker.Stop()
	var lost atomic.Bool
	markLost := func() {
		if lost.CompareAndSwap(false, true) {
			if leaseState != nil {
				leaseState.lost.Store(true)
			}
			leaseLost <- struct{}{}
			if cancel != nil {
				cancel()
			}
		}
	}
	// Redis 短暂抖动时锁仍在有效期内，继续执行；只有超过锁有效期的七成仍无法续期才判定失去租约。
	tolerance := w.opts.JobLockTTL
	if w.opts.StaleActiveAfter > 0 && w.opts.StaleActiveAfter < tolerance {
		tolerance = w.opts.StaleActiveAfter
	}
	tolerance = tolerance * 7 / 10
	lastRenewed := time.Now()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			owned, err := w.queue.Heartbeat(ctx, runID, leaseToken)
			if err != nil && ctx.Err() == nil {
				logger.L().Warn("creative.worker_heartbeat_failed",
					zap.String("run_id", runID),
					zap.Error(err),
				)
				if time.Since(lastRenewed) < tolerance {
					continue
				}
				// 长时间无法确认租约归属时按失去租约处理，避免 Redis 分区期间旧 worker 继续写结果。
				markLost()
				return
			} else if err == nil && !owned {
				logger.L().Warn("creative.worker_lease_lost",
					zap.String("run_id", runID),
				)
				markLost()
				return
			}
			if refresher, ok := lock.(CreativeRunJobLockRefresher); ok {
				owned, err := refresher.Refresh(ctx, w.opts.JobLockTTL)
				if err != nil && ctx.Err() == nil {
					logger.L().Warn("creative.worker_lock_refresh_failed",
						zap.String("run_id", runID),
						zap.Error(err),
					)
					if time.Since(lastRenewed) < tolerance {
						continue
					}
					markLost()
					return
				} else if err == nil && !owned {
					logger.L().Warn("creative.worker_lock_lost",
						zap.String("run_id", runID),
					)
					markLost()
					return
				}
			}
			lastRenewed = time.Now()
		}
	}
}

func (w *CreativeRunWorker) heartbeatInterval() time.Duration {
	interval := w.opts.JobLockTTL
	if w.opts.StaleActiveAfter < interval {
		interval = w.opts.StaleActiveAfter
	}
	interval /= 3
	if interval < time.Second {
		interval = time.Second
	}
	return interval
}

// MoveDueDelayedOnce 把到期的 delayed 任务搬回 ready。
func (w *CreativeRunWorker) MoveDueDelayedOnce(ctx context.Context) (int, error) {
	if w == nil || w.queue == nil {
		return 0, nil
	}
	return w.queue.MoveDueDelayedToReady(ctx, w.opts.DelayedMoveLimit)
}

// RunDelayedMover 是 delayed mover 主循环。
func (w *CreativeRunWorker) RunDelayedMover(ctx context.Context) {
	if w == nil {
		return
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		moved, _ := w.MoveDueDelayedOnce(ctx)
		if moved > 0 {
			continue
		}
		sleepOrDone(ctx, w.opts.DelayedPollInterval)
	}
}

// RecoverStaleActiveOnce 把超时未心跳的 active 任务重投回 ready（worker 重启恢复）。
func (w *CreativeRunWorker) RecoverStaleActiveOnce(ctx context.Context) (int, error) {
	if w == nil || w.queue == nil {
		return 0, nil
	}
	return w.queue.RecoverStaleActive(ctx, w.opts.StaleActiveAfter, w.opts.RecoverLimit)
}

// RunStaleActiveRecovery 是 stale active 恢复主循环，同时从数据库兜底恢复脱离队列的任务。
func (w *CreativeRunWorker) RunStaleActiveRecovery(ctx context.Context) {
	if w == nil {
		return
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		_, _ = w.RecoverStaleActiveOnce(ctx)
		if _, err := w.RecoverOrphanedRunsOnce(ctx); err != nil && ctx.Err() == nil {
			logger.L().Warn("creative.worker_orphan_recovery_failed", zap.Error(err))
		}
		sleepOrDone(ctx, w.opts.RecoveryInterval)
	}
}

// CreativeRunQueueRecoveryLister 由仓储实现，列出长时间未推进的非终态任务 ID。
type CreativeRunQueueRecoveryLister interface {
	ListCreativeRunIDsForQueueRecovery(ctx context.Context, staleBefore time.Time, limit int) ([]string, error)
}

// RecoverOrphanedRunsOnce 以数据库为准把 Redis 中已丢失的非终态任务重新入队；
// 已在队列中的任务由去重键跳过，重复投递也只会被任务锁和状态机幂等处理。
func (w *CreativeRunWorker) RecoverOrphanedRunsOnce(ctx context.Context) (int, error) {
	if w == nil || w.queue == nil {
		return 0, nil
	}
	lister, ok := w.repo.(CreativeRunQueueRecoveryLister)
	if !ok {
		return 0, nil
	}
	runIDs, err := lister.ListCreativeRunIDsForQueueRecovery(ctx, time.Now().Add(-w.opts.OrphanRecoveryAfter), w.opts.RecoverLimit)
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, runID := range runIDs {
		err := w.queue.Enqueue(ctx, runID)
		if errors.Is(err, ErrCreativeAlreadyQueued) {
			continue
		}
		if err != nil {
			return recovered, err
		}
		recovered++
	}
	if recovered > 0 {
		logger.L().Warn("creative.worker_orphan_runs_requeued", zap.Int("count", recovered))
	}
	return recovered, nil
}
