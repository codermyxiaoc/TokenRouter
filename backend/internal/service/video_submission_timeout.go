package service

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"time"
)

const videoUnknownSubmissionGrace = 15 * time.Minute

// VideoSubmissionResponseError 区分已开始读取响应的故障，不能把丢失正文误当作未收到受理证据。
type VideoSubmissionResponseError struct{ Err error }

func (e *VideoSubmissionResponseError) Error() string {
	return "video upstream response could not be read"
}
func (e *VideoSubmissionResponseError) Unwrap() error { return e.Err }

// 创建预算覆盖所有安全换号尝试，零值兼容直接装配的服务及历史配置。
func (s *VideoTaskService) videoCreateTimeout() time.Duration {
	seconds := 180
	if s.cfg != nil && s.cfg.Gateway.VideoCreateTimeoutSeconds > 0 && s.cfg.Gateway.VideoCreateTimeoutSeconds <= 600 {
		seconds = s.cfg.Gateway.VideoCreateTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// 租约必须覆盖创建执行和独立收尾，不能让恢复器在请求还在发送时接管同一任务。
func (s *VideoTaskService) videoSubmissionLease() time.Duration {
	lease := s.videoCreateTimeout() + 30*time.Second
	if lease < 3*time.Minute {
		lease = 3 * time.Minute
	}
	return lease
}

// 到达请求截止时间后仍须保留上游归属，收尾不复用已失效的请求上下文。
func (s *VideoTaskService) saveVideoTaskState(ctx context.Context, task *VideoTaskRecord, release bool) error {
	cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer done()
	return s.repo.Save(cleanup, task, release)
}

func (s *VideoTaskService) saveVideoUnknownSubmission(ctx context.Context, task *VideoTaskRecord) {
	if err := s.saveVideoTaskState(ctx, task, true); err != nil {
		slog.Error("video submission outcome save pending", "task_id", task.ID, "error", err)
		return
	}
	// 保存耗尽自身预算也不能把已失效的上下文传入任务展示投影。
	cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer done()
	if err := s.observe(cleanup, task); err != nil {
		slog.Error("video submission observation pending", "task_id", task.ID, "error", err)
	}
}

// 只有真正超时且没有已读响应的故障参与限时核对；连接故障、响应故障仍保留人工核对。
func videoSubmissionFailureKind(err error) string {
	var responseErr *VideoSubmissionResponseError
	if errors.As(err, &responseErr) || errors.Is(err, errVideoTaskResponseMismatch) {
		return "response"
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "timeout"
	}
	return "transport"
}

// 无响应是严格条件：任何已保存的状态码或包都可能含有无法识别的任务 ID，不自动退款。
func videoSubmissionHasNoResponse(task *VideoTaskRecord) bool {
	return task != nil && strings.TrimSpace(task.UpstreamTaskID) == "" && task.CreateResponseStatus == 0 &&
		task.ResponseStatus == 0 && len(task.CreateResponse) == 0 && len(task.RawResponse) == 0 &&
		task.VideoURL == "" && task.UpstreamStatus == "" && task.Metadata.Tokens == nil && !task.PricingMismatch
}

// recoverUnknownVideoSubmission 只关闭无归属的超时/中断创建，不能重发或触及已有 ID 的生成任务。
// @project-doc docs/domains/video_tasks.md#video_task_lifecycle
func (s *VideoTaskService) recoverUnknownVideoSubmission(ctx context.Context, task *VideoTaskRecord, now time.Time) bool {
	if (task.Status != "submitting" && task.Status != "submission_unknown") || !videoSubmissionHasNoResponse(task) {
		return false
	}
	if task.Status == "submitting" && task.SubmissionFailureKind == "" {
		// 只有租约恢复者会来到这里。按已持久化的创建截止时刻开始核对，不能因重启延长。
		if task.SubmissionStartedAt == nil || task.SubmissionDeadlineAt == nil ||
			task.SubmissionStartedAt.IsZero() || !task.SubmissionDeadlineAt.After(*task.SubmissionStartedAt) {
			// 旧版未保存提交时点，不能按升级后的预算猜测受理阶段。
			return false
		}
		task.SubmissionFailureKind = "interrupted"
		started := *task.SubmissionDeadlineAt
		task.SubmissionUnknownSince = &started
	}
	if (task.SubmissionFailureKind != "timeout" && task.SubmissionFailureKind != "interrupted") ||
		task.SubmissionUnknownSince == nil || task.SubmissionUnknownSince.IsZero() {
		// 旧版未知记录没有错误阶段证据，不能把可能的正文读取失败自动归入超时退款。
		return false
	}
	deadline := task.SubmissionUnknownSince.Add(videoUnknownSubmissionGrace)
	task.Status, task.BillingStatus = "submission_unknown", "reconciliation"
	task.NextPollAt = deadline
	if now.Before(deadline) {
		task.ErrorMessage = "创建超时且未取得任务 ID，等待核对期限后释放预留，请勿重新提交"
		_ = s.observe(ctx, task)
		return true
	}
	task.Status = "failed"
	task.ErrorMessage = "创建超时后 15 分钟仍无任务 ID，平台承担可能的上游成本，正在释放预留"
	task.CompletedAt = &now
	if err := s.saveVideoTaskState(ctx, task, false); err != nil {
		slog.Error("video unknown submission expiry save pending", "task_id", task.ID, "error", err)
		return true
	}
	s.finish(ctx, task)
	return true
}
