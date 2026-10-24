package repository

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/ent/creativerun"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// BeginCreativeImageAttempt 同时持久化账号排除、唯一截止时间和执行栅栏，崩溃后不能重新发送。
func (r *creativeRunRepository) BeginCreativeImageAttempt(ctx context.Context, runID string, accountID int64, budget time.Duration, now time.Time) (*service.CreativeRun, error) {
	current, err := r.client.CreativeRun.Query().Where(creativerun.RunIDEQ(runID)).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrCreativeRunNotFound, nil)
	}
	ids, deadline, err := service.CreativeImageAttemptBudget(creativeRunEntityToService(current), accountID, budget, now)
	if err != nil {
		return nil, err
	}
	affected, err := r.client.CreativeRun.Update().Where(creativerun.RunIDEQ(runID), creativerun.VersionEQ(current.Version), creativerun.StatusEQ(service.CreativeRunStatusRunning), creativerun.ProvisioningPhaseNEQ(service.CreativeProvisioningPhaseProviderInflight), creativerun.ProviderResultRecordedAtIsNil()).
		SetImageAttemptAccountIds(ids).SetImageExecutionDeadlineAt(deadline).SetAccountID(accountID).SetProvisioningPhase(service.CreativeProvisioningPhaseProviderInflight).AddVersion(1).SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, service.ErrCreativeInvalidTransition
	}
	return r.GetCreativeRunByRunID(ctx, runID)
}

// 只有本次实际账号的明确拒绝能打开下一次尝试；旧 worker 不能改写备用账号的栅栏。
func (r *creativeRunRepository) AuthorizeCreativeImageRetry(ctx context.Context, runID string, accountID int64) error {
	current, err := r.client.CreativeRun.Query().Where(creativerun.RunIDEQ(runID)).Only(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrCreativeRunNotFound, nil)
	}
	if current.AccountID == nil || *current.AccountID != accountID || current.Operation == service.CreativeOperationTTS {
		return service.ErrCreativeInvalidTransition
	}
	affected, err := r.client.CreativeRun.Update().Where(creativerun.RunIDEQ(runID), creativerun.VersionEQ(current.Version), creativerun.AccountIDEQ(accountID), creativerun.StatusEQ(service.CreativeRunStatusRunning), creativerun.ProvisioningPhaseEQ(service.CreativeProvisioningPhaseProviderInflight), creativerun.ProviderResultRecordedAtIsNil()).SetProvisioningPhase(service.CreativeProvisioningPhaseProviderRetry).AddVersion(1).SetUpdatedAt(time.Now()).Save(ctx)
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrCreativeInvalidTransition
	}
	return nil
}
