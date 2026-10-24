package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// 门禁与不支持的账号类型可在服务未装配时提前返回，图片探测不应改变这条错误路径。
func (s *GeminiMessagesCompatService) imageCooldownStore() GatewayCache {
	if s == nil {
		return nil
	}
	return s.cache
}

func (s *AntigravityGatewayService) imageCooldownStore() GatewayCache {
	if s == nil {
		return nil
	}
	return s.cache
}

// GeminiImageAttemptOperation 区分带图与纯文本生图，避免一次编辑故障影响正常文生图。
func GeminiImageAttemptOperation(body []byte) string {
	root := gjson.ParseBytes(body)
	if wrapped := root.Get("request"); wrapped.IsObject() {
		root = wrapped
	}
	for _, content := range root.Get("contents").Array() {
		for _, part := range content.Get("parts").Array() {
			for _, key := range []string{"inlineData", "inline_data", "fileData", "file_data"} {
				if part.Get(key).IsObject() {
					return CreativeOperationEdit
				}
			}
		}
	}
	return CreativeOperationGenerate
}

// 模型别名直到选中账号后才能识别；将同一预算放回请求，交给最外层 handler 清理。
// 管理员指定账号测试没有用户 API Key，不隐式引入换号或新的截止时间。
func prepareGeminiImageAttempt(ctx context.Context, c *gin.Context, cache any, account *Account, body []byte, finalModel string, models ...string) (context.Context, error) {
	if !ImageAttemptHasPolicy(ctx) {
		if c == nil || getAPIKeyFromContext(c) == nil || !isGeminiImageGenerationIntent(body, append(models, finalModel)...) {
			return ctx, nil
		}
		ctx, _ = WithImageAttemptPolicy(ctx, DefaultPublicImageAttemptTimeout)
		if c != nil && c.Request != nil {
			c.Request = c.Request.WithContext(ctx)
		}
	}
	return ctx, nil
}

// 实际转换后的请求体才确定操作类型；读取与失败写入必须使用同一个最终模型和操作。
func imageModelCoolingError(ctx context.Context, cache any, accountID int64, finalModel, operation string) error {
	started := time.Now()
	if remaining := ImageModelCooldownRemaining(ctx, cache, accountID, finalModel, operation); remaining > 0 {
		decision := ImageAttemptDecision{Outcome: ImageAttemptNotSent, RetryDifferentAccount: true, Reason: "image_model_cooling"}
		deadline := started.Add(remaining)
		remaining = time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		retryAfter := max(1, int((remaining+time.Second-1)/time.Second))
		return RecordImageAttemptFailure(ctx, &imageModelCooldownError{deadline: deadline, err: &UpstreamFailoverError{
			StatusCode:      http.StatusServiceUnavailable,
			ResponseHeaders: http.Header{"Retry-After": {strconv.Itoa(retryAfter)}},
			ResponseBody:    []byte(`{"error":{"message":"Image model is temporarily cooling down","code":"image_model_cooling"}}`),
		}}, decision)
	}
	return nil
}

// 图片只执行一次底层请求；既有错误策略继续处理响应，但内部退避和换号必须遵守共同决策。
func doGeminiImageAwareRequest(ctx context.Context, upstream HTTPUpstream, req *http.Request, proxy string, account *Account, cache any, finalModel, operation string) (*http.Response, error) {
	if !ImageAttemptHasPolicy(ctx) {
		return upstream.Do(req, proxy, account.ID, account.Concurrency)
	}
	if err := imageModelCoolingError(ctx, cache, account.ID, finalModel, operation); err != nil {
		return nil, err
	}
	if err := BeginImageAttempt(ctx, account.ID, finalModel, operation); err != nil {
		return nil, err
	}
	resp, err := upstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil || resp == nil {
		if err == nil {
			err = errors.New("image upstream returned no response")
		}
		decision := ClassifyImageTransportFailure(err)
		if decision.AttemptFault {
			_ = RecordImageModelCooldown(ctx, cache, account.ID, finalModel, operation, DefaultImageModelCooldown)
		}
		return nil, RecordImageAttemptFailure(ctx, err, decision)
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	if resp.StatusCode >= 400 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, gatewayUpstreamErrorBodyReadLimit+1))
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		decision := ClassifyImageHTTPFailure(resp.StatusCode, body)
		if readErr != nil || int64(len(body)) > gatewayUpstreamErrorBodyReadLimit {
			decision = ImageAttemptDecision{Outcome: ImageAttemptUnknown, AttemptFault: true, Reason: "image_error_response_incomplete"}
		}
		// Google 的任务用量和包装响应也是受理证据，不能只认识 OpenAI 的 usage。
		if gjson.ValidBytes(body) {
			for _, path := range []string{"usageMetadata", "usage_metadata", "response.usageMetadata", "response.usage_metadata"} {
				if gjson.GetBytes(body, path).IsObject() {
					decision = ImageAttemptDecision{Outcome: ImageAttemptAccepted, Reason: "image_usage_observed"}
				}
			}
			for _, prefix := range []string{"", "response."} {
				for _, candidate := range gjson.GetBytes(body, prefix+"candidates").Array() {
					for _, part := range candidate.Get("content.parts").Array() {
						if strings.TrimSpace(part.Get("inlineData.data").String()) != "" || strings.TrimSpace(part.Get("inline_data.data").String()) != "" {
							decision = ImageAttemptDecision{Outcome: ImageAttemptResultReady, Reason: "image_output_observed"}
						}
					}
				}
			}
		}
		// 在共享层写入健康状态之前尊重账号自定义错误码，避免外层关闭故障处理后仍留下冷却。
		if account.IsCustomErrorCodesEnabled() && !account.ShouldHandleErrorCode(resp.StatusCode) {
			decision.RetryDifferentAccount, decision.AttemptFault = false, false
		}
		RecordImageAttemptDecision(ctx, decision)
		if decision.AttemptFault {
			_ = RecordImageModelCooldown(ctx, cache, account.ID, finalModel, operation, DefaultImageModelCooldown)
		}
		return resp, nil
	}
	// HTTP 成功之后读取失败也不能再次发送生成，读取故障可短期避让后续新请求。
	RecordImageAttemptDecision(ctx, ImageAttemptDecision{Outcome: ImageAttemptAccepted, Reason: "image_response_started"})
	resp.Body = &imageAttemptResponseBody{ReadCloser: resp.Body, ctx: ctx, cache: cache, accountID: account.ID, model: finalModel, operation: operation}
	return resp, nil
}

type imageAttemptResponseBody struct {
	io.ReadCloser
	ctx              context.Context
	cache            any
	accountID        int64
	model, operation string
	once             sync.Once
}

func (b *imageAttemptResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.once.Do(func() {
			decision := ClassifyImageTransportFailure(err)
			decision.Outcome, decision.RetryDifferentAccount = ImageAttemptUnknown, false
			RecordImageAttemptDecision(b.ctx, decision)
			// 生成总预算耗尽仍是账号故障；只有调用方主动取消不写冷却。
			if decision.AttemptFault && !errors.Is(b.ctx.Err(), context.Canceled) {
				_ = RecordImageModelCooldown(b.ctx, b.cache, b.accountID, b.model, b.operation, DefaultImageModelCooldown)
			}
		})
	}
	return n, err
}
