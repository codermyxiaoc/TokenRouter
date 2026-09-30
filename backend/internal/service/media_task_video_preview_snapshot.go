package service

import (
	"math"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

// MediaTaskVideoSnapshot 只携带完成产物的白名单属性，不包含提示词或上游认证信息。
type MediaTaskVideoSnapshot struct {
	URL             string  `json:"url"`
	MimeType        string  `json:"mime_type,omitempty"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	SizeBytes       int64   `json:"size_bytes,omitempty"`
}

// videoTaskPreviewSnapshot 在 Grok 地址改写之前提取结果，未完成任务不提供预览。
func videoTaskPreviewSnapshot(endpoint GrokMediaEndpoint, body []byte, requestID string) *MediaTaskVideoSnapshot {
	// 先筛掉非视频入口，避免图片 Base64 等大响应额外经历 JSON 扫描。
	if endpoint.IsSeedance() {
		if endpoint == SeedanceEndpointDelete {
			return nil
		}
	} else {
		switch endpoint {
		case GrokMediaEndpointVideosGenerations, GrokMediaEndpointVideosEdits, GrokMediaEndpointVideosExtensions, GrokMediaEndpointVideoStatus, GrokMediaEndpointVideoContent:
		default:
			return nil
		}
	}
	if !gjson.ValidBytes(body) {
		return nil
	}
	var rawURL, prefix string
	if endpoint.IsSeedance() {
		if gjson.GetBytes(body, "status").String() != "succeeded" {
			return nil
		}
		rawURL = strings.TrimSpace(gjson.GetBytes(body, "content.video_url").String())
		prefix = "content."
	} else {
		if !IsGrokVideoStatusBillable(body) {
			return nil
		}
		// 沿用 Grok 官方媒体地址约束；需要上游 Key 的代理内容地址不能进入预览。
		var err error
		rawURL, err = grokMediaSignedVideoContentURL(body, requestID)
		if err != nil || rawURL == "" {
			return nil
		}
		prefix = "video."
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || len(rawURL) > 8192 {
		return nil
	}
	result := &MediaTaskVideoSnapshot{URL: rawURL}
	// 只读取产物实际报告的数值，不用提交时请求的分辨率、时长替代。
	number := func(name string) float64 {
		value := gjson.GetBytes(body, prefix+name)
		if value.Type != gjson.Number || value.Float() <= 0 || math.IsInf(value.Float(), 0) || math.IsNaN(value.Float()) {
			return 0
		}
		return value.Float()
	}
	if width := number("width"); width <= 65536 {
		result.Width = int(width)
	}
	if height := number("height"); height <= 65536 {
		result.Height = int(height)
	}
	if duration := number("duration"); duration <= 86400 {
		result.DurationSeconds = duration
	}
	if size := number("size_bytes"); size <= 1<<40 {
		result.SizeBytes = int64(size)
	}
	mime := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, prefix+"mime_type").String()))
	if mime == "video/mp4" || mime == "video/webm" || mime == "video/quicktime" {
		result.MimeType = mime
	}
	return result
}
