package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAIImagesForceResponsesContextKey struct{}

// 原生端点明确不可用后的单次旧协议回退；旧 Responses 测试也用此标记隔离两条通道。
func withOpenAIImagesForceResponses(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAIImagesForceResponsesContextKey{}, true)
}

func isOpenAIImagesForceResponses(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	forced, _ := ctx.Value(openAIImagesForceResponsesContextKey{}).(bool)
	return forced
}

// @project-doc docs/interfaces/openai_upstream.md#codex_native_images
// 显式列出已接入的模型，不把未来模型或未知快照自动送到直调端点。
func usesCodexDirectImages(model string) bool {
	switch strings.TrimSpace(model) {
	case "gpt-image-1.5", "gpt-image-2",
		"gpt-image-2.5-flare", "gpt-image-2.5-sunburst",
		"gpt-image-2.5-flare-2026-09-08", "gpt-image-2.5-sunburst-2026-09-08":
		return true
	default:
		return false
	}
}

// 正式转发与后台测试共用同一份端点选择和请求构造。
func buildOpenAIImagesOAuthPayload(parsed *OpenAIImagesRequest, model string) ([]byte, string, error) {
	if parsed == nil {
		return nil, "", fmt.Errorf("parsed images request is required")
	}
	if !usesCodexDirectImages(model) {
		body, err := buildOpenAIImagesResponsesRequest(parsed, model)
		return body, chatgptCodexURL, err
	}
	if strings.TrimSpace(parsed.Prompt) == "" {
		return nil, "", fmt.Errorf("prompt is required")
	}
	// 只把已解析并校验的图片字段发送给上游，避免客户端注入任意 JSON 字段。
	payload := make(map[string]any, 16)
	payload["model"] = model
	prompt := parsed.Prompt
	if !parsed.Multipart && gjson.ValidBytes(parsed.Body) {
		if rawPrompt := gjson.GetBytes(parsed.Body, "prompt").String(); rawPrompt != "" {
			prompt = rawPrompt
		}
	}
	payload["prompt"] = prompt
	for _, field := range []struct {
		key   string
		value string
	}{
		{"size", parsed.Size}, {"quality", parsed.Quality},
		{"background", parsed.Background}, {"output_format", parsed.OutputFormat},
		{"moderation", parsed.Moderation}, {"input_fidelity", parsed.InputFidelity},
		{"style", parsed.Style},
	} {
		if value := strings.TrimSpace(field.value); value != "" {
			payload[field.key] = value
		}
	}
	if parsed.N > 1 {
		payload["n"] = parsed.N
	}
	if parsed.OutputCompression != nil {
		payload["output_compression"] = *parsed.OutputCompression
	}
	if parsed.PartialImages != nil {
		payload["partial_images"] = *parsed.PartialImages
	}
	if parsed.Stream {
		payload["stream"] = true
	}

	endpoint := "/images/generations"
	if parsed.IsEdits() {
		endpoint = "/images/edits"
		images := make([]map[string]string, 0, len(parsed.InputImageURLs)+len(parsed.Uploads))
		for _, imageURL := range parsed.InputImageURLs {
			if imageURL = strings.TrimSpace(imageURL); imageURL != "" {
				images = append(images, map[string]string{"image_url": imageURL})
			}
		}
		for _, upload := range parsed.Uploads {
			url, err := openAIImageUploadToDataURL(upload)
			if err != nil {
				return nil, "", err
			}
			images = append(images, map[string]string{"image_url": url})
		}
		if len(images) == 0 {
			return nil, "", fmt.Errorf("image input is required")
		}
		payload["images"] = images
		mask := strings.TrimSpace(parsed.MaskImageURL)
		if parsed.MaskUpload != nil {
			var err error
			mask, err = openAIImageUploadToDataURL(*parsed.MaskUpload)
			if err != nil {
				return nil, "", err
			}
		}
		if mask != "" {
			payload["mask"] = map[string]string{"image_url": mask}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("marshal Codex Images request: %w", err)
	}
	return body, strings.TrimSuffix(chatgptCodexURL, "/responses") + endpoint, nil
}

// 原生 JSON 响应与后台图片预览使用同一份校验，防止空 data 被计为成功。
func parseCodexDirectImagesResponse(body []byte) ([]openAIResponsesImageResult, error) {
	if !gjson.ValidBytes(body) {
		return nil, newOpenAIUpstreamStreamReadError(fmt.Errorf("invalid Images API JSON response"))
	}
	if upstreamErr := openAIImagesUpstreamErrorFromSSEPayload(body); upstreamErr != nil {
		return nil, upstreamErr
	}
	root := gjson.ParseBytes(body)
	if upstreamErr := openAIImagesUpstreamErrorFromGJSON(root.Get("error"), ""); upstreamErr != nil {
		return nil, upstreamErr
	}
	var results []openAIResponsesImageResult
	for _, item := range root.Get("data").Array() {
		result := strings.TrimSpace(item.Get("b64_json").String())
		if result == "" {
			continue
		}
		meta := func(key string) string {
			if value := item.Get(key).String(); value != "" {
				return value
			}
			return root.Get(key).String()
		}
		results = append(results, openAIResponsesImageResult{
			Result: result, RevisedPrompt: item.Get("revised_prompt").String(),
			OutputFormat: meta("output_format"), Size: meta("size"),
			Quality: meta("quality"), Background: meta("background"), Model: meta("model"),
		})
	}
	if len(results) == 0 {
		// 成功状态但没有完整图片无法确定生成结果，不把它伪装成可重放的上游拒绝。
		return nil, newOpenAIUpstreamStreamReadError(ErrOpenAIUpstreamStreamTruncated)
	}
	reconcileOpenAIResponsesImageResultSizes(results, nil)
	return results, nil
}

func codexDirectImageURL(body []byte, path, outputFormat string) []byte {
	if result := gjson.GetBytes(body, path+"b64_json").String(); result != "" {
		body, _ = sjson.SetBytes(body, path+"url", "data:"+openAIImageOutputMIMEType(outputFormat)+";base64,"+result)
		body, _ = sjson.DeleteBytes(body, path+"b64_json")
	}
	return body
}

func isOpenAIImagesMainModelError(status int, body []byte) bool {
	if !isOpenAICodexPlanGatedModelError(status, body) {
		return false
	}
	message := extractUpstreamErrorMessage(body)
	model := openAIImagesResponsesMainModel
	return strings.Contains(message, "'"+model+"'") ||
		strings.Contains(message, `"`+model+`"`)
}

// 原生生图 HTTP 200 后的传输中断无法证明未生成图片，禁止交给账号/跨组重放。
// 仍写入运维事件；已有部分图片继续由调用方结算，不因日志或交付失败重新生成。
func (s *OpenAIGatewayService) handleCodexDirectImagesResponseError(ctx context.Context, c *gin.Context, account *Account, model, upstreamURL string, resp *http.Response, writtenBefore int, err error) error {
	if code, message, ok := OpenAIUpstreamStreamReadErrorDetails(err); ok {
		upstreamErr := &OpenAIImagesUpstreamError{StatusCode: http.StatusBadGateway, ErrorType: "upstream_error", Code: code, Message: message}
		if resp != nil {
			upstreamErr.UpstreamRequestID = resp.Header.Get("x-request-id")
		}
		setOpsUpstreamError(c, upstreamErr.StatusCode, message, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			UpstreamStatusCode: upstreamErr.StatusCode, UpstreamRequestID: upstreamErr.UpstreamRequestID,
			UpstreamURL: safeUpstreamURL(upstreamURL), Kind: "http_error", Message: message,
		})
		if c.Writer.Written() && OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) >= 0 {
			if flusher, ok := c.Writer.(http.Flusher); ok {
				_ = s.writeOpenAIImagesStreamEvent(c, flusher, "error", buildOpenAIImagesStreamErrorBodyFromUpstream(upstreamErr))
			}
		} else {
			writeOpenAIImagesUpstreamErrorResponse(c, upstreamErr)
		}
		return upstreamErr
	}
	forwardErr := s.handleOpenAIImagesOAuthResponseError(ctx, c, account, model, safeUpstreamURL(upstreamURL), resp, writtenBefore, err)
	var failoverErr *UpstreamFailoverError
	if errors.As(forwardErr, &failoverErr) {
		return forwardErr
	}
	var upstreamErr *OpenAIImagesUpstreamError
	if errors.As(forwardErr, &upstreamErr) {
		// 原生 JSON 的 2xx 内带错误尚未写出；Handler 收到此类型即结束，必须在此补齐响应。
		// 已交付的 SSE 错误与 JSON keepalive 由共用写入器判定，避免重复终止事件。
		writeOpenAIImagesUpstreamErrorResponse(c, upstreamErr)
	}
	return forwardErr
}

// 克隆既有图片尺寸账单明细，添加缓存桶时不修改异步结算共享的结果快照。
func cloneImageSizeBreakdown(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]int, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// Images 端点只输出图片；未提供输出分类时，output_tokens 全部是图片 token。
// 缓存图片数量只采信明确明细，不根据总缓存量猜测图文占比。
func codexDirectImagesUsage(body []byte) (OpenAIUsage, bool) {
	value := gjson.GetBytes(body, "usage")
	usage, ok := openAIUsageFromGJSON(value)
	if !ok {
		return usage, false
	}
	if !value.Get("output_tokens_details.image_tokens").Exists() {
		usage.ImageOutputTokens = usage.OutputTokens
	}
	cached := value.Get("input_tokens_details.cached_tokens_details")
	if !value.Get("input_tokens_details.cached_tokens").Exists() && cached.IsObject() {
		imageTokens, _ := boundedJSONNonNegativeInt(cached.Get("image_tokens"))
		textTokens, _ := boundedJSONNonNegativeInt(cached.Get("text_tokens"))
		usage.CacheReadInputTokens = min(imageTokens, max(usage.InputTokens, 0))
		usage.CacheReadInputTokens += min(textTokens, max(usage.InputTokens-usage.CacheReadInputTokens, 0))
	}
	imageCached, _ := boundedJSONNonNegativeInt(cached.Get("image_tokens"))
	usage.ImageCacheReadTokens = min(imageCached, max(usage.ImageInputTokens, 0), max(usage.CacheReadInputTokens, 0))
	return usage, true
}

func (s *OpenAIGatewayService) handleCodexDirectImagesNonStreamingResponse(resp *http.Response, c *gin.Context, parsed *OpenAIImagesRequest) (OpenAIUsage, int, []string, error) {
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		if shouldClassifyOpenAIUpstreamStreamReadError(err) {
			err = newOpenAIUpstreamStreamReadError(err)
		}
		return OpenAIUsage{}, 0, nil, err
	}
	results, err := parseCodexDirectImagesResponse(body)
	if err != nil {
		return OpenAIUsage{}, 0, nil, err
	}
	usage, _ := codexDirectImagesUsage(body)
	if observer := upstreamResponseModelObserverFromContext(c); observer != nil {
		observer.Observe(gjson.GetBytes(body, "model").String(), true)
		for _, result := range results {
			observer.Observe(result.Model, true)
		}
	}
	clientModel := strings.TrimSpace(parsed.Model)
	if clientModel != "" {
		for i := range gjson.GetBytes(body, "data").Array() {
			body, _ = sjson.SetBytes(body, fmt.Sprintf("data.%d.model", i), clientModel)
		}
	}
	for i, item := range gjson.GetBytes(body, "data").Array() {
		if actualSize := detectOpenAIImageResultSize(item.Get("b64_json").String()); actualSize != "" {
			body, _ = sjson.SetBytes(body, fmt.Sprintf("data.%d.size", i), actualSize)
			if i == 0 {
				body, _ = sjson.SetBytes(body, "size", actualSize)
			}
		}
	}
	if parsed.ResponseFormat == "url" {
		for i, item := range gjson.GetBytes(body, "data").Array() {
			format := item.Get("output_format").String()
			if format == "" {
				format = gjson.GetBytes(body, "output_format").String()
			}
			if format == "" {
				format = parsed.OutputFormat
			}
			body = codexDirectImageURL(body, fmt.Sprintf("data.%d.", i), format)
		}
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	contentType := "application/json"
	if s.cfg != nil && !s.cfg.Security.ResponseHeaders.Enabled {
		if upstreamType := strings.TrimSpace(resp.Header.Get("Content-Type")); upstreamType != "" {
			contentType = upstreamType
		}
	}
	c.Data(resp.StatusCode, contentType, body)
	return usage, len(results), openAIResponsesImageResultSizes(results), nil
}
