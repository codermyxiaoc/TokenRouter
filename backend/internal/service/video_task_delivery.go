package service

import (
	"context"
	"log/slog"
	"time"
)

// 用户没有拿到成品就不扣费：受理无法确认、上游过期、长时间未完成或完成却没有可取回的产物，
// 在各自的持久截止时间后统一失败并通过原 video_release 事务释放预扣；之后迟到的终态不会补扣。
// @project-doc docs/domains/video_tasks.md#video_task_delivery_release

// videoMaxProcessing 返回任务排队与生成的最长等待：模板声明优先，其次配置，最后默认值。
func (s *VideoTaskService) videoMaxProcessing(t *VideoTaskRecord) time.Duration {
	minutes := defaultVideoMaxProcessingMinutes
	if s != nil && s.cfg != nil && s.cfg.Gateway.VideoMaxProcessingMinutes > 0 {
		minutes = s.cfg.Gateway.VideoMaxProcessingMinutes
	}
	if t.Target.Version == videoTargetVersionProfile {
		minutes = videoProfileMaxProcessing(t.Target)
	}
	return time.Duration(minutes) * time.Minute
}

// videoProcessingDeadline 以任务创建时间为准，轮询、重启和配置修改都不能延长冻结。
func (s *VideoTaskService) videoProcessingDeadline(t *VideoTaskRecord) time.Time {
	return t.CreatedAt.Add(s.videoMaxProcessing(t))
}

// videoUnknownSubmissionDeadline 返回受理不明任务的释放时刻：优先使用持久化的未知起点或提交截止时刻，
// 旧版缺少提交时点的记录按创建时间加创建预算推算。
func (s *VideoTaskService) videoUnknownSubmissionDeadline(t *VideoTaskRecord) time.Time {
	anchor := t.CreatedAt.Add(s.videoCreateTimeout())
	switch {
	case t.SubmissionUnknownSince != nil && !t.SubmissionUnknownSince.IsZero():
		anchor = *t.SubmissionUnknownSince
	case t.SubmissionDeadlineAt != nil && !t.SubmissionDeadlineAt.IsZero():
		anchor = *t.SubmissionDeadlineAt
	}
	return anchor.Add(videoUnknownSubmissionGrace)
}

// videoContentProbePendingMessage 标记“已完成但成品暂不可取回”，快速结算路径据此改为重新查询。
const videoContentProbePendingMessage = "视频已完成，成品暂不可取回，等待重新确认"

// videoDeliveryGrace 是完成后确认成品可取回的最短等待，避免临近处理期限完成的任务因一次读取失败被立即释放。
const videoDeliveryGrace = 30 * time.Minute

// videoDeliveryDeadline 返回确认可取回的截止时刻：处理期限与完成后宽限期取较晚者，均以持久字段为准。
func (s *VideoTaskService) videoDeliveryDeadline(t *VideoTaskRecord) time.Time {
	deadline := s.videoProcessingDeadline(t)
	if t.CompletedAt != nil {
		if grace := t.CompletedAt.Add(videoDeliveryGrace); grace.After(deadline) {
			deadline = grace
		}
	}
	return deadline
}

// videoTaskDeliverable 判断完成任务是否声明了用户可取回的成品：安全直链或冻结协议声明的凭据内容路径。
// 这只是格式层面的前置条件；扣费前还要由 deliveryProbe 实际读取确认。
func videoTaskDeliverable(t *VideoTaskRecord) bool {
	if t.VideoURL != "" && safeMediaPreviewURL(t.VideoURL) {
		return true
	}
	_, credentialed, err := videoContentPath(t.Target, t.UpstreamTaskID)
	return err == nil && credentialed
}

// releaseUndeliveredVideo 把任务置为失败并释放预扣；保存失败时留给下一次持有租约的恢复器重试。
func (s *VideoTaskService) releaseUndeliveredVideo(ctx context.Context, t *VideoTaskRecord, message string) {
	now := time.Now().UTC()
	t.Status = "failed"
	t.ErrorMessage = message
	t.CompletedAt = &now
	if err := s.saveVideoTaskState(ctx, t, false); err != nil {
		slog.Error("video undelivered release save pending", "task_id", t.ID, "error", err)
		return
	}
	s.finish(ctx, t)
}
