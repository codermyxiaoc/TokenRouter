package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/jsonutil"
	"github.com/tidwall/gjson"
)

// Cline 非流式成功封装须在所有入站转换前解包，避免只有计量却没有答案的空成功。
func normalizeClineChatJSON(body []byte) ([]byte, error) {
	if jsonutil.ValidateUniqueFields(body) != nil {
		return nil, fmt.Errorf("invalid Cline JSON response")
	}
	root := gjson.ParseBytes(body)
	if success := root.Get("success"); success.Exists() {
		if success.Type != gjson.True || !root.Get("data").IsObject() {
			return nil, fmt.Errorf("Cline upstream did not return success")
		}
		root = root.Get("data")
	}
	if jsonutil.ValidateUniqueFields([]byte(root.Raw)) != nil {
		return nil, fmt.Errorf("ambiguous Cline JSON response")
	}
	if !root.Get("choices").IsArray() || len(root.Get("choices").Array()) == 0 || (root.Get("error").Exists() && root.Get("error").Type != gjson.Null) {
		return nil, fmt.Errorf("Cline upstream response has no choices")
	}
	for _, choice := range root.Get("choices").Array() {
		if !validClineChatChoice(choice) {
			return nil, fmt.Errorf("Cline upstream response has an invalid choice")
		}
	}
	return []byte(root.Raw), nil
}

// 非空 choices 仍可能只有 null 或元数据；完整消息、工具、拒绝和显式空完成均保留。
func validClineChatChoice(choice gjson.Result) bool {
	if !choice.IsObject() || jsonutil.ValidateUniqueFields([]byte(choice.Raw)) != nil {
		return false
	}
	message := choice.Get("message")
	if !message.IsObject() || jsonutil.ValidateUniqueFields([]byte(message.Raw)) != nil {
		return false
	}
	var decoded apicompat.ChatChoice
	if json.Unmarshal([]byte(choice.Raw), &decoded) != nil {
		return false
	}
	if role := message.Get("role"); role.Exists() && (role.Type != gjson.String || role.String() != "assistant") {
		return false
	}
	if index := choice.Get("index"); index.Exists() {
		if index.Type != gjson.Number {
			return false
		}
		if _, ok := jsonutil.ParseNonNegativeInt(index.Raw, 1<<31-1); !ok {
			return false
		}
	}
	content := message.Get("content")
	if content.Exists() && content.Type != gjson.Null && content.Type != gjson.String && !content.IsArray() {
		return false
	}
	if content.IsArray() {
		for _, part := range content.Array() {
			if !part.IsObject() || part.Get("type").Type != gjson.String {
				return false
			}
			if part.Get("type").String() == "text" && part.Get("text").Type != gjson.String {
				return false
			}
			if part.Get("type").String() == "refusal" && part.Get("refusal").Type != gjson.String {
				return false
			}
		}
	}
	for _, tool := range message.Get("tool_calls").Array() {
		if !tool.IsObject() || !tool.Get("function").IsObject() || strings.TrimSpace(tool.Get("function.name").String()) == "" {
			return false
		}
	}
	if call := message.Get("function_call"); call.Exists() && call.Type != gjson.Null {
		if !call.IsObject() || strings.TrimSpace(call.Get("name").String()) == "" {
			return false
		}
	}
	if content.Type == gjson.String || content.IsArray() || len(decoded.Message.ToolCalls) > 0 || decoded.Message.FunctionCall != nil ||
		decoded.Message.Refusal != "" || decoded.Message.Reasoning != "" || decoded.Message.ReasoningContent != "" {
		return true
	}
	// 显式完成允许空 content/null；不能把缺失整个 message 的畸形 choice 视作空完成。
	return strings.TrimSpace(decoded.FinishReason) != ""
}

const (
	DefaultTypeSafeBaseURL             = "https://api.typesafe.ai"
	DefaultTypeSafeModel               = "jev-latest"
	DefaultClineBaseURL                = "https://api.cline.bot/api/v1"
	DefaultCommandCodeBaseURL          = "https://api.commandcode.ai/provider/v1"
	DefaultCommandCodeAnthropicBaseURL = "https://api.commandcode.ai/provider"
	DefaultClineTestModel              = "deepseek/deepseek-v4-flash"
	DefaultClinePassTestModel          = "cline-pass/glm-5.3-flash"
	DefaultCommandCodeTestModel        = "deepseek/deepseek-v4-flash"
)

// 新平台仅扩展原有账号契约，不把独立 Video 或国产平台重新归类。
const (
	clinePassRateLimitKey    = "cline:pass"
	clineCreditsRateLimitKey = "cline:credits"
)

// 同一 Cline Key 的积分、订阅和免费模型互不影响。
func clineWalletRateLimitKey(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || strings.HasPrefix(model, "cline-free/") {
		return ""
	}
	if strings.HasPrefix(model, "cline-pass/") {
		return clinePassRateLimitKey
	}
	return clineCreditsRateLimitKey
}

func isAdditionalAPIKeyPlatform(platform string) bool {
	return platform == PlatformTypeSafe || platform == PlatformCline || platform == PlatformCommandCode
}

func (a *Account) IsTypeSafe() bool    { return a != nil && a.Platform == PlatformTypeSafe }
func (a *Account) IsCline() bool       { return a != nil && a.Platform == PlatformCline }
func (a *Account) IsCommandCode() bool { return a != nil && a.Platform == PlatformCommandCode }

// 新聚合供应商的协议地址可覆盖，但不能把缺失的中继协议转向官方并泄露中继密钥。
func (a *Account) additionalProviderBaseURL(protocol string) string {
	if a == nil {
		return ""
	}
	if urls, ok := a.Credentials["api_base_urls"].(map[string]any); ok {
		if base, ok := urls[protocol].(string); ok && strings.TrimSpace(base) != "" {
			return strings.TrimSpace(base)
		}
	}
	if base := strings.TrimSpace(a.GetCredential("base_url")); base != "" {
		if protocol == APIProtocolAnthropic && a.IsCommandCode() {
			return strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
		}
		return base
	}
	switch a.Platform {
	case PlatformTypeSafe:
		return DefaultTypeSafeBaseURL
	case PlatformCline:
		return DefaultClineBaseURL
	case PlatformCommandCode:
		if protocol == APIProtocolAnthropic {
			return DefaultCommandCodeAnthropicBaseURL
		}
		return DefaultCommandCodeBaseURL
	}
	return ""
}

// @project-doc docs/interfaces/aggregator_upstreams.md#provider_protocols
func normalizeAdditionalProviderCredentials(account *Account, isCreate bool) error {
	if account.Type != AccountTypeAPIKey {
		return infraerrors.BadRequest("PROVIDER_ACCOUNT_TYPE_INVALID", "This platform only supports API Key accounts")
	}
	if account.Credentials == nil {
		account.Credentials = make(map[string]any)
	}
	if mode := strings.TrimSpace(account.GetCredential("account_mode")); mode != "" && mode != AccountModePayG {
		return infraerrors.BadRequest("PROVIDER_MODE_INVALID", "This platform uses one API Key account mode")
	}
	protocol := strings.TrimSpace(account.GetCredential("api_protocol"))
	if account.IsTypeSafe() {
		if protocol != "" && protocol != APIProtocolSystemOne {
			return infraerrors.BadRequest("PROVIDER_PROTOCOL_INVALID", "TypeSafe only supports System One")
		}
		account.Credentials["api_protocol"] = APIProtocolSystemOne
	} else if account.IsCline() {
		if protocol != "" && protocol != APIProtocolChatCompletions {
			return infraerrors.BadRequest("PROVIDER_PROTOCOL_INVALID", "Cline only supports Chat Completions upstream")
		}
		account.Credentials["api_protocol"] = APIProtocolChatCompletions
	} else {
		if protocol == "" {
			protocol = APIProtocolAdaptive
		}
		if protocol != APIProtocolAdaptive && !isNativeOpenCodeGoProtocol(protocol) {
			return infraerrors.BadRequest("PROVIDER_PROTOCOL_INVALID", "Unsupported upstream protocol")
		}
		account.Credentials["api_protocol"] = protocol
		if err := NormalizeOpenCodeGoProtocolRulesCredentials(account.Credentials); err != nil {
			return err
		}
	}
	if isCreate {
		account.Credentials["account_mode"] = AccountModePayG
	}
	return nil
}

// Command Code 优先使用管理员固定协议和模型规则，再查询按账号隔离的目录。
func (s *OpenAIGatewayService) modelRoutedUpstreamProtocol(ctx context.Context, account *Account, inbound, model string) string {
	if !account.IsCommandCode() {
		return openCodeGoNativeProtocol(account, model)
	}
	if protocol := account.GetAPIProtocol(); protocol != APIProtocolAdaptive {
		return protocol
	}
	if rules, exists := account.openCodeGoProtocolRules(); exists {
		return matchOpenCodeGoProtocolRules(model, rules)
	}
	if protocols := s.modelCatalogProtocols(ctx, account, model); len(protocols) > 0 {
		for _, protocol := range protocols {
			if protocol == inbound {
				return protocol
			}
		}
		return protocols[0]
	}
	model = strings.ToLower(lastOpenAIModelSegment(model))
	switch {
	case strings.HasPrefix(model, "claude-"):
		return APIProtocolAnthropic
	case strings.HasPrefix(model, "gpt-"):
		if inbound == APIProtocolChatCompletions {
			return inbound
		}
		return APIProtocolResponses
	default:
		return APIProtocolChatCompletions
	}
}

// 原生 Jev 只走结构化协议；模型映射不能改变平台边界。
func (a *Account) supportsSystemOneModel(model string) bool {
	if a.IsTypeSafe() {
		return strings.TrimSpace(model) == DefaultTypeSafeModel
	}
	return a.IsOpenCodeZen() && IsOpenCodeSystemOneModel(model)
}

// 官方账户查询不接受同名中继主机或 HTTP，避免把密钥发往错误站点。
func isOfficialProviderHost(raw, host string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), host) && (u.Port() == "" || u.Port() == "443")
}

func systemOneClientRequestError(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge || status == http.StatusUnprocessableEntity
}
