package service

import "github.com/tidwall/gjson"

// replaceOpenAIRawInput 只在最终结果中复制未变化的图片和工具结果，不额外构建完整 input。
// 原始 body 和片段均保持只读，避免破坏智能路由重试及 OpenCode 保存的入站会话字段。
// @project-doc docs/interfaces/openai_upstream.md#responses_large_body
func replaceOpenAIRawInput(body []byte, input gjson.Result, items []string) []byte {
	size := len(body) - len(input.Raw) + 2
	for index, item := range items {
		size += len(item)
		if index > 0 {
			size++
		}
	}
	result := make([]byte, 0, size)
	result = append(result, body[:input.Index]...)
	result = append(result, '[')
	for index, item := range items {
		if index > 0 {
			result = append(result, ',')
		}
		result = append(result, item...)
	}
	result = append(result, ']')
	return append(result, body[input.Index+len(input.Raw):]...)
}

// hasDuplicateJSONObjectKeys 检测重名键；标准解码器取最后一个值，GJSON 取第一个值。
func hasDuplicateJSONObjectKeys(object gjson.Result) bool {
	seen := make(map[string]struct{})
	duplicate := false
	object.ForEach(func(key, _ gjson.Result) bool {
		if _, exists := seen[key.Str]; exists {
			duplicate = true
			return false
		}
		seen[key.Str] = struct{}{}
		return true
	})
	return duplicate
}
