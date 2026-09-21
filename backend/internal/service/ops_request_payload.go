package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// 响应正文仅保留有限片段；请求正文受网关自身 MaxRequestBodySize 约束，失败时完整保存。
	OpsRequestPayloadMaxBytes = 64 * 1024
	OpsRequestHeaderMaxBytes  = 16 * 1024
)

var sensitivePayloadKeyRE = regexp.MustCompile(`(?i)(authorization|api[-_]?key|access[-_]?token|refresh[-_]?token|id[-_]?token|cookie|set[-_]?cookie|password|secret|credential|private[-_]?key)`)
var sensitivePayloadValueRE = regexp.MustCompile(`(?i)(Bearer\s+|sk-[A-Za-z0-9._-]{8,}|api[_-]?key\s*[:=]\s*)[^\s"',]+`)

// GetRequestPayloadDetail 只在运维服务启用时读取，普通用户服务没有调用入口。
func (s *OpsService) GetRequestPayloadDetail(ctx context.Context, requestID string) (*OpsRequestPayloadDetail, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || len(requestID) > 200 {
		return nil, fmt.Errorf("invalid request id")
	}
	if s == nil || s.opsRepo == nil {
		return nil, nil
	}
	return s.opsRepo.GetRequestPayloadDetail(ctx, requestID)
}

// RecordRequestPayloadDetail 负责最后一道长度和脱敏校验，调用方可以直接传入采集到的原文。
func (s *OpsService) RecordRequestPayloadDetail(ctx context.Context, detail *OpsRequestPayloadDetail) error {
	if s == nil || detail == nil || s.opsRepo == nil || strings.TrimSpace(detail.RequestID) == "" {
		return nil
	}
	if !s.IsMonitoringEnabled(ctx) {
		return nil
	}
	prepareRequestPayloadDetail(detail)
	return s.opsRepo.UpsertRequestPayloadDetail(ctx, detail)
}

func prepareRequestPayloadDetail(detail *OpsRequestPayloadDetail) {
	detail.RequestID = strings.TrimSpace(detail.RequestID)
	detail.ClientRequestID = strings.TrimSpace(detail.ClientRequestID)
	detail.Method = strings.ToUpper(strings.TrimSpace(detail.Method))
	detail.Path = truncateOpsPayloadUTF8(strings.TrimSpace(detail.Path), 2048)
	detail.InboundEndpoint = truncateOpsPayloadUTF8(strings.TrimSpace(detail.InboundEndpoint), 512)
	detail.UpstreamEndpoint = truncateOpsPayloadUTF8(strings.TrimSpace(detail.UpstreamEndpoint), 512)
	detail.Platform = truncateOpsPayloadUTF8(strings.TrimSpace(detail.Platform), 128)
	detail.Model = truncateOpsPayloadUTF8(strings.TrimSpace(detail.Model), 512)
	detail.RequestHeaders = sanitizePayloadJSON(detail.RequestHeaders, OpsRequestHeaderMaxBytes)
	detail.ResponseHeaders = sanitizePayloadJSON(detail.ResponseHeaders, OpsRequestHeaderMaxBytes)
	// 失败请求的请求体需要完整用于复现问题；仍执行敏感字段脱敏，但不再按
	// 响应正文上限截断。入口层的 MaxRequestBodySize 仍是请求整体大小上限。
	detail.RequestBody = sanitizePayloadBody(detail.RequestBody, 0)
	detail.ResponseBody = sanitizePayloadBody(detail.ResponseBody, OpsRequestPayloadMaxBytes)
}

func truncateOpsPayloadUTF8(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value
}

func sanitizePayloadJSON(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err == nil {
		redactPayloadValue(decoded)
		if encoded, err := json.Marshal(decoded); err == nil {
			return truncateOpsPayloadUTF8(string(encoded), max)
		}
	}
	return truncateOpsPayloadUTF8(sensitivePayloadValueRE.ReplaceAllString(value, "[REDACTED]"), max)
}

func sanitizePayloadBody(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return "[binary payload omitted]"
	}
	return sanitizePayloadJSON(value, max)
}

func redactPayloadValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if sensitivePayloadKeyRE.MatchString(key) {
				typed[key] = "[REDACTED]"
				continue
			}
			redactPayloadValue(child)
		}
	case []any:
		for _, child := range typed {
			redactPayloadValue(child)
		}
	}
}
