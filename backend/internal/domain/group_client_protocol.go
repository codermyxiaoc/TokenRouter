package domain

import "fmt"

// GroupClientProtocol 表示客户端调用分组时使用的公开文本协议。
type GroupClientProtocol string

const (
	GroupClientProtocolAnthropicMessages     GroupClientProtocol = "anthropic_messages"
	GroupClientProtocolOpenAIResponses       GroupClientProtocol = "openai_responses"
	GroupClientProtocolOpenAIChatCompletions GroupClientProtocol = "openai_chat_completions"
	GroupClientProtocolGeminiGenerateContent GroupClientProtocol = "gemini_generate_content"
)

var canonicalGroupClientProtocols = []GroupClientProtocol{
	GroupClientProtocolAnthropicMessages,
	GroupClientProtocolOpenAIResponses,
	GroupClientProtocolOpenAIChatCompletions,
	GroupClientProtocolGeminiGenerateContent,
}

// SupportedGroupClientProtocols 返回平台实际实现的客户端协议集合。
// @project-doc docs/interfaces/upstream_account_matrix.md#public_gateway_protocols
func SupportedGroupClientProtocols(platform string) []GroupClientProtocol {
	switch platform {
	case PlatformAnthropic, PlatformOpenAI, PlatformQoder, PlatformGrok, PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax:
		return []GroupClientProtocol{
			GroupClientProtocolAnthropicMessages,
			GroupClientProtocolOpenAIResponses,
			GroupClientProtocolOpenAIChatCompletions,
		}
	case PlatformGemini, PlatformAntigravity:
		return append([]GroupClientProtocol{}, canonicalGroupClientProtocols...)
	default:
		return []GroupClientProtocol{}
	}
}

// DefaultGroupClientProtocols 返回新建分组的协议默认值。
// 默认值只决定初始选择，管理员可以在保存时关闭任意协议。
func DefaultGroupClientProtocols(platform string) []GroupClientProtocol {
	switch platform {
	case PlatformAnthropic:
		return []GroupClientProtocol{GroupClientProtocolAnthropicMessages}
	case PlatformOpenAI, PlatformGrok:
		return []GroupClientProtocol{
			GroupClientProtocolOpenAIResponses,
			GroupClientProtocolOpenAIChatCompletions,
		}
	case PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax:
		return []GroupClientProtocol{
			GroupClientProtocolAnthropicMessages,
			GroupClientProtocolOpenAIResponses,
			GroupClientProtocolOpenAIChatCompletions,
		}
	case PlatformGemini:
		return []GroupClientProtocol{GroupClientProtocolGeminiGenerateContent}
	case PlatformAntigravity:
		return []GroupClientProtocol{
			GroupClientProtocolAnthropicMessages,
			GroupClientProtocolGeminiGenerateContent,
		}
	default:
		return []GroupClientProtocol{}
	}
}

// ValidateGroupClientProtocols 校验完整协议集合并返回固定顺序的副本。
func ValidateGroupClientProtocols(platform string, protocols []GroupClientProtocol) ([]GroupClientProtocol, error) {
	supported := make(map[GroupClientProtocol]struct{})
	for _, protocol := range SupportedGroupClientProtocols(platform) {
		supported[protocol] = struct{}{}
	}
	known := make(map[GroupClientProtocol]struct{}, len(canonicalGroupClientProtocols))
	for _, protocol := range canonicalGroupClientProtocols {
		known[protocol] = struct{}{}
	}

	seen := make(map[GroupClientProtocol]struct{}, len(protocols))
	for i, protocol := range protocols {
		if _, ok := known[protocol]; !ok {
			return nil, fmt.Errorf("allowed_client_protocols[%d] contains unknown protocol %q", i, protocol)
		}
		if _, ok := supported[protocol]; !ok {
			return nil, fmt.Errorf("protocol %q is not supported by platform %q", protocol, platform)
		}
		if _, ok := seen[protocol]; ok {
			return nil, fmt.Errorf("protocol %q is duplicated", protocol)
		}
		seen[protocol] = struct{}{}
	}
	out := make([]GroupClientProtocol, 0, len(seen))
	for _, protocol := range canonicalGroupClientProtocols {
		if _, ok := seen[protocol]; ok {
			out = append(out, protocol)
		}
	}
	return out, nil
}

// HasGroupClientProtocol 判断集合是否包含指定协议。
func HasGroupClientProtocol(protocols []GroupClientProtocol, target GroupClientProtocol) bool {
	for _, protocol := range protocols {
		if protocol == target {
			return true
		}
	}
	return false
}

// SetGroupClientProtocol 更新单个协议并保持公共契约规定的顺序。
func SetGroupClientProtocol(protocols []GroupClientProtocol, target GroupClientProtocol, enabled bool) []GroupClientProtocol {
	selected := make(map[GroupClientProtocol]struct{}, len(protocols)+1)
	for _, protocol := range protocols {
		selected[protocol] = struct{}{}
	}
	if enabled {
		selected[target] = struct{}{}
	} else {
		delete(selected, target)
	}
	out := make([]GroupClientProtocol, 0, len(selected))
	for _, protocol := range canonicalGroupClientProtocols {
		if _, ok := selected[protocol]; ok {
			out = append(out, protocol)
		}
	}
	return out
}
