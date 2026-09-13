package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// ForwardResponsesInputTokens 转发 OpenAI 原生 POST /responses/input_tokens。
// 不支持该预检端点的账号使用本地估算，避免把已知不兼容请求发送到上游。
func (s *OpenAIGatewayService) ForwardResponsesInputTokens(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) error {
	if account == nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusServiceUnavailable, "api_error", "No available OpenAI accounts")
		return fmt.Errorf("responses input_tokens: missing account")
	}

	prepared, err := prepareNativeOpenAIInputTokensCountRequest(body, account)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return err
	}

	if shouldEstimateOpenAIInputTokensLocally(account) {
		writeOpenAIResponsesInputTokensFallback(c, account, prepared, 0, "local_account")
		return nil
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to get access token")
		return fmt.Errorf("responses input_tokens: get access token: %w", err)
	}

	upstreamBody, err := marshalOpenAIUpstreamJSON(prepared.Request)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("responses input_tokens: marshal request: %w", err)
	}
	upstreamReq, err := s.buildInputTokensUpstreamRequest(ctx, c, account, upstreamBody, token)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("responses input_tokens: build request: %w", err)
	}
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	if s.httpUpstream == nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return fmt.Errorf("responses input_tokens: upstream client is unavailable")
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return fmt.Errorf("responses input_tokens: upstream request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := s.readResponsesInputTokensBody(resp)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to read response")
		return err
	}
	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusNotFound ||
			(account.Type == AccountTypeOAuth && isOpenAIOAuthInputTokensUnsupported(resp.StatusCode, respBody)) {
			writeOpenAIResponsesInputTokensFallback(c, account, prepared, resp.StatusCode, "upstream_unsupported")
			return nil
		}
		return s.handleResponsesInputTokensUpstreamError(ctx, c, account, prepared, resp, respBody)
	}

	inputTokens := gjson.GetBytes(respBody, "input_tokens")
	if !inputTokens.Exists() || inputTokens.Type != gjson.Number {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream response missing input_tokens")
		return fmt.Errorf("responses input_tokens: upstream response missing input_tokens")
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(http.StatusOK, contentType, respBody)
	return nil
}

func prepareNativeOpenAIInputTokensCountRequest(body []byte, account *Account) (*openAIInputTokensCountPrepared, error) {
	var req openAIInputTokensCountRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse responses input_tokens request: %w", err)
	}
	originalModel := strings.TrimSpace(req.Model)
	if originalModel == "" {
		return nil, fmt.Errorf("parse responses input_tokens request: model is required")
	}
	billingModel := resolveOpenAIForwardModel(account, originalModel, "")
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	req.Model = upstreamModel
	return &openAIInputTokensCountPrepared{
		Request:         req,
		OriginalModel:   originalModel,
		NormalizedModel: originalModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
	}, nil
}

func shouldEstimateOpenAIInputTokensLocally(account *Account) bool {
	if account == nil || account.IsGrok() || account.IsCNProvider() || account.Type == AccountTypeUpstream {
		return true
	}
	if account.Type != AccountTypeAPIKey {
		return false
	}
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	if baseURL == "" {
		return false
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return true
	}
	return !strings.EqualFold(parsed.Hostname(), "api.openai.com")
}

func writeOpenAIResponsesInputTokensFallback(c *gin.Context, account *Account, prepared *openAIInputTokensCountPrepared, statusCode int, reason string) {
	estimated := openAIInputTokensFallbackMinimum
	if prepared != nil {
		if got, err := estimateOpenAIInputTokens(prepared.Request); err == nil && got > 0 {
			estimated = got
		}
	}
	accountID := int64(0)
	model := ""
	if account != nil {
		accountID = account.ID
	}
	if prepared != nil {
		model = prepared.UpstreamModel
	}
	logger.L().Info("openai responses input_tokens: local estimate fallback",
		zap.Int64("account_id", accountID),
		zap.Int("upstream_status", statusCode),
		zap.Int("estimated_input_tokens", estimated),
		zap.String("upstream_model", model),
		zap.String("reason", reason),
	)
	c.JSON(http.StatusOK, gin.H{
		"object":       "response.input_tokens",
		"input_tokens": estimated,
	})
}

func writeOpenAIResponsesInputTokensError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{"error": gin.H{"type": errType, "message": message}})
}

func (s *OpenAIGatewayService) readResponsesInputTokensBody(resp *http.Response) ([]byte, error) {
	body := s.readUpstreamErrorBody(resp)
	if len(body) == 0 {
		return nil, fmt.Errorf("responses input_tokens: empty upstream response")
	}
	return body, nil
}

func (s *OpenAIGatewayService) handleResponsesInputTokensUpstreamError(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	prepared *openAIInputTokensCountPrepared,
	resp *http.Response,
	body []byte,
) error {
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	var decision UpstreamErrorDecision
	if account.Platform == PlatformGrok {
		decision = s.applyGrokAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, prepared.UpstreamModel)
	} else {
		decision = s.applyOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, prepared.UpstreamModel)
	}
	if decision.ShouldReturnGenericError() {
		writeOpenAIResponsesInputTokensError(c, http.StatusInternalServerError, "upstream_error", "Upstream gateway error")
		return fmt.Errorf("responses input_tokens: upstream error %d (custom policy)", resp.StatusCode)
	}
	defaultFailover := s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMsg, body)
	if account.Platform == PlatformGrok {
		defaultFailover = s.shouldFailoverGrokUpstreamError(resp.StatusCode, body)
	}
	if decision.ShouldFailover(account, resp.StatusCode, defaultFailover) {
		return &UpstreamFailoverError{
			StatusCode:             resp.StatusCode,
			ResponseBody:           body,
			ResponseHeaders:        resp.Header.Clone(),
			RetryableOnSameAccount: decision.RetryableOnSameAccount(account, resp.StatusCode),
		}
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, "")
	message := "Upstream request failed"
	if resp.StatusCode == http.StatusTooManyRequests {
		message = "Rate limit exceeded"
	} else if resp.StatusCode >= 500 {
		message = "Upstream service temporarily unavailable"
	}
	writeOpenAIResponsesInputTokensError(c, resp.StatusCode, "upstream_error", message)
	if upstreamMsg == "" {
		return fmt.Errorf("responses input_tokens: upstream error %d", resp.StatusCode)
	}
	return fmt.Errorf("responses input_tokens: upstream error %d message=%s", resp.StatusCode, upstreamMsg)
}
