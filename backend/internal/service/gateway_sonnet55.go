package service

import (
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/pkg/claude"
	"github.com/tidwall/gjson"
)

// ValidateSonnet55Request 供各协议桥共用映射后的 Sonnet 5.5 参数边界。
func ValidateSonnet55Request(body []byte, model string) error {
	return claude.ValidateSonnet55Request(body, model)
}

// filterSonnet55ToolsetBeta 移除与稳定 computer/browser 工具集不兼容的旧流式 beta。
// 每个工具自己的 eager_input_streaming 字段仍保持不变。
func filterSonnet55ToolsetBeta(header string, body []byte, model string) string {
	if !claude.IsSonnet55Model(model) {
		return header
	}
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		switch tool.Get("type").String() {
		case "computer_toolset_20260801", "browser_toolset_20260801":
			return stripBetaTokensWithSet(header, map[string]struct{}{claude.BetaFineGrainedToolStreaming: {}})
		}
	}
	return header
}

// filterSonnet55ToolsetBetaHeader 在管理员覆写后再次约束协议不支持的 beta 组合。
func filterSonnet55ToolsetBetaHeader(headers http.Header, body []byte, model string) {
	header := getHeaderRaw(headers, "anthropic-beta")
	filtered := filterSonnet55ToolsetBeta(header, body, model)
	if filtered == header {
		return
	}
	deleteHeaderAllForms(headers, "anthropic-beta")
	if filtered != "" {
		setHeaderRaw(headers, "anthropic-beta", filtered)
	}
}
