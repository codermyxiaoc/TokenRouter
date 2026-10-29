//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/stretchr/testify/require"
)

// 调度替身保留真实 Prepare 入口，只记录能力需求并返回指定账号与槽位。
type creativeMJSafetyScheduler struct {
	OpenAIAccountScheduler
	accounts []*Account
	requests []OpenAIAccountScheduleRequest
	released map[int64]int
}

func (s *creativeMJSafetyScheduler) Select(_ context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	s.requests = append(s.requests, req)
	if len(s.accounts) == 0 {
		return nil, OpenAIAccountScheduleDecision{}, ErrNoAvailableAccounts
	}
	account := s.accounts[0]
	s.accounts = s.accounts[1:]
	return &AccountSelectionResult{Account: account, Acquired: true, ReleaseFunc: func() { s.released[account.ID]++ }}, OpenAIAccountScheduleDecision{}, nil
}

// MJ 原名、账号别名和错误映射都走最终模型校验，不能落到 OAuth 付费生成。
func TestCreativePrepareMidjourneyAPIKeyIsolation(t *testing.T) {
	for _, tc := range []struct {
		name         string
		model        string
		accounts     []*Account
		capabilities []OpenAIImagesCapability
		wantAccount  int64
		wantError    bool
	}{
		{name: "direct_midjourney", model: "MJ-v7", accounts: []*Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}}, capabilities: []OpenAIImagesCapability{OpenAIImagesCapabilityAPIKey}, wantAccount: 1},
		{name: "gpt_keeps_oauth", model: "gpt-image-2", accounts: []*Account{{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}}, capabilities: []OpenAIImagesCapability{OpenAIImagesCapabilityNative}, wantAccount: 2},
		{name: "mapped_alias_reselects", model: "image-alias", accounts: []*Account{
			{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"image-alias": "MJ-v8.1"}}},
			{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"image-alias": "MJ-v8.1"}}},
		}, capabilities: []OpenAIImagesCapability{OpenAIImagesCapabilityNative, OpenAIImagesCapabilityAPIKey}, wantAccount: 4},
		{name: "mapped_text_rejected", model: "MJ-v7", accounts: []*Account{{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"MJ-v7": "gpt-5"}}}}, capabilities: []OpenAIImagesCapability{OpenAIImagesCapabilityAPIKey}, wantError: true},
		{name: "invalid_scheduler_oauth_rejected", model: "MJ-v8.2", accounts: []*Account{{ID: 6, Platform: PlatformOpenAI, Type: AccountTypeOAuth}}, capabilities: []OpenAIImagesCapability{OpenAIImagesCapabilityAPIKey}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := append([]*Account(nil), tc.accounts...)
			scheduler := &creativeMJSafetyScheduler{accounts: accounts, released: map[int64]int{}}
			group := &Group{ID: 12, Platform: PlatformOpenAI, Status: StatusActive}
			executor := &CreativeExecutor{gateway: &OpenAIGatewayService{openaiScheduler: scheduler}, groupRepo: &creativeFakeGroupRepo{byID: map[int64]*Group{12: group}}}
			execution, err := executor.Prepare(withAdvancedSchedulerTestGroup(context.Background(), 12), CreativeRun{GroupID: 12, Model: tc.model, Operation: CreativeOperationGenerate})
			if tc.wantError {
				require.Error(t, err)
				require.False(t, IsRetryableCreativeError(err))
				require.Nil(t, execution)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.wantAccount, execution.Account.ID)
				require.Zero(t, scheduler.released[execution.Account.ID])
				execution.ReleaseFunc()
			}
			require.Len(t, scheduler.requests, len(tc.capabilities))
			for i, capability := range tc.capabilities {
				require.Equal(t, capability, scheduler.requests[i].RequiredImageCapability)
			}
			for _, account := range tc.accounts {
				require.Equal(t, 1, scheduler.released[account.ID], "账号 %d 的槽位必须且只能释放一次", account.ID)
			}
		})
	}
}

// 内部调用绕过 Prepare 仍不能把 MJ 发到 OAuth、SetupToken 或其它平台。
func TestCreativeExecuteMidjourneyRejectsUnsupportedAccounts(t *testing.T) {
	for _, account := range []*Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeSetupToken},
		{ID: 3, Platform: PlatformGemini, Type: AccountTypeAPIKey},
	} {
		_, err := (&CreativeExecutor{}).Execute(context.Background(), CreativeRun{}, CreativeRunPayload{}, &CreativeExecution{Account: account, UpstreamModel: "MJ-v7"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "Midjourney requires")
		require.False(t, IsRetryableCreativeError(err))
	}
}

// 已受理后的不确定错误在包装后仍保持同一语义，且错误本身不保存上游素材或认证信息。
func TestCreativeUnconfirmedErrorClassification(t *testing.T) {
	err := creativeResultUnconfirmedError()
	require.Equal(t, "creative provider result is unconfirmed", err.Error())
	require.True(t, isCreativeResultUnconfirmedError(fmt.Errorf("wrapped: %w", err)))
	require.False(t, IsRetryableCreativeError(err))
	require.False(t, IsRetryableCreativeError(fmt.Errorf("wrapped: %w", err)))
	require.False(t, isCreativeResultUnconfirmedError(creativeHTTPStatusError(400, "rejected")))
}

// 产物无法确认即视为用户未拿到成品：一次执行后失败并释放；队列重投、载荷过期及 worker 重建都不能再次生成或重复退款。
func TestCreativeWorkerMidjourneyUnconfirmedRetainsHoldWithoutReplay(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_mj_unconfirmed"
	seedCreativeRun(f, runID, true)
	run := f.repo.runs[runID]
	run.Model = "MJ-v7"
	f.exec.err = fmt.Errorf("wrapped: %w", creativeResultUnconfirmedError())
	first, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, first.Terminal)
	require.Equal(t, CreativeRunStatusFailed, run.Status)
	require.Equal(t, CreativeResultUnavailableCode, *run.ErrorCode)
	delete(f.store.payloads, runID)
	f.worker = NewCreativeRunWorker(f.queue, f.repo, f.store, f.exec, f.service, f.worker.opts)
	for range 5 {
		next, err := f.worker.process(context.Background(), runID)
		require.NoError(t, err)
		require.True(t, next.Terminal)
	}
	require.Equal(t, 1, f.exec.calls)
	require.Nil(t, run.ActualCost)
	require.Zero(t, f.billing.captureN)
	require.Equal(t, []string{"creative_release:" + runID}, f.billing.releaseIDs)
}

// 明确拒绝继续沿用一次释放路径，不与不确定状态混淆，也不重复退款。
func TestCreativeWorkerMidjourneyExplicitRejectionReleasesOnce(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_mj_rejected"
	seedCreativeRun(f, runID, true)
	f.repo.runs[runID].Model = "MJ-v7"
	f.exec.err = creativeHTTPStatusError(400, "invalid parameters")
	for range 3 {
		result, err := f.worker.process(context.Background(), runID)
		require.NoError(t, err)
		require.True(t, result.Terminal)
	}
	require.Equal(t, CreativeRunStatusFailed, f.repo.runs[runID].Status)
	require.Equal(t, 1, f.exec.calls)
	require.Equal(t, []string{"creative_release:" + runID}, f.billing.releaseIDs)
	require.Zero(t, f.billing.captureN)
}

// 原执行者在栅栏等待期内落库真实输出时，只按原任务结算一次，不调用上游补造结果。
func TestCreativeWorkerMidjourneyUnconfirmedRecoveredOutputSettlesOnce(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_mj_recovered"
	seedCreativeRun(f, runID, true)
	run := f.repo.runs[runID]
	run.Model = "MJ-v8.1"
	run.Status = CreativeRunStatusRunning
	run.ProvisioningPhase = CreativeProvisioningPhaseProviderInflight
	run.ImageAttemptAccountIDs = []int64{55}
	deadline := time.Now().Add(time.Minute)
	run.ImageExecutionDeadlineAt = &deadline
	waiting, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.False(t, waiting.Terminal)
	_, err = f.service.SucceedRun(context.Background(), runID, 55, []CreativeOutputResult{{Index: 0, Success: true, Bytes: []byte("image"), Mime: "image/png"}})
	require.NoError(t, err)
	for range 3 {
		result, err := f.worker.process(context.Background(), runID)
		require.NoError(t, err)
		require.True(t, result.Terminal)
	}
	require.Equal(t, CreativeRunStatusSucceeded, run.Status)
	require.Zero(t, f.exec.calls)
	require.Equal(t, []string{"creative_capture:" + runID}, f.billing.captureIDs)
	require.Zero(t, f.billing.releaseN)
}

// 队列完整入口对不确定任务失败释放后 ACK 出队，并释放任务锁。
func TestCreativeWorkerMidjourneyUnconfirmedRequeuesWithoutAck(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_mj_queue_unconfirmed"
	seedCreativeRun(f, runID, true)
	f.repo.runs[runID].Model = "MJ-v8.2"
	f.exec.err = creativeResultUnconfirmedError()
	f.queue.reserveBatch = []string{runID}
	require.NoError(t, f.worker.RunOnce(context.Background()))
	require.Equal(t, []string{runID}, f.queue.acked)
	require.Empty(t, f.queue.requeued)
	require.True(t, f.queue.lastLock.released)
	require.Equal(t, 1, f.exec.calls)
	require.Zero(t, f.billing.captureN)
	require.Equal(t, 1, f.billing.releaseN)
}

// 固定四张合同防止旧任务或映射变更只预占一张后执行四张生成。
func TestCreativeExecuteMidjourneyRequiresFourImageReservation(t *testing.T) {
	for _, count := range []int{-1, 0, 1, 3, 5} {
		_, err := (&CreativeExecutor{}).Execute(context.Background(), CreativeRun{RequestedOutputCount: count}, CreativeRunPayload{}, &CreativeExecution{
			Account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, UpstreamModel: "MJ-v7",
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "four-image reservation")
		require.False(t, IsRetryableCreativeError(err))
	}
	for _, model := range []string{"gpt-image-2", "grok-imagine-image", "gemini-3.1-flash-image-preview"} {
		_, err := (&CreativeExecutor{}).Execute(context.Background(), CreativeRun{RequestedOutputCount: 4}, CreativeRunPayload{}, &CreativeExecution{
			Account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, UpstreamModel: model,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "four-image reservation requires a Midjourney model")
		require.False(t, IsRetryableCreativeError(err))
	}
}

// MJ 按索引交付不同图片，重复字节保守去重，其它模型继续只交付一张。
func TestNormalizeCreativeMidjourneyOutputs(t *testing.T) {
	outputs := []CreativeOutput{
		{Index: 0, Bytes: []byte("one"), Mime: "image/png"},
		{Index: 1, Bytes: []byte("two"), Mime: "image/png"},
		{Index: 2, Bytes: []byte("three"), Mime: "image/jpeg"},
		{Index: 3, Bytes: []byte("four"), Mime: "image/webp"},
	}
	actual, err := normalizeCreativeOutputsForModel(outputs, "MJ-v7")
	require.NoError(t, err)
	require.Len(t, actual, 4)
	for i := range actual {
		require.Equal(t, i, actual[i].Index)
	}
	actual, err = normalizeCreativeOutputsForModel(outputs, "gpt-image-2")
	require.NoError(t, err)
	require.Len(t, actual, 1)
	outputs[3].Bytes = []byte("one")
	actual, err = normalizeCreativeOutputsForModel(outputs, "MJ-v8.1")
	require.NoError(t, err)
	require.Len(t, actual, 3)
	_, err = normalizeCreativeOutputsForModel(append(outputs, CreativeOutput{Bytes: []byte("five")}), "MJ-v8.2")
	require.True(t, isCreativeResultUnconfirmedError(err))
}

// 记录真实资金命令，仍使用既有测试仓储完成状态推进与幂等终态判断。
type creativeMJBillingRecorder struct {
	UsageBillingRepository
	reserveErr error
	reserved   []*BatchImageBalanceHoldCommand
	captured   []*BatchImageBalanceHoldCommand
}

func (b *creativeMJBillingRecorder) ReserveBatchImageBalance(ctx context.Context, cmd *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	b.reserved = append(b.reserved, cmd)
	if b.reserveErr != nil {
		return nil, b.reserveErr
	}
	return b.UsageBillingRepository.ReserveBatchImageBalance(ctx, cmd)
}

func (b *creativeMJBillingRecorder) CaptureBatchImageBalance(ctx context.Context, cmd *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	b.captured = append(b.captured, cmd)
	return b.UsageBillingRepository.CaptureBatchImageBalance(ctx, cmd)
}

// 四张预占、部分产物结算与重投保持同一计费合同，不追加预占或多次扣款。
func TestCreativeWorkerMidjourneyFourImageBilling(t *testing.T) {
	for _, successCount := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("success_%d", successCount), func(t *testing.T) {
			f := newCreativeWorkerFixture()
			runID := fmt.Sprintf("crun_mj_billing_%d", successCount)
			seedCreativeRun(f, runID, true)
			run := f.repo.runs[runID]
			run.Model, run.RequestedOutputCount, run.BaseUnitPrice = "MJ-v7", 4, 0.34
			run.SubscriptionRateMultiplier, run.BalanceRateMultiplier = 1, 1
			run.EstimatedCost = 1.36
			run.HoldAmount = &run.EstimatedCost
			run.QuotaPlatform = PlatformOpenAI
			f.repo.outputs[runID] = make([]*CreativeRunOutput, 4)
			for i := range f.repo.outputs[runID] {
				f.repo.outputs[runID][i] = &CreativeRunOutput{RunID: runID, OutputIndex: i, Status: CreativeRunOutputStatusPending}
			}
			billing := &creativeMJBillingRecorder{UsageBillingRepository: f.billing}
			f.service.BillingRepo = billing
			require.NoError(t, reserveCreativeBalanceHold(context.Background(), billing, run))
			require.Len(t, billing.reserved, 1)
			require.InDelta(t, 1.36, billing.reserved[0].BaseAmountUSD, 0.00000001)
			require.Equal(t, CreativeHoldRequestID(runID), billing.reserved[0].RequestID)
			f.exec.result = &CreativeExecuteResult{AccountID: 55}
			for i := range successCount {
				f.exec.result.Outputs = append(f.exec.result.Outputs, CreativeOutput{Index: i, Bytes: []byte(fmt.Sprintf("image-%d", i)), Mime: "image/png"})
			}
			for range 3 {
				result, err := f.worker.process(context.Background(), runID)
				require.NoError(t, err)
				require.True(t, result.Terminal)
			}
			require.Equal(t, 1, f.exec.calls)
			require.Len(t, billing.captured, 1)
			require.InDelta(t, float64(successCount)*0.34, billing.captured[0].ActualBaseAmountUSD, 0.00000001)
			require.Equal(t, CreativeCaptureRequestID(runID), billing.captured[0].RequestID)
			require.Equal(t, PlatformOpenAI, billing.captured[0].QuotaPlatform)
			require.InDelta(t, float64(successCount)*0.34, *run.ActualCost, 0.00000001)
			require.Len(t, f.repo.outputs[runID], 4)
			for i, output := range f.repo.outputs[runID] {
				if i < successCount {
					require.Equal(t, CreativeRunOutputStatusSucceeded, output.Status)
				} else {
					require.Equal(t, CreativeRunOutputStatusFailed, output.Status)
				}
			}
			require.Zero(t, f.billing.releaseN)
		})
	}
}

// 四张预算不足时返回明确余额不足，不把失败预占记成已冻结；超合同捕获被本地拒绝。
func TestCreativeMidjourneyBillingRejectsInsufficientAndExcess(t *testing.T) {
	run := &CreativeRun{RunID: "crun_mj_insufficient", APIKeyID: 1, UserID: 1, GroupID: 1, RequestedOutputCount: 4,
		BaseUnitPrice: 0.34, EstimatedCost: 1.36, SubscriptionRateMultiplier: 1, BalanceRateMultiplier: 1}
	billing := &creativeMJBillingRecorder{UsageBillingRepository: &creativeFakeBillingRepo{}, reserveErr: ErrBatchImageInsufficientBalance}
	err := reserveCreativeBalanceHold(context.Background(), billing, run)
	require.ErrorIs(t, err, ErrCreativeInsufficientBalance)
	require.Len(t, billing.reserved, 1)
	require.InDelta(t, 1.36, billing.reserved[0].BaseAmountUSD, 0.00000001)
	require.False(t, run.AllowanceReserved)
	require.Zero(t, run.BalanceHoldAmount)
	for _, count := range []int{-1, 5} {
		_, err := captureCreativeBalanceHold(context.Background(), billing, run, count)
		require.ErrorIs(t, err, ErrCreativeSettlementBillingFail)
	}
	require.Empty(t, billing.captured)
}

// 四张混合付款按原订阅/余额倍率分摊，少生成时释放差额，日志失败重试不改变资金合同。
func TestCreativeMidjourneyMixedCaptureAndUsageRecovery(t *testing.T) {
	for _, tc := range []struct {
		count                        int
		actual, subscription, wallet float64
		subscriptionRelease          float64
	}{
		{count: 1, actual: 0.6, subscription: 0.6, wallet: 0, subscriptionRelease: 0.6},
		{count: 2, actual: 1.2, subscription: 1.2, wallet: 0},
		{count: 3, actual: 1.4, subscription: 1.2, wallet: 0.2},
		{count: 4, actual: 1.6, subscription: 1.2, wallet: 0.4},
	} {
		t.Run(fmt.Sprintf("successful_%d", tc.count), func(t *testing.T) {
			f := newCreativeWorkerFixture()
			runID := fmt.Sprintf("crun_mj_mixed_%d", tc.count)
			seedCreativeRun(f, runID, true)
			run := f.repo.runs[runID]
			subscriptionID, accountID := int64(77), int64(55)
			now := time.Now()
			run.Model, run.RequestedModel, run.RequestedOutputCount = "MJ-v8.1", "MJ-v8.1", 4
			run.Status, run.BaseUnitPrice, run.AccountID = CreativeRunStatusSettlementPending, 1, &accountID
			run.ProviderResultRecordedAt = &now
			run.SubscriptionRateMultiplier, run.BalanceRateMultiplier, run.BalanceHoldAmount = 0.6, 0.2, 0.4
			run.SubscriptionHoldAllocations = []domain.BillingAllocation{{Type: domain.BillingAllocationTypeSubscription,
				SubscriptionID: &subscriptionID, AmountUSD: 1.2, BaseAmountUSD: 2, RateMultiplier: 0.6}}
			run.EstimatedCost = 1.6
			run.HoldAmount = &run.EstimatedCost
			run.QuotaPlatform = PlatformOpenAI
			f.repo.outputs[runID] = make([]*CreativeRunOutput, 4)
			for i := range 4 {
				status := CreativeRunOutputStatusFailed
				if i < tc.count {
					status = CreativeRunOutputStatusSucceeded
					f.store.outputs[fmtInputKey(runID, i)] = []byte(fmt.Sprintf("image-%d", i))
				}
				f.repo.outputs[runID][i] = &CreativeRunOutput{RunID: runID, OutputIndex: i, Status: status}
			}
			cmd, err := buildCreativeHoldCommand(run, CreativeCaptureRequestID(runID), float64(tc.count))
			require.NoError(t, err)
			plan, err := PlanBatchImageBillingCapture(cmd)
			require.NoError(t, err)
			require.InDelta(t, tc.actual, plan.ActualAmountUSD, 0.00000001)
			require.InDelta(t, tc.subscription, plan.SubscriptionAmountUSD, 0.00000001)
			require.InDelta(t, tc.wallet, plan.BalanceAmountUSD, 0.00000001)
			require.InDelta(t, 0.4, plan.BalanceHoldAmount, 0.00000001)
			releasedSubscription := 0.0
			for _, allocation := range plan.SubscriptionReleases {
				releasedSubscription += allocation.AmountUSD
			}
			require.InDelta(t, tc.subscriptionRelease, releasedSubscription, 0.00000001)
			billing := &creativeUsageBillingRepo{}
			logs := &creativeUsageLogRepo{err: errors.New("usage database unavailable")}
			f.service.BillingRepo, f.service.UsageLogRepo = billing, logs
			require.Error(t, f.service.SettleRun(context.Background(), runID))
			require.Equal(t, CreativeRunStatusSettlementPending, run.Status)
			logs.err = nil
			require.NoError(t, f.service.SettleRun(context.Background(), runID))
			require.NoError(t, f.service.SettleRun(context.Background(), runID))
			require.Equal(t, 2, billing.calls)
			require.Len(t, billing.seen, 1)
			require.Len(t, logs.logs, 1)
			log := logs.logs[CreativeSettlementRequestID(runID)]
			require.Equal(t, tc.count, log.ImageCount)
			require.InDelta(t, tc.actual, log.ActualCost, 0.00000001)
			require.InDelta(t, tc.wallet, log.BalanceAmountUSD, 0.00000001)
			require.InDelta(t, tc.subscription, log.SubscriptionAmountUSD, 0.00000001)
			receipt, count, err := creativeRecordedCaptureReceipt(run, f.repo.outputs[runID])
			require.NoError(t, err)
			require.Equal(t, tc.count, count)
			require.InDelta(t, tc.actual, receipt.ActualAmountUSD, 0.00000001)
			require.Zero(t, f.exec.calls)
		})
	}
}

// 用同步锁模拟 Redis 的互斥契约，制造同一任务被多个 worker 同时领取的压力。
type creativeMJConcurrentQueue struct {
	CreativeRunQueue
	runID    string
	lock     sync.Mutex
	entered  sync.WaitGroup
	reserved atomic.Int64
	acked    atomic.Int64
	requeued atomic.Int64
}

func (q *creativeMJConcurrentQueue) Reserve(context.Context, time.Duration) (ReservedCreativeRun, error) {
	return ReservedCreativeRun{RunID: q.runID, LeaseToken: fmt.Sprintf("lease-%d", q.reserved.Add(1))}, nil
}
func (q *creativeMJConcurrentQueue) TryAcquireJobLock(context.Context, string, time.Duration) (CreativeRunJobLock, bool, error) {
	defer q.entered.Done()
	if !q.lock.TryLock() {
		return nil, false, nil
	}
	return q, true, nil
}
func (q *creativeMJConcurrentQueue) Release(context.Context) error { q.lock.Unlock(); return nil }
func (q *creativeMJConcurrentQueue) RequeueAfter(context.Context, string, string, time.Duration) error {
	q.requeued.Add(1)
	return nil
}
func (q *creativeMJConcurrentQueue) Ack(context.Context, string, string) error {
	q.acked.Add(1)
	return nil
}
func (q *creativeMJConcurrentQueue) Heartbeat(context.Context, string, string) (bool, error) {
	return true, nil
}

// 32 个 worker 竞争同一 MJ 任务时只有一个上游调用；不确定结果不扣费，只释放一次。
func TestCreativeWorkerMidjourneyConcurrentDeliveryDoesNotReplay(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_mj_concurrent"
	seedCreativeRun(f, runID, true)
	f.repo.runs[runID].Model = "MJ-v7"
	queue := &creativeMJConcurrentQueue{runID: runID}
	queue.entered.Add(32)
	f.exec.err = creativeResultUnconfirmedError()
	f.exec.onExecute = func(string) { queue.entered.Wait() }
	errors := make(chan error, 32)
	for range 32 {
		go func() {
			worker := NewCreativeRunWorker(queue, f.repo, f.store, f.exec, f.service, f.worker.opts)
			errors <- worker.RunOnce(context.Background())
		}()
	}
	for range 32 {
		require.NoError(t, <-errors)
	}
	require.Equal(t, 1, f.exec.calls)
	require.Equal(t, int64(32), queue.requeued.Load()+queue.acked.Load())
	require.Zero(t, f.billing.captureN)
	require.Equal(t, 1, f.billing.releaseN)
	result, err := f.worker.process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 1, f.exec.calls)
	require.Equal(t, 1, f.billing.releaseN)
}
