package service

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	creativeTransientCleanupInterval = 5 * time.Minute
	creativeTransientCleanupAge      = 10 * time.Minute
	creativeTransientCleanupLimit    = 100
)

// ReconcileCreativeTransientOnce 清理已进入终态且客户端已无须继续读取的 transient 数据。
// 删除失败只保留日志，下一轮扫描会再次尝试；run 状态不会被清理动作改写。
func (s *CreativePublicService) ReconcileCreativeTransientOnce(ctx context.Context) (int, error) {
	if s == nil || s.Repo == nil || s.TransientStore == nil {
		return 0, nil
	}
	runs, err := s.Repo.ListCreativeRunsDueForTransientCleanup(ctx, time.Now().Add(-creativeTransientCleanupAge), creativeTransientCleanupLimit)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for _, run := range runs {
		if run == nil {
			continue
		}
		if err := s.TransientStore.DeleteRunTransient(ctx, run.RunID, 0, run.RequestedOutputCount); err != nil {
			logger.L().Warn("creative.transient_cleanup_failed", zap.String("run_id", run.RunID), zap.Error(err))
			continue
		}
		cleaned++
	}
	return cleaned, nil
}

// RunCreativeTransientReconciler 周期性清理终态任务的 Redis 临时数据。
func (s *CreativePublicService) RunCreativeTransientReconciler(ctx context.Context) {
	if s == nil || s.Repo == nil || s.TransientStore == nil {
		return
	}
	ticker := time.NewTicker(creativeTransientCleanupInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		_, _ = s.ReconcileCreativeTransientOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
