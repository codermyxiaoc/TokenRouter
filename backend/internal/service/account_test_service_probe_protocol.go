package service

import (
	"fmt"
	"maps"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
)

const accountTestExplicitProbeProtocolContextKey = "account_test_explicit_probe_protocol"

// testAccountConnectionForProbeProtocol 仅在分组探测中覆盖单次测试协议。
// 独立复制可修改的配置，避免污染仓储缓存或改变正常业务与管理员完整账号诊断。
func (s *AccountTestService) testAccountConnectionForProbeProtocol(c *gin.Context, accountID int64, modelID, prompt, protocol string) error {
	storedAccount, err := s.accountRepo.GetByID(c.Request.Context(), accountID)
	if err != nil || storedAccount == nil {
		return s.sendErrorAndEnd(c, "Account not found")
	}
	if err := ValidateGroupAvailabilityProbeProtocol(storedAccount.Platform, protocol); err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	account := *storedAccount
	account.Credentials = maps.Clone(storedAccount.Credentials)
	account.Extra = maps.Clone(storedAccount.Extra)
	if account.Credentials == nil {
		account.Credentials = make(map[string]any)
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	c.Set(accountTestExplicitProbeProtocolContextKey, true)

	if account.IsCNProvider() || account.IsOpenCodeGo() {
		if account.Type != AccountTypeAPIKey {
			return s.sendErrorAndEnd(c, "Selected probe protocol requires an API Key account")
		}
		if account.IsOpenCodeGo() && IsOpenCodeSystemOneModel(account.GetMappedModel(modelID)) {
			return s.sendErrorAndEnd(c, "OpenCode Jev uses System One; select auto for this probe model")
		}
		// 必须先解析自适应账号的分协议地址，再固定副本的协议，避免丢失中继主机和路径。
		baseURL := strings.TrimSpace(account.GetCredential("base_url"))
		if account.IsAdaptiveAPIProtocol() || baseURL == "" {
			baseURL = account.GetCNProtocolBaseURL(protocol)
		}
		account.Credentials["base_url"] = baseURL
		account.Credentials["api_protocol"] = protocol
		return s.testCNProviderAccountConnection(c, &account, modelID, prompt)
	}

	switch account.Platform {
	case PlatformOpenAI:
		if protocol == APIProtocolChatCompletions && account.Type != AccountTypeAPIKey {
			return s.sendErrorAndEnd(c, "Chat Completions probing is only supported for OpenAI API Key accounts; select Responses or auto")
		}
		routeMode := openai_compat.TextRouteModeForceResponses
		if protocol == APIProtocolChatCompletions {
			routeMode = openai_compat.TextRouteModeForceChatCompletions
		}
		account.Extra[openai_compat.ExtraKeyTextRouteMode] = string(routeMode)
		if err := s.prepareOpenAIAutomaticProbe(c, &account); err != nil {
			return s.sendErrorAndEnd(c, err.Error())
		}
		// 显式文本协议不得因图片模型名称而意外发起生图请求。
		return s.testOpenAIAccountConnection(c, &account, modelID, prompt, AccountTestModeDefault, AccountTestTypeText)
	case PlatformAnthropic:
		return s.testClaudeAccountConnection(c, &account, modelID, prompt)
	case PlatformGemini:
		return s.testGeminiAccountConnection(c, &account, modelID, prompt, AccountTestTypeText)
	case PlatformGrok:
		return s.testGrokAccountConnection(c, &account, modelID, prompt, AccountTestTypeText)
	default:
		return s.sendErrorAndEnd(c, fmt.Sprintf("Selected probe protocol is not supported for platform %s", account.Platform))
	}
}
