package apicompat

import "strings"

// responsesReasoningText 优先使用可见的 summary 文本；当兼容上游未填充 summary 时，
// 回退到 content 中的原始 reasoning_text，避免缓冲（非流式）路径丢失推理内容。
func responsesReasoningText(item ResponsesOutput) string {
	var text strings.Builder
	for _, part := range item.Summary {
		if part.Type == "summary_text" {
			_, _ = text.WriteString(part.Text)
		}
	}
	if text.Len() == 0 {
		for _, part := range item.Content {
			if part.Type == "reasoning_text" {
				_, _ = text.WriteString(part.Text)
			}
		}
	}
	return text.String()
}
