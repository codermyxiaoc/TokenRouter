package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

// MediaTaskPreview 只返回允许展示的媒体属性，不透传短期结果中的原始响应或提示词。
type MediaTaskPreview struct {
	Items             []MediaTaskPreviewItem `json:"items"`
	UnavailableReason string                 `json:"unavailable_reason,omitempty"`
	ExpiresAt         *time.Time             `json:"expires_at,omitempty"`
}

type MediaTaskPreviewItem struct {
	URL             string  `json:"url"`
	MediaType       string  `json:"media_type"`
	MimeType        string  `json:"mime_type,omitempty"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	SizeBytes       int64   `json:"size_bytes,omitempty"`
}

// Preview 先验证任务归属，再只读已保存结果；不进入任务恢复、上游查询或结算流程。
// @project-doc docs/domains/media_tasks.md#media_task_preview
func (s *MediaTaskService) Preview(ctx context.Context, actor MediaTaskActor, id int64) (*MediaTaskPreview, error) {
	task, err := s.Get(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	preview := &MediaTaskPreview{Items: []MediaTaskPreviewItem{}, ExpiresAt: task.ExpiresAt}
	if task.ExpiresAt != nil && !task.ExpiresAt.After(time.Now()) {
		preview.UnavailableReason = "expired"
		return preview, nil
	}
	if task.Status == "queued" || task.Status == "processing" {
		preview.UnavailableReason = "pending"
		return preview, nil
	}
	if task.Status != "completed" {
		preview.UnavailableReason = "unavailable"
		return preview, nil
	}
	if task.Source != "async_image" {
		return s.videoPreview(ctx, task, preview)
	}
	record, err := s.repo.GetImageResult(ctx, task)
	if errors.Is(err, ErrImageTaskNotFound) {
		preview.UnavailableReason = "unavailable"
		return preview, nil
	}
	if err != nil {
		return nil, err
	}
	// 数据库列与 JSON 身份都要匹配，损坏或错误绑定的记录不能扩大访问范围。
	if record == nil || record.ID != task.TaskID || record.UserID != task.UserID || record.APIKeyID != task.APIKeyID {
		preview.UnavailableReason = "unavailable"
		return preview, nil
	}
	expires := time.Unix(record.ExpiresAt, 0).UTC()
	preview.ExpiresAt = &expires
	if !expires.After(time.Now()) {
		preview.UnavailableReason = "expired"
		return preview, nil
	}
	if record.Status != ImageTaskStatusCompleted || len(record.Result) > maxImageTaskStoredResultBytes {
		preview.UnavailableReason = "unavailable"
		return preview, nil
	}
	var result struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if json.Unmarshal(record.Result, &result) == nil {
		for _, item := range result.Data {
			if safeMediaPreviewURL(item.URL) {
				preview.Items = append(preview.Items, MediaTaskPreviewItem{URL: item.URL, MediaType: "image"})
			}
		}
	}
	if len(preview.Items) == 0 {
		preview.UnavailableReason = "unavailable"
	}
	return preview, nil
}

// 只允许对象存储的 HTTP(S) 访问链接，拒绝内联数据、凭据和可执行协议。
func safeMediaPreviewURL(raw string) bool {
	if raw == "" || len(raw) > 16384 || strings.TrimSpace(raw) != raw {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}
