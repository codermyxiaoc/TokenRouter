package service

import (
	"context"
	"errors"
	"time"
)

func (w *CreativeRunWorker) creativeImageExecutionBudget() time.Duration {
	if executor, ok := w.executor.(*CreativeExecutor); ok {
		return executor.executeTimeout()
	}
	if w.service != nil && w.service.Config != nil && w.service.Config.Creative.ExecuteTimeoutSeconds > 0 {
		return time.Duration(w.service.Config.Creative.ExecuteTimeoutSeconds) * time.Second
	}
	return defaultCreativeExecuteTimeout
}

func (w *CreativeRunWorker) failCreativeImageRun(ctx context.Context, runID string, err error) (CreativeProcessResult, error) {
	code, message := creativeExecuteErrorParts(err)
	if errors.Is(err, ErrCreativeImageBudgetExhausted) {
		code = "IMAGE_EXECUTION_BUDGET_EXHAUSTED"
		message = "图片生成的备用账号或总等待时间已用完"
	}
	if err := w.service.FailRun(ctx, runID, code, message); err != nil {
		return CreativeProcessResult{}, err
	}
	return CreativeProcessResult{Terminal: true}, nil
}

// 只有明确未执行/拒绝的结果能换号重试；未知、已受理但无图等情况都不重发生成，
// 并按“用户未拿到成品不扣费”直接失败释放。
func (w *CreativeRunWorker) handleCreativeImageFailure(ctx context.Context, run *CreativeRun, accountID int64, execErr error) (CreativeProcessResult, error) {
	decision, classified := ImageAttemptDecisionForError(execErr)
	if !classified || decision.Outcome != ImageAttemptNotSent && decision.Outcome != ImageAttemptRejected {
		return w.releaseUnavailableExecution(ctx, run.RunID)
	}
	attempts, ok := w.repo.(CreativeImageAttemptRepository)
	if !ok {
		return CreativeProcessResult{}, errors.New("creative image attempt persistence is unavailable")
	}
	if err := attempts.AuthorizeCreativeImageRetry(ctx, run.RunID, accountID); err != nil {
		return CreativeProcessResult{}, err
	}
	if ImageAttemptCanSwitch(decision) {
		if _, err := w.repo.IncrementCreativeRunAttempt(ctx, run.RunID); err != nil {
			return CreativeProcessResult{}, err
		}
	}
	if ImageAttemptCanSwitch(decision) && len(run.ImageAttemptAccountIDs) < ImageAttemptMaxSends && run.ImageExecutionDeadlineAt != nil && time.Now().Before(*run.ImageExecutionDeadlineAt) {
		// 当前槽位已经释放；在同一租约中立即选备用账号，不等待旧的60秒重排间隔。
		return w.process(ctx, run.RunID)
	}
	return w.failCreativeImageRun(ctx, run.RunID, execErr)
}
