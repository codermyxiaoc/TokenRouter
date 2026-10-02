package service

import (
	"encoding/json"
	"net/http"
)

// 入口协议决定客户端响应形状，实际供应商协议继续由冻结的路由快照决定。
func videoProtocolResponse(t *VideoTaskRecord, protocol string, native bool) *VideoTaskResponse {
	// 即使直接调用投影，也不能把历史错误归属的观测转换成可信 OpenAI 任务。
	if !videoStoredResponseMatchesTask(t) {
		return videoTaskMismatchResponse(t)
	}
	if protocol != string(VideoEndpointOpenAIVideos) || native {
		return videoTaskResponse(t, native)
	}
	status := "in_progress"
	switch t.Status {
	case "prepared", "submitting", "queued":
		status = "queued"
	case "completed":
		status = "completed"
	case "failed", "cancelled":
		status = "failed"
	}
	value := map[string]any{
		"id": t.ID, "object": "video", "model": t.RequestedModel,
		"status": status, "created_at": t.CreatedAt.Unix(), "billing_status": t.BillingStatus,
	}
	if t.Status == "submission_unknown" || t.Status == "expired" {
		// 四态协议没有受理不明或过期，额外保留原观测以便调用方识别待核对任务。
		value["task_status"] = t.Status
	}
	if t.CompletedAt != nil {
		value["completed_at"] = t.CompletedAt.Unix()
	}
	if t.Metadata.DurationSeconds > 0 {
		value["duration"] = t.Metadata.DurationSeconds
	}
	if t.Metadata.Resolution != "" {
		value["resolution"] = t.Metadata.Resolution
	}
	if t.Metadata.Tokens != nil {
		value["usage"] = map[string]any{"completion_tokens": *t.Metadata.Tokens}
	}
	if t.ErrorMessage != "" {
		value["message"] = t.ErrorMessage
	}
	if status == "failed" {
		message := t.ErrorMessage
		if message == "" {
			message = "视频生成未成功完成"
		}
		value["error"] = map[string]string{"code": "video_generation_failed", "message": message}
	}
	body, _ := json.Marshal(value)
	return &VideoTaskResponse{StatusCode: http.StatusOK, Body: body, LocalTaskID: t.ID}
}

// 新统一入口幂等命中仍投影本地任务，原生入口继续重放保存的创建响应。
func videoCreateProtocolResponse(t *VideoTaskRecord, req VideoTaskSubmitRequest) *VideoTaskResponse {
	if req.InboundProtocol == string(VideoEndpointOpenAIVideos) && !req.Native {
		return videoProtocolResponse(t, req.InboundProtocol, false)
	}
	return videoCreateResponse(t, req.Native)
}
