package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const maxOpenCodeSystemOneResponseBytes = 16 << 20

// OpenAIEndpointCapabilitySystemOne 仅供 OpenCode Zen 原生结构化端点调度。
const OpenAIEndpointCapabilitySystemOne OpenAIEndpointCapability = "systemone"

// ForwardOpenCodeSystemOne 将 Zen Jev 请求原样转发到 System One。
// Jev 不是 Chat/Responses 的文本协议，必须保留结构化 state/questions，且只
// 在整个 JSON 响应读完后写给客户端，才能在输出前触发智能路由故障转移。
func (s *OpenAIGatewayService) ForwardOpenCodeSystemOne(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*OpenAIForwardResult, error) {
	if !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySystemOne) {
		return nil, fmt.Errorf("OpenCode System One requires a Zen account")
	}

	originalModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	model := normalizeOpenCodeGoModelID(account.GetMappedModel(originalModel))
	if !IsOpenCodeSystemOneModel(model) {
		return nil, fmt.Errorf("model %q is not supported by OpenCode System One", model)
	}
	if model != originalModel {
		body = ReplaceModelInBody(body, model)
	}
	apiKey := strings.TrimSpace(account.GetOpenAIProtocolAPIKey())
	if apiKey == "" {
		return nil, fmt.Errorf("account %d missing api_key", account.ID)
	}
	base, err := s.validateUpstreamBaseURL(account.openCodeProtocolBaseURL(APIProtocolSystemOne))
	if err != nil {
		return nil, fmt.Errorf("invalid OpenCode System One base URL: %w", err)
	}
	target := buildOpenAIEndpointURL(base, "/v1/systemone")
	SetActualOpenAIUpstreamEndpoint(c, "/v1/systemone")
	SetOpsUpstreamModel(c, model)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)

	start := time.Now()
	resp, err := s.doOpenAIUpstream(req, accountProxyURL(account), account, s.resolveOpenAITLSProfile(account))
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(start).Milliseconds())
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, true)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxOpenCodeSystemOneResponseBytes+1))
	if err != nil {
		return nil, s.systemOneResponseFailure(c, account, resp, fmt.Sprintf("read OpenCode System One response: %v", err))
	}
	if len(respBody) > maxOpenCodeSystemOneResponseBytes {
		return nil, s.systemOneResponseFailure(c, account, resp, "OpenCode System One response exceeds size limit")
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
		setOpsUpstreamError(c, resp.StatusCode, message, "")
		if failoverErr := s.failoverOpenAIUpstreamHTTPError(ctx, c, account, resp, respBody, message, model); failoverErr != nil {
			return nil, failoverErr
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID,
			AccountName: account.Name, UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
			Kind: "http_error", Message: message})
		// 参数校验失败不能通过跨组重放掩盖；保持原生错误 JSON。
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
		}
		writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		c.Data(resp.StatusCode, contentType, respBody)
		return nil, fmt.Errorf("OpenCode System One upstream status %d: %s", resp.StatusCode, message)
	}

	// 不能把 HTML、截断 JSON 或缺失计量的响应当成成功，避免成功但漏记扣费。
	usage, validUsage := extractOpenAIUsageFromJSONBytes(respBody)
	if !gjson.ValidBytes(respBody) || !gjson.GetBytes(respBody, "answers").IsObject() || !validUsage {
		return nil, s.systemOneResponseFailure(c, account, resp, "Invalid OpenCode System One response: expected answers and usage")
	}
	writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(resp.StatusCode, contentType, respBody)
	requestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	if requestID == "" {
		requestID = strings.TrimSpace(resp.Header.Get("request-id"))
	}
	return &OpenAIForwardResult{
		RequestID:        requestID,
		UpstreamHeaders:  resp.Header.Clone(),
		ResponseHeaders:  resp.Header.Clone(),
		Usage:            usage,
		Model:            originalModel,
		BillingModel:     model,
		UpstreamModel:    model,
		UpstreamEndpoint: "/v1/systemone",
		Duration:         time.Since(start),
	}, nil
}

// systemOneResponseFailure 记录输出前的损坏响应，交给既有重试流程处理。
func (s *OpenAIGatewayService) systemOneResponseFailure(c *gin.Context, account *Account, resp *http.Response, message string) error {
	setOpsUpstreamError(c, http.StatusBadGateway, message, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID,
		AccountName: account.Name, UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
		Kind: "response_error", Message: message})
	return &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseHeaders: resp.Header.Clone(),
		ResponseBody: []byte(`{"error":{"message":"Invalid System One upstream response"}}`)}
}
