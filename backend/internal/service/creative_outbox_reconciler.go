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
	run, err := s.Repo.GetCreativeRunByRunID(ctx, event.RunID)
	if err != nil {
		if errors.Is(err, ErrCreativeRunNotFound) {
			return nil
		}
		return err
	}
	switch event.Operation {
	case CreativeRunOutboxProvision:
		return s.reconcileCreativeProvision(ctx, run)
	case CreativeRunOutboxSettle, CreativeRunOutboxRelease:
		if IsTerminalCreativeRunStatus(run.Status) {
			return nil
		}
		if err := s.Queue.Enqueue(ctx, run.RunID); err != nil && !errors.Is(err, ErrCreativeAlreadyQueued) {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown creative outbox operation %q", event.Operation)
	}
}

func (s *CreativePublicService) reconcileCreativeProvision(ctx context.Context, run *CreativeRun) error {
	if run == nil {
		return nil
	}
	if run.ProvisioningPhase == CreativeProvisioningPhaseEnqueued || run.ProvisioningPhase == CreativeProvisioningPhaseComplete {
		if err := s.Queue.Enqueue(ctx, run.RunID); err != nil && !errors.Is(err, ErrCreativeAlreadyQueued) {
			return err
		}
		return s.Repo.SetCreativeRunProvisioningPhase(ctx, run.RunID, CreativeProvisioningPhaseComplete)
	}
	// 只有 transient 已保存时才能在不持久化图片的前提下继续入队。
	if run.ProvisioningPhase != CreativeProvisioningPhaseTransientSaved || s.TransientStore == nil {
		return s.FailRun(ctx, run.RunID, "PROVISIONING_INCOMPLETE", "creative provisioning could not be recovered without transient input")
	}
	if _, err := s.TransientStore.LoadPayload(ctx, run.RunID); err != nil {
		return s.MarkResultLost(ctx, run.RunID, false)
	}
	if err := s.Queue.Enqueue(ctx, run.RunID); err != nil && !errors.Is(err, ErrCreativeAlreadyQueued) {
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
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
