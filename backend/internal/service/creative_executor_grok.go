package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// executeGrok 执行 Grok 平台任务：generate 与 edit 分别使用 xAI 图片端点。
func (e *CreativeExecutor) executeGrok(ctx context.Context, run CreativeRun, payload CreativeRunPayload, account *Account, upstreamModel string) ([]CreativeOutput, error) {
	if run.Operation != CreativeOperationGenerate && run.Operation != CreativeOperationEdit {
		return nil, creativeNonRetryableError("grok platform does not support creative operation %s", run.Operation)
	}
	if run.Operation == CreativeOperationEdit {
		if len(payload.Sources) == 0 {
			return nil, creativeNonRetryableError("grok image edit requires at least one source image")
		}
		if len(payload.Sources) > grokMediaMaxEditSourceImages {
			return nil, creativeNonRetryableError("grok image edit supports at most %d source images", grokMediaMaxEditSourceImages)
		}
	}
	if e.gateway == nil {
		return nil, errors.New("creative grok gateway is not configured")
	}
	endpoint := GrokMediaEndpointImagesGenerations
	if run.Operation == CreativeOperationEdit {
		endpoint = GrokMediaEndpointImagesEdits
	}
	targetURL, err := buildGrokMediaURL(account, e.cfg, endpoint, "")
	if err != nil {
		return nil, creativeHTTPStatusError(0, err.Error())
	}
	token, _, err := e.gateway.GetAccessToken(ctx, account)
	if err != nil {
		return nil, creativeHTTPStatusError(0, err.Error())
	}
	request := buildCreativeGrokRequest(run, payload, upstreamModel)
	if run.Operation == CreativeOperationEdit {
		request = buildCreativeGrokEditRequest(run, payload, upstreamModel)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileGrok))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if account.IsGrokOAuth() && isGrokCLIProxyTarget(targetURL) {
		applyGrokCLIHeaders(req.Header)
	}
	// 账号级请求头覆写最后应用，配置值优先于内置默认头。
	account.ApplyHeaderOverrides(req.Header)

	resp, err := e.gateway.httpUpstream.Do(req, accountProxyURL(account), account.ID, account.Concurrency)
	if err != nil {
		return nil, creativeHTTPStatusError(0, err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := readCreativeUpstreamBody(resp.Body, 64<<20)
	if err != nil {
		return nil, creativeHTTPStatusError(0, err.Error())
	}
	if resp.StatusCode >= 400 {
		return nil, creativeHTTPStatusError(resp.StatusCode, extractUpstreamErrorMessage(respBody))
	}
	return parseCreativeOpenAIImageOutputs(respBody)
}

// buildCreativeGrokRequest 构造 xAI images/generations 请求体。
func buildCreativeGrokRequest(run CreativeRun, payload CreativeRunPayload, upstreamModel string) map[string]any {
	request := map[string]any{
		"model":           upstreamModel,
		"prompt":          payload.Prompt,
		"n":               1,
		"response_format": "b64_json",
		"resolution":      creativeGrokImageResolution(run.ImageSize),
	}
	if aspectRatio := creativeGrokAspectRatio(run.AspectRatio); aspectRatio != "" {
		request["aspect_ratio"] = aspectRatio
	}
	if quality := strings.TrimSpace(payload.Quality); quality != "" {
		request["quality"] = quality
	}
	return request
}

// buildCreativeGrokEditRequest 构造 xAI images/edits 所需的 JSON 请求体。
// xAI 编辑端点不接受 OpenAI SDK 的 multipart 格式，图片必须作为 data URI 引用。
func buildCreativeGrokEditRequest(run CreativeRun, payload CreativeRunPayload, upstreamModel string) map[string]any {
	request := map[string]any{
		"model":           upstreamModel,
		"prompt":          payload.Prompt,
		"n":               1,
		"response_format": "b64_json",
		"resolution":      creativeGrokImageResolution(run.ImageSize),
	}
	if aspectRatio := creativeGrokAspectRatio(run.AspectRatio); aspectRatio != "" {
		request["aspect_ratio"] = aspectRatio
	}
	if quality := strings.TrimSpace(payload.Quality); quality != "" {
		request["quality"] = quality
	}

	images := make([]map[string]string, 0, len(payload.Sources))
	for _, source := range payload.Sources {
		mime := source.Mime
		if mime == "" {
			mime = "image/png"
		}
		images = append(images, map[string]string{
			"type": "image_url",
			"url":  "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(source.Bytes),
		})
	}
	if len(images) == 1 {
		request["image"] = images[0]
	} else {
		request["images"] = images
	}
	return request
}
