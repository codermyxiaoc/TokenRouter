package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	creativeOutboxClaimLimit = 50
	creativeOutboxLease      = 2 * time.Minute
	creativeOutboxPoll       = 5 * time.Second
	creativeOutboxRetry      = 15 * time.Second
)

// ReconcileCreativeOutboxOnce 领取并处理一批创作台后台动作。
// 图片仍只从 Redis 临时存储读取，outbox 本身只负责恢复入队和结算触发。
func (s *CreativePublicService) ReconcileCreativeOutboxOnce(ctx context.Context) (int, error) {
	if s == nil || s.Outbox == nil || s.Queue == nil || s.Repo == nil {
		return 0, nil
	}
	events, err := s.Outbox.Claim(ctx, "creative-reconciler", creativeOutboxClaimLimit, creativeOutboxLease)
	if err != nil {
		return 0, err
	}
	processed := 0
	var lastErr error
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		if err := s.reconcileCreativeOutboxEvent(ctx, event); err != nil {
			lastErr = err
			if retryErr := s.Outbox.Retry(ctx, event.ID, event.LeaseToken, time.Now().Add(creativeOutboxRetry), sanitizeCreativeMessage(err.Error())); retryErr != nil {
				logger.L().Warn("creative.outbox_retry_failed", zap.Int64("outbox_id", event.ID), zap.Error(retryErr))
			}
			continue
		}
		if err := s.Outbox.Complete(ctx, event.ID, event.LeaseToken); err != nil {
			lastErr = err
			continue
		}
		processed++
	}
	return processed, lastErr
}

func (s *CreativePublicService) reconcileCreativeOutboxEvent(ctx context.Context, event CreativeRunOutbox) error {
	switch event.Operation {
	case CreativeRunOutboxProvision, CreativeRunOutboxSettle, CreativeRunOutboxRelease:
	default:
		return fmt.Errorf("unknown creative outbox operation %q", event.Operation)
	}
	// 创建事务已经发布 outbox；三类补偿与创建、provider worker 必须使用同一任务锁。
	lease, err := s.acquireCreativeProvisionLease(ctx, event.RunID)
	if err != nil {
		return err
	}
	defer lease.close()
	ctx = lease.ctx
	if err := lease.check(); err != nil {
		return err
	}
	// 锁前读到的 created 快照可能已过期，所有阶段和终态判断必须在取得锁后重读。
	run, err := s.Repo.GetCreativeRunByRunID(ctx, event.RunID)
	if err != nil {
		if errors.Is(err, ErrCreativeRunNotFound) {
			return nil
		}
		return err
	}
	if err := lease.check(); err != nil {
		return err
	}
	switch event.Operation {
	case CreativeRunOutboxProvision:
		return s.reconcileCreativeProvision(ctx, run)
	case CreativeRunOutboxSettle, CreativeRunOutboxRelease:
		// 只有资金动作及任务推进成功才确认 outbox，瞬时故障保持原持久意图。
		if event.Operation == CreativeRunOutboxRelease {
			return s.ReleaseRun(ctx, run.RunID)
		}
		return s.SettleRun(ctx, run.RunID)
	default:
		return fmt.Errorf("unknown creative outbox operation %q", event.Operation)
	}
}

func (s *CreativePublicService) reconcileCreativeProvision(ctx context.Context, run *CreativeRun) error {
	if run == nil {
		return nil
	}
	if err := checkCreativeProvisionLease(ctx); err != nil {
		return err
	}
	// 已进入执行阶段说明创建已完成；旧 provision 事件不得擦除执行栅栏或误释放资金。
	if run.Status != CreativeRunStatusQueued {
		if IsTerminalCreativeRunStatus(run.Status) {
			return nil
		}
		if err := s.Queue.Enqueue(ctx, run.RunID); err != nil && !errors.Is(err, ErrCreativeAlreadyQueued) {
			return err
		}
		return nil
	}
	if run.ProvisioningPhase == CreativeProvisioningPhaseEnqueued || run.ProvisioningPhase == CreativeProvisioningPhaseComplete {
		if err := s.Queue.Enqueue(ctx, run.RunID); err != nil && !errors.Is(err, ErrCreativeAlreadyQueued) {
			return err
		}
		if err := checkCreativeProvisionLease(ctx); err != nil {
			return err
		}
		return s.Repo.SetCreativeRunProvisioningPhase(ctx, run.RunID, CreativeProvisioningPhaseComplete)
	}
	// 只有 transient 已保存时才能在不持久化图片的前提下继续入队。
	if run.ProvisioningPhase != CreativeProvisioningPhaseTransientSaved {
		return s.FailRun(ctx, run.RunID, "PROVISIONING_INCOMPLETE", "creative provisioning could not be recovered without transient input")
	}
	if s.TransientStore == nil {
		return ErrCreativeTransientUnavailable
	}
	if _, err := s.TransientStore.LoadPayload(ctx, run.RunID); err != nil {
		// Redis 不可用不等于载荷丢失；不能把可恢复的临时故障变成失败退款。
		if !errors.Is(err, ErrCreativeTransientNotFound) && !errors.Is(err, ErrCreativeTransientCorrupt) {
			return err
		}
		if leaseErr := checkCreativeProvisionLease(ctx); leaseErr != nil {
			return leaseErr
		}
		return s.MarkResultLost(ctx, run.RunID, false)
	}
	if err := checkCreativeProvisionLease(ctx); err != nil {
		return err
	}
	if err := s.Queue.Enqueue(ctx, run.RunID); err != nil && !errors.Is(err, ErrCreativeAlreadyQueued) {
		return err
	}
	if err := checkCreativeProvisionLease(ctx); err != nil {
		return err
	}
	if err := s.Repo.SetCreativeRunProvisioningPhase(ctx, run.RunID, CreativeProvisioningPhaseEnqueued); err != nil {
		return err
	}
	return nil
}

// RunCreativeOutboxReconciler 启动创作台 outbox 周期恢复循环。
func (s *CreativePublicService) RunCreativeOutboxReconciler(ctx context.Context) {
	if s == nil || s.Outbox == nil || s.Queue == nil {
		return
	}
	ticker := time.NewTicker(creativeOutboxPoll)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		_, _ = s.ReconcileCreativeOutboxOnce(ctx)
		recoveryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if _, err := s.ReconcileCreativeUsageOnce(recoveryCtx); err != nil {
			logger.L().Warn("creative.usage_recovery_failed", zap.Error(err))
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
