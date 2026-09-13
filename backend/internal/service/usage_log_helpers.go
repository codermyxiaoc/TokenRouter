package service

import "strings"

func optionalTrimmedStringPtr(raw string) *string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// coalesceRequestedReasoningEffort 优先保留处理器或传输层已捕获的客户端档位，
// 再使用尚未改写的请求体推导值；两者都为空时保留 NULL 表示客户端未声明。
func coalesceRequestedReasoningEffort(requested, forwarded *string) *string {
	if requested != nil {
		if value := strings.TrimSpace(*requested); value != "" {
			return &value
		}
	}
	if forwarded != nil {
		if value := strings.TrimSpace(*forwarded); value != "" {
			return &value
		}
	}
	return nil
}

func forwardResultBillingModel(requestedModel, upstreamModel string) string {
	if trimmed := strings.TrimSpace(requestedModel); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(upstreamModel)
}

func optionalInt64Ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}
