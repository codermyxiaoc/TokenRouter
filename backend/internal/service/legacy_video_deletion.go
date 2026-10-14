package service

import (
	"context"
	"errors"
	"time"
)

// RecordLegacyVideoDeletion 保存已成功删除的事实，不重发上游请求，不根据删除响应猜测账单。
func (s *OpenAIGatewayService) RecordLegacyVideoDeletion(ctx context.Context, task *LegacyVideoTask) error {
	if !s.HasLegacyVideoRecovery() {
		return nil
	}
	repo, ok := s.legacyVideoTasks.(LegacyVideoTaskDeletionRepository)
	if !ok || task == nil || task.Source != "seedance_video" {
		return errors.New("视频删除事实存储不可用")
	}
	// 旧缓存任务也先保存原归属；缺少资金快照仍可记录删除，但不能推算或释放账务。
	if task.Pending == nil {
		// 缓存暂失不能阻止记住删除；数据库写入仍校验原归属并保留首次资金快照。
		pending, _ := s.LoadGrokVideoPendingBilling(ctx, SeedanceTaskKey(task.TaskID), task.BillingUserID, task.APIKeyID)
		task.Pending = pending
	}
	if task.Pending != nil {
		task.Model = task.Pending.OriginalModel
		if task.Model == "" {
			task.Model = task.Pending.Model
		}
		task.CreatedAt, _ = time.Parse(time.RFC3339Nano, task.Pending.CreatedAt)
	}
	if task.Status == "" {
		task.Status = "queued"
	}
	if task.BillingStatus == "" {
		task.BillingStatus = "reconciliation"
	}
	return repo.MarkUpstreamDeleted(ctx, task)
}

// legacyVideoDeletionSeen 查询最新删除事实，防止领取之后的成功删除被旧 worker 忽略。
func (r *LegacyVideoRecovery) legacyVideoDeletionSeen(ctx context.Context, task *LegacyVideoTask) (bool, error) {
	if task.Source != "seedance_video" {
		return false, nil
	}
	if task.UpstreamDeletedAt != nil {
		return true, nil
	}
	current, err := r.repo.Get(ctx, task.Source, task.TaskID, task.UserID, task.APIKeyID)
	if errors.Is(err, ErrLegacyVideoTaskNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current != nil && current.UpstreamDeletedAt != nil {
		task.UpstreamDeletedAt = current.UpstreamDeletedAt
		return true, nil
	}
	return false, nil
}
