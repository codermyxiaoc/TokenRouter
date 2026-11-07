package service

import (
	"net/url"
	"strings"
)

// buildAnthropicMessagesURL 在已校验的 base URL 后追加 Messages 端点。
// Cloudflare 账号级 REST 端点会拒绝 beta 查询参数，因此只对该精确路径省略；
// 其余上游（包括 Cloudflare 原生 AI Gateway）保持原有的 ?beta=true。
func buildAnthropicMessagesURL(baseURL string) string {
	targetURL := strings.TrimRight(baseURL, "/") + "/v1/messages"
	parsed, err := url.Parse(targetURL)
	if err == nil && strings.EqualFold(parsed.Scheme, "https") && parsed.User == nil &&
		parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == "" && parsed.RawPath == "" {
		host := strings.ToLower(parsed.Host)
		parts := strings.Split(parsed.Path, "/")
		if (host == "api.cloudflare.com" || host == "api.cloudflare.com:443") &&
			len(parts) == 8 && parts[0] == "" && parts[1] == "client" && parts[2] == "v4" &&
			parts[3] == "accounts" && parts[4] != "" && parts[4] != "." && parts[4] != ".." &&
			parts[5] == "ai" && parts[6] == "v1" && parts[7] == "messages" {
			return targetURL
		}
	}
	return targetURL + "?beta=true"
}
