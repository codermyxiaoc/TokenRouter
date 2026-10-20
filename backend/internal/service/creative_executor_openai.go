package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// executeOpenAI 执行 OpenAI 平台任务：generate 走 /v1/images/generations（JSON），
// edit/inpaint 走 /v1/images/edits（multipart，多源图 + mask）。
func (e *CreativeExecutor) executeOpenAI(ctx context.Context, run CreativeRun, payload CreativeRunPayload, account *Account, upstreamModel string) ([]CreativeOutput, error) {
	if e.gateway == nil {
		return nil, errors.New("creative openai gateway is not configured")
	}
	midjourney := IsMidjourneyImageGenerationModel(upstreamModel)
	// MJ 是第三方 Images 兼容模型，不能使用 OAuth 或误入 GPT Image 参数通道。
	if midjourney && (account == nil || account.Type != AccountTypeAPIKey) {
		return nil, creativeNonRetryableError("Midjourney requires an API key account")
	}
	endpoint := openAIImagesGenerationsEndpoint
	if run.Operation != CreativeOperationGenerate {
		endpoint = openAIImagesEditsEndpoint
	}
	body, contentType, err := buildCreativeOpenAIRequestBody(run, payload, upstreamModel)
	if err != nil {
		return nil, err
	}
	if account.IsOpenAIOAuthLike() {
		return e.executeOpenAIOAuth(ctx, run, account, upstreamModel, endpoint, contentType, body)
	}
	token, _, err := e.gateway.GetAccessToken(ctx, account)
	if err != nil {
		return nil, creativeHTTPStatusError(0, err.Error())
	}
	targetURL, err := e.creativeOpenAIURL(account, endpoint)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	authHeaders, err := e.gateway.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, creativeHTTPStatusError(0, err.Error())
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if strings.TrimSpace(contentType) != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// 账号级请求头覆写最后应用，配置值优先于内置默认头。
	account.ApplyHeaderOverrides(req.Header)

	resp, err := e.gateway.httpUpstream.DoWithTLS(req, accountProxyURL(account), account.ID, account.Concurrency, e.gateway.resolveOpenAITLSProfile(account))
	if err != nil {
		if midjourney {
			// POST 发送后断连不能证明未生图，必须保留预占，禁止重复生成。
			logCreativeImageUnconfirmed(ctx, run.RunID, account.ID, "transport", 0)
			return nil, creativeResultUnconfirmedError()
		}
		return nil, creativeHTTPStatusError(0, err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	var respBody []byte
	if midjourney {
		// MJ 一次返回四张图片，总包预算包含各张 base64 开销；其它模型维持原上限。
		respBody, err = readCreativeMidjourneyBody(resp.Body, creativeMidjourneyMaxResponseBytes)
	} else {
		respBody, err = readCreativeUpstreamBody(resp.Body, 64<<20)
	}
	if err != nil {
		if midjourney {
			logCreativeImageUnconfirmed(ctx, run.RunID, account.ID, "response_body", resp.StatusCode)
			return nil, creativeResultUnconfirmedError()
		}
		return nil, creativeHTTPStatusError(0, err.Error())
	}
	if midjourney && (resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500 || resp.StatusCode < 200 || resp.StatusCode >= 300 && resp.StatusCode < 400) {
		logCreativeImageUnconfirmed(ctx, run.RunID, account.ID, "http_status_unconfirmed", resp.StatusCode)
		return nil, creativeResultUnconfirmedError()
	}
	if resp.StatusCode >= 400 {
		// 只记状态与固定阶段，区分真实 HTTP 拒绝和成功响应后的解析失败，不落原包或供应商消息。
		logger.L().Warn("creative.openai_image_failed", zap.String("run_id", run.RunID), zap.Int64("account_id", account.ID), zap.String("stage", "http_status"), zap.Int("upstream_status", resp.StatusCode))
		return nil, creativeHTTPResponseError(resp.StatusCode, respBody)
	}
	if midjourney {
		outputs, parseErr := parseCreativeMidjourneyImageOutputs(ctx, respBody, creativeMidjourneyImageDownloader)
		if parseErr != nil {
			logCreativeImageUnconfirmed(ctx, run.RunID, account.ID, "response_parse", resp.StatusCode)
		}
		return outputs, parseErr
	}
	outputs, err := parseCreativeOpenAIImageOutputs(respBody)
	if err != nil {
		// 对外继续沿用原安全错误；后台保留真实 HTTP 状态，不能将本地解码失败误诊为上游502。
		logger.L().Warn("creative.openai_image_failed", zap.String("run_id", run.RunID), zap.Int64("account_id", account.ID), zap.String("stage", "response_parse"), zap.Int("upstream_status", resp.StatusCode))
	}
	return outputs, err
}

// 未知结果不会抛出 worker 错误；独立记录固定阶段，供管理员用任务编号核对，
// 不保存上游错误、响应体、素材地址或用户提示词。
func logCreativeImageUnconfirmed(ctx context.Context, runID string, accountID int64, stage string, status int) {
	logger.L().Warn("creative.openai_image_result_unconfirmed",
		zap.String("run_id", runID), zap.Int64("account_id", accountID),
		zap.String("stage", stage), zap.Int("upstream_status", status),
		zap.Bool("execution_timed_out", errors.Is(ctx.Err(), context.DeadlineExceeded)),
	)
}

// creativeOpenAIURL 推导 OpenAI images 上游 URL：账号自定义 base_url 优先，否则官方端点。
func (e *CreativeExecutor) creativeOpenAIURL(account *Account, endpoint string) (string, error) {
	targetURL := openAIImagesGenerationsURL
	if endpoint == openAIImagesEditsEndpoint {
		targetURL = openAIImagesEditsURL
	}
	baseURL := account.GetOpenAIBaseURL()
	if baseURL == "" {
		return targetURL, nil
	}
	validatedURL, err := e.gateway.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return "", creativeNonRetryableError("creative openai base url invalid: %s", err.Error())
	}
	return buildOpenAIImagesURL(validatedURL, endpoint), nil
}

// buildCreativeOpenAIRequestBody 构造 OpenAI images 请求体：
// generate 为 JSON；edit/inpaint 为 multipart（image 多文件、mask、model、prompt）。
// 输出格式固定为 PNG；其余可选参数已经过模型能力校验，生成与编辑请求保持同一语义。
func buildCreativeOpenAIRequestBody(run CreativeRun, payload CreativeRunPayload, upstreamModel string) ([]byte, string, error) {
	if IsMidjourneyImageGenerationModel(upstreamModel) {
		return buildCreativeMidjourneyRequestBody(run, payload, upstreamModel)
	}
	if run.Operation == CreativeOperationGenerate {
		bodyMap := map[string]any{
			"model":         upstreamModel,
			"prompt":        payload.Prompt,
			"n":             1,
			"output_format": "png",
			"size":          creativeOpenAIImageSize(run.ImageSize, run.AspectRatio),
		}
		if creativeOpenAIUsesResponseFormat(upstreamModel) {
			bodyMap["response_format"] = "b64_json"
		}
		if quality := strings.TrimSpace(payload.Quality); quality != "" {
			bodyMap["quality"] = quality
		}
		if background := strings.TrimSpace(payload.Background); background != "" {
			bodyMap["background"] = background
		}
		body, err := json.Marshal(bodyMap)
		if err != nil {
			return nil, "", err
		}
		return body, "application/json", nil
	}

	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for i, image := range payload.Sources {
		if err := writeCreativeOpenAIImagePart(writer, "image", fmt.Sprintf("source_%d.%s", i, creativeFileExtension(image.Mime)), image); err != nil {
			return nil, "", err
		}
	}
	if payload.Mask != nil {
		if err := writeCreativeOpenAIImagePart(writer, "mask", "mask."+creativeFileExtension(payload.Mask.Mime), *payload.Mask); err != nil {
			return nil, "", err
		}
	}
	if err := writer.WriteField("model", upstreamModel); err != nil {
		return nil, "", err
	}
	if err := writer.WriteField("prompt", payload.Prompt); err != nil {
		return nil, "", err
	}
	if creativeOpenAIUsesResponseFormat(upstreamModel) {
		if err := writer.WriteField("response_format", "b64_json"); err != nil {
			return nil, "", err
		}
	}
	if err := writer.WriteField("output_format", "png"); err != nil {
		return nil, "", err
	}
	if err := writer.WriteField("size", creativeOpenAIImageSize(run.ImageSize, run.AspectRatio)); err != nil {
		return nil, "", err
	}
	if err := writer.WriteField("n", "1"); err != nil {
		return nil, "", err
	}
	if quality := strings.TrimSpace(payload.Quality); quality != "" {
		if err := writer.WriteField("quality", quality); err != nil {
			return nil, "", err
		}
	}
	if background := strings.TrimSpace(payload.Background); background != "" {
		if err := writer.WriteField("background", background); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

// 图片与蒙版必须保留已校验的媒体类型；CreateFormFile 默认的 octet-stream 会被严格图片端点拒绝。
func writeCreativeOpenAIImagePart(writer *multipart.Writer, field, filename string, image CreativeInputImage) error {
	mediaType := strings.ToLower(strings.TrimSpace(image.Mime))
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return creativeNonRetryableError("unsupported creative image MIME type")
	}
	header := make(textproto.MIMEHeader)
	// 字段和文件名由调用方固定生成，保留原 multipart 带引号的序列化以兼容代理解析器。
	header.Set("Content-Disposition", fmt.Sprintf("form-data; name=%q; filename=%q", field, filename))
	header.Set("Content-Type", mediaType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(image.Bytes)
	return err
}

// creativeOpenAIUsesResponseFormat 仅对 DALL-E 保留旧版 response_format 参数；GPT Image 固定返回 base64。
func creativeOpenAIUsesResponseFormat(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "dall-e")
}

// parseCreativeOpenAIImageOutputs 解析 OpenAI images 响应（grok 同结构）的 data[].b64_json。
func parseCreativeOpenAIImageOutputs(body []byte) ([]CreativeOutput, error) {
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() || len(data.Array()) == 0 {
		return nil, creativeHTTPStatusError(http.StatusBadGateway, "upstream returned no image output")
	}
	outputs := make([]CreativeOutput, 0, len(data.Array()))
	for _, item := range data.Array() {
		b64 := strings.TrimSpace(item.Get("b64_json").String())
		if b64 == "" {
			continue
		}
		decoded, err := decodeBase64Image(b64)
		if err != nil || len(decoded.Bytes) == 0 {
			continue
		}
		outputs = append(outputs, CreativeOutput{Index: len(outputs), Bytes: decoded.Bytes, Mime: decoded.Mime})
		break
	}
	if len(outputs) == 0 {
		return nil, creativeHTTPStatusError(http.StatusBadGateway, "upstream returned no decodable image output")
	}
	return outputs, nil
}
