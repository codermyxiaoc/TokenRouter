// Package openai_compat 提供 OpenAI 协议族在不同上游间的兼容判定工具。
package openai_compat

// TextRouteMode 描述普通文本请求的上游协议路由策略。
type TextRouteMode string

const (
	// TextRouteModePreserveClientProtocol 优先保留客户端协议。
	TextRouteModePreserveClientProtocol TextRouteMode = "preserve_client_protocol"
	// TextRouteModeForceResponses 强制使用 Responses 协议。
	TextRouteModeForceResponses TextRouteMode = "force_responses"
	// TextRouteModeForceChatCompletions 强制使用 Chat Completions 协议。
	TextRouteModeForceChatCompletions TextRouteMode = "force_chat_completions"
)

// ResponsesProbeStatus 描述最近一次 Responses 能力探测结果。
type ResponsesProbeStatus string

const (
	ResponsesProbeStatusSupported   ResponsesProbeStatus = "supported"
	ResponsesProbeStatusUnsupported ResponsesProbeStatus = "unsupported"
	ResponsesProbeStatusUnknown     ResponsesProbeStatus = "unknown"
)

// TextProtocol 描述普通文本请求发往上游时使用的协议。
type TextProtocol string

const (
	TextProtocolChatCompletions TextProtocol = "chat_completions"
	TextProtocolResponses       TextProtocol = "responses"
)

const (
	// ExtraKeyTextRouteMode 是管理员控制的文本协议路由配置。
	ExtraKeyTextRouteMode = "openai_text_route_mode"
	// ExtraKeyResponsesProbeStatus 是探测服务维护的 Responses 支持状态。
	ExtraKeyResponsesProbeStatus = "openai_responses_probe_status"
	// ExtraKeyResponsesContinuationSupported 是管理员控制的 HTTP continuation 能力开关。
	ExtraKeyResponsesContinuationSupported = "openai_responses_continuation_supported"
)

// NormalizeTextRouteMode 将缺失或非法模式归一化为保留客户端协议。
func NormalizeTextRouteMode(mode string) TextRouteMode {
	switch TextRouteMode(mode) {
	case TextRouteModeForceResponses:
		return TextRouteModeForceResponses
	case TextRouteModeForceChatCompletions:
		return TextRouteModeForceChatCompletions
	default:
		return TextRouteModePreserveClientProtocol
	}
}

// NormalizeResponsesProbeStatus 将缺失或非法探测值归一化为 unknown。
func NormalizeResponsesProbeStatus(status string) ResponsesProbeStatus {
	switch ResponsesProbeStatus(status) {
	case ResponsesProbeStatusSupported:
		return ResponsesProbeStatusSupported
	case ResponsesProbeStatusUnsupported:
		return ResponsesProbeStatusUnsupported
	default:
		return ResponsesProbeStatusUnknown
	}
}

// ResolveTextRouteMode 从账号 extra 中读取管理员配置的文本协议路由模式。
func ResolveTextRouteMode(extra map[string]any) TextRouteMode {
	if extra == nil {
		return TextRouteModePreserveClientProtocol
	}
	mode, _ := extra[ExtraKeyTextRouteMode].(string)
	return NormalizeTextRouteMode(mode)
}

// ResolveResponsesProbeStatus 从账号 extra 中读取 Responses 探测状态。
func ResolveResponsesProbeStatus(extra map[string]any) ResponsesProbeStatus {
	if extra == nil {
		return ResponsesProbeStatusUnknown
	}
	status, _ := extra[ExtraKeyResponsesProbeStatus].(string)
	return NormalizeResponsesProbeStatus(status)
}

// ResolveResponsesContinuationSupported 从账号 extra 中读取 HTTP continuation 能力开关。
// 缺失或类型不匹配时按不支持处理，避免把账号类型误当作上游能力证明。
func ResolveResponsesContinuationSupported(extra map[string]any) bool {
	if extra == nil {
		return false
	}
	supported, _ := extra[ExtraKeyResponsesContinuationSupported].(bool)
	return supported
}

// ResolveUpstreamTextProtocol 综合客户端首选协议、管理员路由模式和探测事实，
// 返回普通文本请求实际应使用的上游协议。
func ResolveUpstreamTextProtocol(extra map[string]any, preferred TextProtocol) TextProtocol {
	switch ResolveTextRouteMode(extra) {
	case TextRouteModeForceResponses:
		return TextProtocolResponses
	case TextRouteModeForceChatCompletions:
		return TextProtocolChatCompletions
	}

	if preferred == TextProtocolChatCompletions {
		return TextProtocolChatCompletions
	}
	if ResolveResponsesProbeStatus(extra) == ResponsesProbeStatusUnsupported {
		return TextProtocolChatCompletions
	}
	return TextProtocolResponses
}
