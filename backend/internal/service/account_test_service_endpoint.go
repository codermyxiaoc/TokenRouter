package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/claude"
	"github.com/TokenFlux/TokenRouter/internal/pkg/openai"
	"github.com/TokenFlux/TokenRouter/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
)

// AccountTestOptions 只承载本次管理员测试的素材，不写入账号或任务记录。
type AccountTestOptions struct {
	ImageDataURL string `json:"image_data_url"`
	AudioDataURL string `json:"audio_data_url"`
}

// TestAccountConnectionWithOptions 在旧测试入口外提供显式端点和媒体模式。
// @project-doc docs/interfaces/http_api.md#account_connection_tests
func (s *AccountTestService) TestAccountConnectionWithOptions(c *gin.Context, accountID int64, modelID, prompt, testType, mode, endpoint string, options AccountTestOptions) error {
	// 验证失败也采用同一 SSE 契约，避免浏览器把首个错误误判为普通文本。
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	if endpoint == "" {
		endpoint = "auto"
	}
	if endpoint == "messages" {
		endpoint = APIProtocolAnthropic
	}
	testType = strings.ToLower(strings.TrimSpace(testType))
	switch testType {
	case "", AccountTestTypeText, AccountTestTypeImage, "video", "search", "tts", "stt", "realtime":
	default:
		return s.sendErrorAndEnd(c, "Unsupported account test type")
	}
	if testType == "" {
		// 旧客户端可能把 image/text 放在 mode 中，新 Grok 分支也保留这一语义。
		mode, testType, _ = resolveAccountTestModeAndType(mode)
	}
	switch endpoint {
	case "auto", APIProtocolChatCompletions, APIProtocolResponses, APIProtocolAnthropic, GroupAvailabilityProbeProtocolGemini, APIProtocolSystemOne:
	default:
		return s.sendErrorAndEnd(c, "Unsupported account test endpoint")
	}
	if endpoint != "auto" && testType != "" && testType != AccountTestTypeText {
		return s.sendErrorAndEnd(c, "Test endpoint selection is only supported for text tests")
	}
	if endpoint != "auto" && endpoint != APIProtocolResponses && normalizeAccountTestMode(mode) != AccountTestModeDefault {
		return s.sendErrorAndEnd(c, "Compact tests require the Responses endpoint")
	}
	account, err := s.accountRepo.GetByID(c.Request.Context(), accountID)
	if err != nil || account == nil {
		return s.sendErrorAndEnd(c, "Account not found")
	}
	// Compact 是 OpenAI 账号的专项能力，不能在其它平台静默变成普通文字测试。
	if endpoint != "auto" && account.Platform != PlatformOpenAI && normalizeAccountTestMode(mode) != AccountTestModeDefault {
		return s.sendErrorAndEnd(c, "Compact tests are only supported for OpenAI accounts")
	}
	if account.Platform == PlatformGrok {
		if endpoint != "auto" && endpoint != APIProtocolResponses {
			return s.sendErrorAndEnd(c, "Selected endpoint is not supported for Grok account tests")
		}
		return s.testGrokAccountConnectionWithOptions(c, account, modelID, prompt, testType, options)
	}
	if testType != "" && testType != AccountTestTypeText && testType != AccountTestTypeImage {
		return s.sendErrorAndEnd(c, "Selected test type is only supported for Grok accounts")
	}
	if options.ImageDataURL != "" || options.AudioDataURL != "" {
		return s.sendErrorAndEnd(c, "Test attachments are only supported for Grok accounts")
	}
	if endpoint == "auto" {
		// 省略端点时保持图片模型推断、Compact、定时测试等既有行为。
		return s.TestAccountConnectionWithType(c, accountID, modelID, prompt, testType, mode)
	}
	if !supportsAccountTestEndpoint(account, endpoint) {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Test endpoint %s is not supported for %s %s accounts", endpoint, account.Platform, account.Type))
	}
	// 仅复制本次可能修改的顶层配置，不修改账号持久协议或共享仓储快照。
	copy := *account
	copy.Credentials = maps.Clone(account.Credentials)
	copy.Extra = maps.Clone(account.Extra)
	if copy.Extra == nil {
		copy.Extra = make(map[string]any)
	}
	account = &copy
	if account.IsOpenAI() && endpoint != APIProtocolAnthropic {
		route := openai_compat.TextRouteModeForceResponses
		if endpoint == APIProtocolChatCompletions {
			route = openai_compat.TextRouteModeForceChatCompletions
		}
		account.Extra[openai_compat.ExtraKeyTextRouteMode] = string(route)
		if err := s.prepareOpenAIAutomaticProbe(c, account); err != nil {
			return s.sendErrorAndEnd(c, err.Error())
		}
		return s.testOpenAIAccountConnection(c, account, modelID, prompt, mode, AccountTestTypeText)
	}
	if account.IsGemini() {
		return s.testGeminiAccountConnection(c, account, modelID, prompt, AccountTestTypeText)
	}
	if account.Platform == PlatformAnthropic && account.Type != AccountTypeAPIKey {
		return s.testClaudeAccountConnection(c, account, modelID, prompt)
	}
	return s.testSelectedAPIKeyEndpoint(c, account, modelID, prompt, endpoint)
}

// supportsAccountTestEndpoint 限制凭据可用的原生协议；兼容 API Key 由上游返回实际能力。
func supportsAccountTestEndpoint(account *Account, endpoint string) bool {
	if account == nil {
		return false
	}
	textProtocol := endpoint == APIProtocolChatCompletions || endpoint == APIProtocolResponses || endpoint == APIProtocolAnthropic
	switch account.Platform {
	case PlatformOpenAI:
		return account.Type == AccountTypeAPIKey && textProtocol || account.IsOAuth() && endpoint == APIProtocolResponses
	case PlatformAnthropic:
		if account.Type == AccountTypeAPIKey {
			return textProtocol
		}
		return endpoint == APIProtocolAnthropic && (account.IsOAuth() || account.IsBedrock() || account.Type == AccountTypeServiceAccount)
	case PlatformKimi, PlatformDeepseek, PlatformMiniMax:
		return account.Type == AccountTypeAPIKey && textProtocol
	case PlatformZhipu:
		return account.Type == AccountTypeAPIKey && (endpoint == APIProtocolChatCompletions || endpoint == APIProtocolAnthropic)
	case PlatformOpenCodeGo:
		return account.Type == AccountTypeAPIKey && (textProtocol || endpoint == APIProtocolSystemOne)
	case PlatformGemini:
		return endpoint == GroupAvailabilityProbeProtocolGemini && (account.Type == AccountTypeAPIKey || account.IsOAuth() || account.Type == AccountTypeServiceAccount)
	}
	return false
}

// selectedAccountTestBaseURL 保留中继地址；自适应账号优先使用所选协议的专用地址。
func selectedAccountTestBaseURL(account *Account, protocol string) string {
	if account.IsOpenCodeGo() {
		return account.GetCNProtocolBaseURL(protocol)
	}
	if account.IsCNProvider() {
		if account.IsAdaptiveAPIProtocol() {
			return account.GetCNProtocolBaseURL(protocol)
		}
		if base := strings.TrimSpace(account.GetCredential("base_url")); base != "" {
			return base
		}
		return account.GetCNProtocolBaseURL(protocol)
	}
	if base := strings.TrimSpace(account.GetCredential("base_url")); base != "" {
		return base
	}
	if account.Platform == PlatformAnthropic {
		return "https://api.anthropic.com"
	}
	return "https://api.openai.com"
}

// testSelectedAPIKeyEndpoint 按明确协议构造原生载荷，不再按模型将请求换到另一协议。
func (s *AccountTestService) testSelectedAPIKeyEndpoint(c *gin.Context, account *Account, modelID, prompt, protocol string) error {
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if apiKey == "" {
		return s.sendErrorAndEnd(c, "No API key available")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		modelID = defaultCNProviderTestModel(account.Platform)
		if modelID == "" {
			modelID = openai.DefaultTestModel
			if account.Platform == PlatformAnthropic {
				modelID = claude.DefaultTestModel
			}
		}
	}
	modelID = account.GetMappedModel(modelID)
	if account.IsOpenCodeGo() {
		isSystemOne := IsOpenCodeSystemOneModel(modelID)
		if (protocol == APIProtocolSystemOne) != isSystemOne {
			return s.sendErrorAndEnd(c, "OpenCode Jev models require the System One test endpoint; other models require a text endpoint")
		}
		if isSystemOne {
			return s.testOpenCodeSystemOneConnection(c, account, modelID, prompt, apiKey)
		}
	}
	baseURL, err := s.validateUpstreamBaseURL(selectedAccountTestBaseURL(account, protocol))
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Invalid base URL: %s", err))
	}
	var payload any
	apiURL := ""
	switch protocol {
	case APIProtocolAnthropic:
		apiURL = buildOpenAIEndpointURL(baseURL, "/v1/messages")
		payload, err = createTestPayloadWithPrompt(modelID, prompt)
	case APIProtocolResponses:
		apiURL = buildOpenAIResponsesURLForPlatform(account.Platform, baseURL)
		body := createOpenAITestPayload(modelID, prompt, false)
		body["store"] = false
		delete(body, "instructions")
		payload = body
	case APIProtocolChatCompletions:
		apiURL = buildOpenAIChatCompletionsURL(baseURL)
		payload = createOpenAIChatCompletionsTestPayload(modelID, prompt)
	}
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create test payload")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to encode test payload")
	}
	if protocol == APIProtocolResponses {
		body = normalizeDeepSeekResponsesRequestBody(account, body)
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create test request")
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if protocol == APIProtocolAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
		setAnthropicAPIKeyAuthHeader(req.Header, account, apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	applyAccountTestUserAgent(req)
	applyOpenCodeUpstreamUserAgent(account, apiURL, req.Header)
	account.ApplyHeaderOverrides(req.Header)
	applyOpenCodeSessionHeader(c, account, apiURL, req.Header, body)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: modelID})
	s.sendEvent(c, TestEvent{Type: "status", Text: "Testing endpoint: " + req.URL.Path})
	resp, err := s.doCNProviderAdaptiveRequest(req, account)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Test endpoint request failed: %s", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		// 单个端点不可用不代表整个账号失效，不持久化修改其他协议的调度资格。
		return s.sendErrorAndEnd(c, fmt.Sprintf("Test endpoint returned %d: %s", resp.StatusCode, errorBody))
	}
	switch protocol {
	case APIProtocolAnthropic:
		return s.processClaudeStream(c, resp.Body)
	case APIProtocolResponses:
		return s.processOpenAIStream(c, resp.Body)
	default:
		return s.processOpenAIChatCompletionsStream(c, resp.Body)
	}
}
