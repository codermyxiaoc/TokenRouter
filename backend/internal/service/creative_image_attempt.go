package service

import (
	"context"
	"errors"
	"slices"
	"time"
)

var ErrCreativeImageBudgetExhausted = errors.New("creative image execution budget exhausted")

// CreativeImageAttemptBudget 只计算持久状态；仓储还需通过 version CAS 原子取得执行权。
func CreativeImageAttemptBudget(run *CreativeRun, accountID int64, budget time.Duration, now time.Time) ([]int64, time.Time, error) {
	if run == nil || run.Operation == CreativeOperationTTS || accountID <= 0 || run.Status != CreativeRunStatusRunning || run.ProvisioningPhase == CreativeProvisioningPhaseProviderInflight || run.ProviderResultRecordedAt != nil {
		return nil, time.Time{}, ErrCreativeInvalidTransition
	}
	ids := append([]int64(nil), run.ImageAttemptAccountIDs...)
	// 兼容升级前明确允许重试的任务，只记住已有账号，不能重新选择该账号。
	if len(ids) == 0 && run.ProvisioningPhase == CreativeProvisioningPhaseProviderRetry && run.AccountID != nil && *run.AccountID > 0 {
		ids = append(ids, *run.AccountID)
	}
	if len(ids) == 0 && run.AttemptCount > 0 {
		return nil, time.Time{}, ErrCreativeImageBudgetExhausted
	}
	if len(ids) >= ImageAttemptMaxSends || run.AttemptCount >= ImageAttemptMaxSends || slices.Contains(ids, accountID) {
		return nil, time.Time{}, ErrCreativeImageBudgetExhausted
	}
	if budget <= 0 {
		budget = defaultCreativeExecuteTimeout
	}
	deadline := now.Add(budget)
	if run.ImageExecutionDeadlineAt != nil {
		deadline = *run.ImageExecutionDeadlineAt
	} else if run.StartedAt != nil {
		deadline = run.StartedAt.Add(budget)
	} else if len(ids) > 0 && !run.CreatedAt.IsZero() {
		// 仅历史执行缺少 started_at 时使用最保守的创建时间；新任务排队不能占用执行预算。
		deadline = run.CreatedAt.Add(budget)
	}
	if !now.Before(deadline) {
		return nil, time.Time{}, ErrCreativeImageBudgetExhausted
	}
	return append(ids, accountID), deadline, nil
}

// HTTP 和传输层分别提供受理证据，不能将读取失败伪装成可安全重试的 HTTP 502。
func creativeImageHTTPFailure(ctx context.Context, status int, body []byte) error {
	decision := ClassifyImageHTTPFailure(status, body)
	if decision.Outcome != ImageAttemptRejected && decision.Outcome != ImageAttemptNotSent {
		return RecordImageAttemptFailure(ctx, errors.Join(creativeResultUnconfirmedError(), creativeHTTPResponseError(status, body)), decision)
	}
	return RecordImageAttemptFailure(ctx, creativeHTTPResponseError(status, body), decision)
}
func creativeImageTransportFailure(ctx context.Context, err error) error {
	decision := ClassifyImageTransportFailure(err)
	if decision.Outcome == ImageAttemptUnknown {
		return RecordImageAttemptFailure(ctx, errors.Join(creativeResultUnconfirmedError(), creativeHTTPStatusError(0, err.Error())), decision)
	}
	return RecordImageAttemptFailure(ctx, creativeHTTPStatusError(0, err.Error()), decision)
}
func creativeImageReadFailure(ctx context.Context) error {
	// 租约/进程主动取消不代表账号故障；发送后的未知状态仍不得重发。
	return RecordImageAttemptFailure(ctx, creativeResultUnconfirmedError(), ImageAttemptDecision{Outcome: ImageAttemptUnknown, AttemptFault: !errors.Is(ctx.Err(), context.Canceled), Reason: "image_response_interrupted"})
}
func creativeImageAccountPreparationFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return RecordImageAttemptFailure(ctx, creativeNonRetryableError("creative image request was cancelled before submission"), ImageAttemptDecision{Outcome: ImageAttemptNotSent, Reason: "image_request_cancelled"})
	}
	return RecordImageAttemptFailure(ctx, creativeNonRetryableError("creative account could not prepare image request"), ImageAttemptDecision{Outcome: ImageAttemptNotSent, RetryDifferentAccount: true, AttemptFault: true, Reason: "image_account_prepare_failed"})
}

// 显式发送前拒绝仍应结束任务并释放预占，不留作上游未知受理。
func creativeImageLocalFailure(err error) error {
	return WrapImageAttemptError(err, ImageAttemptDecision{Outcome: ImageAttemptNotSent, Reason: "image_request_not_sent"})
}
func creativeImageOutputFailure(ctx context.Context, status int, body []byte, err error) error {
	decision := ClassifyImageHTTPFailure(status, body)
	if decision.Outcome == ImageAttemptRejected {
		return RecordImageAttemptFailure(ctx, creativeHTTPResponseError(status, body), decision)
	}
	return RecordImageAttemptFailure(ctx, creativeResultUnconfirmedError(), decision)
}

// 只把明确的账号故障反馈给调度和短暂图片模型冷却；参数、审核和结果持久化不处罚账号。
func (e *CreativeExecutor) recordCreativeImageFailure(ctx context.Context, run CreativeRun, execution *CreativeExecution, err error) {
	decision, ok := ImageAttemptDecisionForError(err)
	if !ok || !decision.AttemptFault || execution == nil || execution.Account == nil {
		return
	}
	e.reportScheduleResult(execution, execution.Account.ID, false)
	healthCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if execution.Account.Platform == PlatformGemini && e.gatewayService != nil {
		_ = e.gatewayService.RecordImageModelFailure(healthCtx, execution.Account.ID, execution.UpstreamModel, run.Operation, decision)
	} else if e.gateway != nil {
		_ = e.gateway.RecordImageModelFailure(healthCtx, execution.Account.ID, execution.UpstreamModel, run.Operation, decision)
	}
}
