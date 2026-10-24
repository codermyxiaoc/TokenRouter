package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// 某些兼容上游以 HTTP200 返回失败 JSON；已受理不等于成功，不允许重生或伪造零用量成功记录。
func responsesImageJSONFailure(ctx context.Context, body []byte) error {
	if !ImageAttemptHasPolicy(ctx) || !gjson.ValidBytes(body) {
		return nil
	}
	failed := gjson.GetBytes(body, "status").String() == "failed"
	errorValue := gjson.GetBytes(body, "error")
	if !failed && (!errorValue.IsObject() || len(errorValue.Map()) == 0) {
		return nil
	}
	return RecordImageAttemptFailure(ctx, errors.New("image generation returned a failed response"), ImageAttemptDecision{Outcome: ImageAttemptAccepted, Reason: "image_response_failed"})
}

// HTTP Responses 的显式生图与图片端点共享执行预算；纯文本、管理员及交互式 WS 保持原行为。
func prepareOpenAIResponsesImageAttempt(ctx context.Context, c *gin.Context, account *Account, body []byte, finalModel string) context.Context {
	if ImageAttemptHasPolicy(ctx) || c == nil || c.Request == nil || getAPIKeyFromContext(c) == nil || account == nil || account.Platform != PlatformOpenAI || GetOpenAIClientTransport(c) == OpenAIClientTransportWS {
		return ctx
	}
	if !IsExplicitImageGenerationIntent(openAIResponsesEndpoint, finalModel, body) {
		return ctx
	}
	ctx, _ = WithImageAttemptPolicy(ctx, DefaultPublicImageAttemptTimeout)
	c.Request = c.Request.WithContext(ctx)
	return ctx
}

// 以真正发送的工具模型和图片输入确定健康范围，不把图片故障扩大到同账号的文本主模型。
func openAIResponsesImageAttemptScope(body []byte, finalModel string) (string, string) {
	model := strings.TrimSpace(finalModel)
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("type").String() == "image_generation" {
			model = strings.TrimSpace(tool.Get("model").String())
			if model == "" {
				model = "gpt-image-2"
			}
			if tool.Get("input_image_mask").IsObject() {
				return model, CreativeOperationInpaint
			}
			break
		}
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "input_image" {
			return model, CreativeOperationEdit
		}
		for _, part := range item.Get("content").Array() {
			if part.Get("type").String() == "input_image" {
				return model, CreativeOperationEdit
			}
		}
	}
	return model, CreativeOperationGenerate
}

// 适配器保留既有 TLS 指纹和 OAuth 插件出口，只复用单次 HTTP 请求的受理证据规则。
type openAIResponsesImageHTTPAdapter struct {
	HTTPUpstream
	service *OpenAIGatewayService
	account *Account
	profile *tlsfingerprint.Profile
}

func (a openAIResponsesImageHTTPAdapter) Do(req *http.Request, proxy string, _ int64, _ int) (*http.Response, error) {
	return a.service.doOpenAIUpstream(req, proxy, a.account, a.profile)
}

func (s *OpenAIGatewayService) doOpenAIResponsesImageAwareRequest(ctx context.Context, c *gin.Context, req *http.Request, proxy string, account *Account, body []byte, finalModel string, profile *tlsfingerprint.Profile) (*http.Response, error) {
	if !ImageAttemptHasPolicy(ctx) {
		return s.doOpenAIUpstream(req, proxy, account, profile)
	}
	model, operation := openAIResponsesImageAttemptScope(body, finalModel)
	upstream := openAIResponsesImageHTTPAdapter{service: s, account: account, profile: profile}
	resp, err := doGeminiImageAwareRequest(ctx, upstream, req, proxy, account, s.cache, model, operation)
	if err != nil || resp == nil || resp.StatusCode < http.StatusBadRequest {
		return resp, err
	}
	// 共享读取层已完整判定错误体；此处沿用其结论，不能把截断的 429 再降级成确定拒绝。
	decision, _ := ImageAttemptLastDecision(ctx)
	bodyBytes, _ := readImageAttemptErrorBody(resp)
	_ = resp.Body.Close()
	if account.IsCustomErrorCodesEnabled() && !account.ShouldHandleErrorCode(resp.StatusCode) {
		decision.RetryDifferentAccount, decision.AttemptFault = false, false
	}
	if decision.AttemptFault {
		s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, bodyBytes, model)
	}
	next := NextAccountStop
	kind := "http_error"
	if ImageAttemptCanSwitch(decision) {
		next, kind = NextAccountRetry, "failover"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"), Kind: kind, Message: "image generation upstream HTTP failure"})
	return nil, RecordImageAttemptFailure(ctx, &UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseHeaders: resp.Header.Clone(), ResponseBody: bodyBytes, NextAccountAction: next}, decision)
}
