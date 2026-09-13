package service

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// AccountTestModeDefault drives the standard /responses connection test.
	AccountTestModeDefault = "default"
	// AccountTestModeCompact drives the native remote compaction v2 probe.
	AccountTestModeCompact = "compact"
	// AccountTestModeLegacyCompact drives the legacy /responses/compact probe.
	AccountTestModeLegacyCompact = "legacy_compact"
)

const (
	// 原生 V2 与旧端点状态分别保存，旧端点 404 不得污染 V2 能力判定。
	openAINativeCompactionV2ModeExtraKey       = "openai_native_compaction_v2_mode"
	openAINativeCompactionV2SupportedExtraKey  = "openai_native_compaction_v2_supported"
	openAINativeCompactionV2CheckedAtExtraKey  = "openai_native_compaction_v2_checked_at"
	openAINativeCompactionV2LastStatusExtraKey = "openai_native_compaction_v2_last_status"
	openAINativeCompactionV2LastErrorExtraKey  = "openai_native_compaction_v2_last_error"
)

func normalizeAccountTestMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case AccountTestModeCompact:
		return AccountTestModeCompact
	case AccountTestModeLegacyCompact:
		return AccountTestModeLegacyCompact
	default:
		return AccountTestModeDefault
	}
}

// createOpenAICompactProbePayload 构造原生 V2 的流式 Responses 请求。V2 的关键
// 契约是最后一个 input 为 compaction_trigger，而非旧端点路径。
func createOpenAICompactProbePayload(model string, isOAuth bool) map[string]any {
	payload := map[string]any{
		"model":        strings.TrimSpace(model),
		"instructions": "You are a helpful coding assistant.",
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": "Respond with OK.",
			},
			map[string]any{"type": "compaction_trigger"},
		},
		"stream": true,
	}
	if isOAuth {
		// ChatGPT 内部 Responses API 与正常 OAuth 转发保持相同的 store 约束。
		payload["store"] = false
	}
	return payload
}

// createOpenAILegacyCompactProbePayload 保留旧端点的 unary 载荷形状。它只用于
// 管理员显式兼容性测试，绝不能作为原生 V2 的能力依据。
func createOpenAILegacyCompactProbePayload(model string) map[string]any {
	return map[string]any{
		"model":        strings.TrimSpace(model),
		"instructions": "You are a helpful coding assistant.",
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": "Respond with OK.",
			},
		},
	}
}

// openAICompactProbeFoundCompactionItem 确认原生 V2 响应确实给出了 compaction
// item。单纯 2xx 可能表示中间链路吞掉 trigger，不能误判为 V2 可用。
func openAICompactProbeFoundCompactionItem(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	bodyText := string(body)
	if _, found := findRawCompactionItemFromSSE(bodyText); found {
		return true
	}
	if finalResponse, ok := extractCodexFinalResponse(bodyText); ok && responsesOutputHasCompactionItem(finalResponse) {
		return true
	}
	return responsesOutputHasCompactionItem(body)
}

func shouldMarkOpenAICompactUnsupported(status int, body []byte) bool {
	switch status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	case http.StatusBadRequest, http.StatusForbidden, http.StatusUnprocessableEntity:
		lower := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body) + " " + string(body)))
		if strings.Contains(lower, "compact") {
			for _, keyword := range []string{
				"unsupported",
				"not support",
				"does not support",
				"not available",
				"disabled",
			} {
				if strings.Contains(lower, keyword) {
					return true
				}
			}
		}
	}
	return false
}

func buildOpenAICompactProbeExtraUpdates(resp *http.Response, body []byte, probeErr error, now time.Time) map[string]any {
	updates := map[string]any{
		"openai_compact_checked_at":  now.Format(time.RFC3339),
		"openai_compact_last_status": nil,
	}

	if resp != nil {
		updates["openai_compact_last_status"] = resp.StatusCode
	}

	switch {
	case probeErr != nil:
		updates["openai_compact_last_error"] = truncateString(sanitizeUpstreamErrorMessage(probeErr.Error()), 2048)
	case resp == nil:
		updates["openai_compact_last_error"] = "compact probe failed"
	default:
		errMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
		if errMsg == "" && len(body) > 0 {
			errMsg = strings.TrimSpace(string(body))
		}
		if errMsg == "" && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
			errMsg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		errMsg = truncateString(sanitizeUpstreamErrorMessage(errMsg), 2048)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			updates["openai_compact_supported"] = true
			updates["openai_compact_last_error"] = ""
		} else {
			if shouldMarkOpenAICompactUnsupported(resp.StatusCode, body) {
				updates["openai_compact_supported"] = false
			}
			updates["openai_compact_last_error"] = errMsg
		}
	}

	return updates
}

// buildOpenAINativeCompactionV2ProbeExtraUpdates 只更新 V2 独立状态。旧端点的
// openai_compact_* 状态继续只服务 legacy /responses/compact 调度。
func buildOpenAINativeCompactionV2ProbeExtraUpdates(resp *http.Response, body []byte, probeErr error, compactionFound bool, now time.Time) map[string]any {
	updates := map[string]any{
		openAINativeCompactionV2CheckedAtExtraKey:  now.Format(time.RFC3339),
		openAINativeCompactionV2LastStatusExtraKey: nil,
	}
	if resp != nil {
		updates[openAINativeCompactionV2LastStatusExtraKey] = resp.StatusCode
	}

	switch {
	case probeErr != nil:
		updates[openAINativeCompactionV2LastErrorExtraKey] = truncateString(sanitizeUpstreamErrorMessage(probeErr.Error()), 2048)
	case resp == nil:
		updates[openAINativeCompactionV2LastErrorExtraKey] = "native remote compaction v2 probe failed"
	case resp.StatusCode >= 200 && resp.StatusCode < 300 && compactionFound:
		updates[openAINativeCompactionV2SupportedExtraKey] = true
		updates[openAINativeCompactionV2LastErrorExtraKey] = ""
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		updates[openAINativeCompactionV2SupportedExtraKey] = false
		updates[openAINativeCompactionV2LastErrorExtraKey] = "upstream returned 2xx without a compaction output item (native remote compaction v2 unsupported)"
	default:
		errMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
		if errMsg == "" && len(body) > 0 {
			errMsg = strings.TrimSpace(string(body))
		}
		if errMsg == "" {
			errMsg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		if shouldMarkOpenAICompactUnsupported(resp.StatusCode, body) {
			updates[openAINativeCompactionV2SupportedExtraKey] = false
		}
		updates[openAINativeCompactionV2LastErrorExtraKey] = truncateString(sanitizeUpstreamErrorMessage(errMsg), 2048)
	}
	return updates
}

func mergeExtraUpdates(base map[string]any, more map[string]any) map[string]any {
	if len(base) == 0 && len(more) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(more))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range more {
		out[key] = value
	}
	return out
}

func compactProbeSessionID(accountID int64) string {
	if accountID <= 0 {
		return deriveStableUUIDv4("tokenrouter:openai-native-compaction-v2-probe:anonymous")
	}
	return deriveStableUUIDv4("tokenrouter:openai-native-compaction-v2-probe:" + strconv.FormatInt(accountID, 10))
}

// legacyCompactProbeSessionID 保持旧端点既有会话格式，避免兼容性测试本身改变
// legacy 上游对请求形状的识别。
func legacyCompactProbeSessionID(accountID int64) string {
	if accountID <= 0 {
		return "probe_compact"
	}
	return "probe_compact_" + strconv.FormatInt(accountID, 10)
}
