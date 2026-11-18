package apicompat

import "strings"

// IsServerSideSearchCall 判断 Responses 输出条目是否为 xAI 服务端自行执行的搜索，客户端无需执行。
//
// xAI 文档规定 Responses API 中 X Search 表现为 call_id 以 xs_ 开头的 custom_tool_call，
// 附件搜索为 call_id 以 as_ 开头的 custom_tool_call，二者均"由 xAI 服务端处理"
// （https://docs.x.ai/developers/tools/tool-usage-details；https://docs.x.ai/developers/tools/x-search）。
// 不带这些前缀的 custom_tool_call 仍是客户端工具调用。
//
// 前缀规则只在此处维护：上游为 xAI 时，把 custom_tool_call 转为客户端工具调用的转换器
// 跳过本函数返回 true 的条目，Grok 搜索计数则将其计入。
func IsServerSideSearchCall(itemType, callID string) bool {
	if itemType != "custom_tool_call" {
		return false
	}
	return strings.HasPrefix(callID, "xs_") || strings.HasPrefix(callID, "as_")
}
