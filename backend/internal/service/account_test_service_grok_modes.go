package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	_ "golang.org/x/image/webp"
)

const (
	maxGrokTestMediaBytes = 6 << 20
	maxGrokTestImageBytes = 4 << 20
	maxGrokTestVideoBytes = 64 << 20
)

// testGrokAccountConnectionWithOptions 只扩展管理员主动测试，不接入用户调度或扣费链路。
// @project-doc docs/interfaces/grok_upstream.md#grok_account_tests
func (s *AccountTestService) testGrokAccountConnectionWithOptions(c *gin.Context, account *Account, modelID, prompt, testType string, opts AccountTestOptions) error {
	if account == nil || account.Platform != PlatformGrok {
		return s.sendErrorAndEnd(c, "Grok account is required")
	}
	if testType == "" {
		return s.testGrokAccountConnection(c, account, modelID, prompt)
	}
	if testType == "text" {
		return s.testGrokAccountConnection(c, account, modelID, prompt, AccountTestTypeText)
	}
	switch testType {
	case "image", "video", "search", "tts", "stt", "realtime":
	default:
		return s.sendErrorAndEnd(c, "Unsupported Grok test type")
	}
	if testType != "realtime" && s.httpUpstream == nil {
		return s.sendErrorAndEnd(c, "HTTP upstream not configured")
	}
	// 单次诊断限制总时长，取消浏览器测试后同时取消上游和视频轮询。
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	token, err := s.grokTestAccessToken(ctx, account)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	s.prepareGrokTestSSE(c)
	switch testType {
	case "image":
		model := grokTestMappedModel(account, modelID, "grok-imagine-image")
		model = NormalizeGrokMediaModelForEndpoint(GrokMediaEndpointImagesGenerations, model, false)
		return s.testGrokImageWithSource(c, ctx, account, token, model, firstNonEmpty(strings.TrimSpace(prompt), defaultGrokImageTestPrompt), opts.ImageDataURL)
	case "video":
		model := grokTestMappedModel(account, modelID, "grok-imagine-video")
		model = NormalizeGrokMediaModelForEndpoint(GrokMediaEndpointVideosGenerations, model, false)
		return s.testGrokVideoGeneration(c, ctx, account, token, model, firstNonEmpty(strings.TrimSpace(prompt), "A red ball bouncing once on a white floor, short simple motion."), opts.ImageDataURL)
	case "search":
		return s.testGrokWebSearch(c, ctx, account, token, prompt)
	case "tts":
		return s.testGrokTTS(c, ctx, account, token, prompt)
	case "stt":
		return s.testGrokSTT(c, ctx, account, token, opts.AudioDataURL)
	default:
		return s.testGrokRealtime(c, ctx, account, token, grokTestMappedModel(account, modelID, "grok-voice-latest"))
	}
}

// grokTestMappedModel 与账号已有模型映射保持一致，媒体默认值不继承文本模型。
func grokTestMappedModel(account *Account, modelID, fallback string) string {
	model := firstNonEmpty(strings.TrimSpace(modelID), fallback)
	if mapped := strings.TrimSpace(account.GetMappedModel(model)); mapped != "" {
		model = mapped
	}
	return model
}

func (s *AccountTestService) grokTestAccessToken(ctx context.Context, account *Account) (string, error) {
	switch account.Type {
	case AccountTypeOAuth:
		if s.grokTokenProvider == nil {
			return "", errors.New("Grok token provider not configured")
		}
		// 冷却与暂停不阻止管理员诊断，但仍使用正式的凭据刷新逻辑。
		return s.grokTokenProvider.GetAccessTokenForManualTest(ctx, account)
	case AccountTypeAPIKey:
		if token := strings.TrimSpace(account.GetCredential("api_key")); token != "" {
			return token, nil
		}
		return "", errors.New("Grok API key is missing")
	default:
		return "", fmt.Errorf("Unsupported Grok account type: %s", account.Type)
	}
}

func (s *AccountTestService) grokTestProxyURL(account *Account) string {
	if account.ProxyID != nil && account.Proxy != nil {
		return account.Proxy.URL()
	}
	return ""
}

func (s *AccountTestService) prepareGrokTestSSE(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.Flush()
}

func (s *AccountTestService) applyGrokTestRequestHeaders(req *http.Request, account *Account, token, accept string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+token)
	// 官方媒体/语音主机不能混入 CLI 代理专用身份头。
	if account.IsGrokOAuth() && isGrokCLIProxyTarget(req.URL.String()) {
		applyGrokCLIHeaders(req.Header)
	}
	applyAccountTestUserAgent(req)
	account.ApplyHeaderOverrides(req.Header)
}

// grokTestRequest 复用正式出站传输、账号代理和 TLS 指纹；所有响应都先有界读取。
func (s *AccountTestService) grokTestRequest(ctx context.Context, account *Account, token, method, target, contentType string, body []byte, limit int64) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileGrok), method, target, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	s.applyGrokTestRequestHeaders(req, account, token, "application/json, audio/*, video/*")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := s.httpUpstream.DoWithTLS(req, s.grokTestProxyURL(account), account.ID, account.Concurrency, s.resolveTLSProfile(account))
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if readErr != nil {
		return nil, resp.Header, readErr
	}
	if int64(len(data)) > limit {
		return nil, resp.Header, fmt.Errorf("Grok test response exceeds %d byte preview limit", limit)
	}
	s.observeGrokModeTestResponse(ctx, account, resp, data)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Header, fmt.Errorf("Grok API returned %d: %s", resp.StatusCode, truncateString(string(data), 1000))
	}
	// HTTP 200 携带错误对象也不应误报连接成功。
	if json.Valid(data) && gjson.GetBytes(data, "error").Exists() && gjson.GetBytes(data, "error").Type != gjson.Null {
		return nil, resp.Header, fmt.Errorf("Grok API error: %s", truncateString(gjson.GetBytes(data, "error").Raw, 1000))
	}
	if status := gjson.GetBytes(data, "status").String(); status == "failed" || status == "error" || status == "cancelled" || status == "canceled" || status == "incomplete" {
		return nil, resp.Header, fmt.Errorf("Grok API returned unsuccessful status %s", status)
	}
	return data, resp.Header, nil
}

// observeGrokModeTestResponse 保留原有主动测试的额度恢复与计费冷却规则。
func (s *AccountTestService) observeGrokModeTestResponse(ctx context.Context, account *Account, resp *http.Response, body []byte) {
	if s.accountRepo == nil {
		return
	}
	now := time.Now()
	snapshot := parseGrokQuotaSnapshot(resp.Header, resp.StatusCode, now)
	stampGrokQuotaSnapshotForPlan(account, snapshot, grokRequestedModelFromCtx(ctx))
	if snapshot != nil {
		resetAt, limited := grokRateLimitResetAtForAccount(account, snapshot, now)
		if limited {
			normalizeGrokExhaustedWindowResets(snapshot, resetAt, now)
		}
		_ = s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{grokQuotaSnapshotExtraKey: snapshot})
		if limited {
			persistGrokRateLimit(ctx, s.accountRepo, account, resetAt)
		} else if isSuccessfulGrokRateLimitRecovery(account, snapshot) {
			clearGrokRateLimitAfterRecovery(ctx, s.accountRepo, account)
		}
	} else if isSuccessfulGrokRateLimitRecovery(account, &xai.QuotaSnapshot{StatusCode: resp.StatusCode}) {
		clearGrokRateLimitAfterRecovery(ctx, s.accountRepo, account)
	}
	if resp.StatusCode < 400 || isGrokContentPolicyRejection(resp.StatusCode, body) {
		return
	}
	decision := classifyGrokUpstreamFailure(resp.StatusCode, body, "")
	switch {
	case decision.Class == GrokFailureFreeUsage:
		if resetAt, limited := grokRateLimitResetAtForAccount(account, snapshot, now); limited && resetAt.After(now) {
			persistGrokRateLimit(ctx, s.accountRepo, account, resetAt)
		} else {
			stateCtx, cancel := openAIAccountStateContext(ctx)
			defer cancel()
			_ = s.accountRepo.SetTempUnschedulable(stateCtx, account.ID, now.Add(grokFreeUsageProbeCooldown), "grok free usage exhausted")
		}
	case decision.Class == GrokFailureBilling && (isGrokSpendingLimitError(body) || strings.Contains(strings.ToLower(decision.Reason), "credit")):
		persistGrokRateLimit(ctx, s.accountRepo, account, grokSpendingLimitResetAt(account, now))
	case resp.StatusCode == http.StatusPaymentRequired:
		stateCtx, cancel := openAIAccountStateContext(ctx)
		defer cancel()
		_ = s.accountRepo.SetTempUnschedulable(stateCtx, account.ID, now.Add(30*time.Minute), "grok payment required")
	}
}

func (s *AccountTestService) testGrokImageWithSource(c *gin.Context, ctx context.Context, account *Account, token, model, prompt, source string) error {
	endpoint := GrokMediaEndpointImagesGenerations
	payload := map[string]any{"model": model, "prompt": prompt, "n": 1, "response_format": "b64_json"}
	if strings.TrimSpace(source) != "" {
		imageURL, err := normalizeGrokTestImage(source)
		if err != nil {
			return s.sendErrorAndEnd(c, err.Error())
		}
		endpoint = GrokMediaEndpointImagesEdits
		payload["image"] = grokMediaImageObject(imageURL)
	}
	target, err := buildGrokMediaURL(account, s.cfg, endpoint, "")
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	s.sendEvent(c, TestEvent{Type: "test_start", Model: model})
	s.sendEvent(c, TestEvent{Type: "status", Text: "正在测试 Grok 图片端点"})
	encoded, _ := json.Marshal(payload)
	data, _, err := s.grokTestRequest(ctx, account, token, http.MethodPost, target, "application/json", encoded, 32<<20)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	count := 0
	for _, item := range gjson.GetBytes(data, "data").Array() {
		imageURL := strings.TrimSpace(item.Get("b64_json").String())
		mimeType := strings.TrimSpace(item.Get("mime_type").String())
		if !strings.HasPrefix(mimeType, "image/") {
			mimeType = "image/png"
		}
		if imageURL != "" {
			imageURL = "data:" + mimeType + ";base64," + imageURL
		} else {
			imageURL = safeGrokTestPreviewURL(item.Get("url").String())
		}
		if imageURL != "" {
			count++
			s.sendEvent(c, TestEvent{Type: "image", ImageURL: imageURL, MimeType: mimeType})
		}
	}
	if count == 0 {
		return s.sendErrorAndEnd(c, "No images returned from Grok API")
	}
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

// testGrokVideoGeneration 只创建一次视频任务，后续轮询固定在原账号且不会重新提交。
func (s *AccountTestService) testGrokVideoGeneration(c *gin.Context, ctx context.Context, account *Account, token, model, prompt, source string) error {
	target, err := buildGrokMediaURL(account, s.cfg, GrokMediaEndpointVideosGenerations, "")
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	payload := map[string]any{"model": model, "prompt": prompt, "duration": 6, "aspect_ratio": "16:9", "resolution": "480p"}
	if strings.TrimSpace(source) != "" {
		imageURL, imageErr := normalizeGrokTestImage(source)
		if imageErr != nil {
			return s.sendErrorAndEnd(c, imageErr.Error())
		}
		payload["image"] = grokMediaImageObject(imageURL)
	}
	s.sendEvent(c, TestEvent{Type: "test_start", Model: model})
	s.sendEvent(c, TestEvent{Type: "status", Text: "正在通过 /v1/videos/generations 创建测试视频"})
	encoded, _ := json.Marshal(payload)
	data, _, err := s.grokTestRequest(ctx, account, token, http.MethodPost, target, "application/json", encoded, 2<<20)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	requestID := extractGrokMediaVideoRequestID(data)
	if requestID == "" {
		return s.sendErrorAndEnd(c, "Grok video response missing request_id")
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: "视频任务已创建：" + requestID + "\n"})
	statusURL, err := buildGrokMediaURL(account, s.cfg, GrokMediaEndpointVideoStatus, requestID)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	pollCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for {
		data, _, err = s.grokTestRequest(pollCtx, account, token, http.MethodGet, statusURL, "application/json", nil, 2<<20)
		if err != nil {
			return s.sendErrorAndEnd(c, fmt.Sprintf("Grok video status failed (request_id=%s): %s", requestID, err))
		}
		status := strings.ToLower(strings.TrimSpace(gjson.GetBytes(data, "status").String()))
		s.sendEvent(c, TestEvent{Type: "status", Text: "视频任务状态：" + status})
		switch status {
		case "done", "completed", "succeeded", "success":
			return s.emitGrokVideoResult(c, pollCtx, account, token, requestID, data)
		case "failed", "error", "canceled", "cancelled":
			return s.sendErrorAndEnd(c, "Grok video failed: "+truncateString(string(data), 1000))
		}
		select {
		case <-pollCtx.Done():
			return s.sendErrorAndEnd(c, "Grok video poll timed out or canceled; task may still be processing (request_id="+requestID+")")
		case <-time.After(3 * time.Second):
		}
	}
}

func (s *AccountTestService) emitGrokVideoResult(c *gin.Context, ctx context.Context, account *Account, token, requestID string, data []byte) error {
	videoURL := safeGrokTestPreviewURL(firstNonEmpty(gjson.GetBytes(data, "video.url").String(), gjson.GetBytes(data, "url").String(), gjson.GetBytes(data, "video_url").String()))
	if videoURL == "" {
		// 只向经过安全校验的账号 content 端点发凭据，不携带凭据请求任意返回 URL。
		contentURL, err := buildGrokMediaURL(account, s.cfg, GrokMediaEndpointVideoContent, requestID)
		if err != nil {
			return s.sendErrorAndEnd(c, err.Error())
		}
		body, header, err := s.grokTestRequest(ctx, account, token, http.MethodGet, contentURL, "", nil, maxGrokTestVideoBytes)
		if err != nil {
			return s.sendErrorAndEnd(c, err.Error())
		}
		mediaType, _, _ := mime.ParseMediaType(header.Get("Content-Type"))
		if len(body) == 0 || (!strings.HasPrefix(mediaType, "video/") && mediaType != "application/octet-stream") {
			return s.sendErrorAndEnd(c, "Grok video content is empty or not a video")
		}
		videoURL = "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(body)
	}
	s.sendEvent(c, TestEvent{Type: "video", VideoURL: videoURL, MimeType: "video/mp4"})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *AccountTestService) testGrokWebSearch(c *gin.Context, ctx context.Context, account *Account, token, query string) error {
	query = firstNonEmpty(strings.TrimSpace(query), "xAI Grok")
	model := normalizeOpenAIModelForUpstream(account, grokDefaultResponsesModel)
	target, err := buildGrokResponsesURL(account, s.cfg, s.settingService)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	s.sendEvent(c, TestEvent{Type: "test_start", Model: "grok-web-search"})
	s.sendEvent(c, TestEvent{Type: "status", Text: "正在测试 /v1/web_search 对应的 Responses 原生搜索工具"})
	payload, _ := json.Marshal(map[string]any{"model": model, "input": query, "tools": []map[string]any{{"type": "web_search"}}, "include": []string{"web_search_call.action.sources"}, "store": false, "stream": false})
	data, _, err := s.grokTestRequest(withGrokTeamRateLimitModel(ctx, model), account, token, http.MethodPost, target, "application/json", payload, 2<<20)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	if countGrokNativeSearchCallsFromJSONBytes(data) == 0 {
		return s.sendErrorAndEnd(c, "Grok web_search returned no search tool calls")
	}
	for _, item := range gjson.GetBytes(data, "output").Array() {
		if item.Get("type").String() == "web_search_call" && item.Get("status").String() == "failed" {
			return s.sendErrorAndEnd(c, "Grok web_search tool call failed")
		}
		for _, part := range item.Get("content").Array() {
			if text := part.Get("text").String(); text != "" {
				s.sendEvent(c, TestEvent{Type: "content", Text: text})
			}
		}
	}
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *AccountTestService) testGrokTTS(c *gin.Context, ctx context.Context, account *Account, token, text string) error {
	target, err := buildGrokVoiceURL(account, s.cfg, "tts")
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	s.sendEvent(c, TestEvent{Type: "test_start", Model: "grok-voice-tts"})
	payload, _ := json.Marshal(map[string]any{"text": firstNonEmpty(strings.TrimSpace(text), "Hello from TokenRouter account connectivity test."), "language": "en", "voice_id": "Ara"})
	// 不自动重试生成操作，避免超时后再次生成造成上游重复收费。
	data, header, err := s.grokTestRequest(ctx, account, token, http.MethodPost, target, "application/json", payload, maxGrokTestMediaBytes)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	mediaType, _, _ := mime.ParseMediaType(header.Get("Content-Type"))
	if mediaType == "application/octet-stream" {
		mediaType = "audio/mpeg"
	}
	if len(data) == 0 || !strings.HasPrefix(mediaType, "audio/") {
		return s.sendErrorAndEnd(c, "Grok TTS returned empty or non-audio content")
	}
	s.sendEvent(c, TestEvent{Type: "audio", AudioURL: "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), MimeType: mediaType})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *AccountTestService) testGrokSTT(c *gin.Context, ctx context.Context, account *Account, token, audioDataURL string) error {
	data, filename := minimalGrokTestWAV(), "probe.wav"
	if strings.TrimSpace(audioDataURL) != "" {
		audio, mediaType, err := decodeGrokTestDataURL(audioDataURL, "audio/", maxGrokTestMediaBytes)
		if err != nil {
			return s.sendErrorAndEnd(c, err.Error())
		}
		if !validGrokTestAudio(audio, mediaType) {
			return s.sendErrorAndEnd(c, "Audio content does not match a supported MP3/WAV/OGG/WebM/MP4 recording")
		}
		data = audio
		extensions := map[string]string{"audio/mpeg": ".mp3", "audio/mp3": ".mp3", "audio/wav": ".wav", "audio/x-wav": ".wav", "audio/webm": ".webm", "audio/ogg": ".ogg", "audio/mp4": ".m4a", "audio/x-m4a": ".m4a"}
		filename = "upload" + firstNonEmpty(extensions[mediaType], ".bin")
	}
	target, err := buildGrokVoiceURL(account, s.cfg, "stt")
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	s.sendEvent(c, TestEvent{Type: "test_start", Model: "grok-voice-stt"})
	if strings.TrimSpace(audioDataURL) == "" {
		s.sendEvent(c, TestEvent{Type: "status", Text: "未上传音频，使用短静音 WAV 验证 STT 端点连通性"})
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	_, _ = part.Write(data)
	_ = writer.WriteField("model", "grok-stt")
	_ = writer.WriteField("language", "en")
	_ = writer.Close()
	response, _, err := s.grokTestRequest(ctx, account, token, http.MethodPost, target, writer.FormDataContentType(), body.Bytes(), 2<<20)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	// 静音的合法转录可以为空，但 HTML 页面或任意 200 响应不算成功。
	if !json.Valid(response) || gjson.GetBytes(response, "text").Type != gjson.String {
		return s.sendErrorAndEnd(c, "Grok STT returned no transcription result")
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: "STT 转录：" + gjson.GetBytes(response, "text").String()})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

// testGrokRealtime 只验证握手和首个事件，不发送音频或创建模型响应。
func (s *AccountTestService) testGrokRealtime(c *gin.Context, ctx context.Context, account *Account, token, model string) error {
	base, err := buildGrokVoiceURL(account, s.cfg, "realtime")
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	target, err := url.Parse(base)
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid Grok Realtime URL")
	}
	if target.Scheme == "https" {
		target.Scheme = "wss"
	} else {
		target.Scheme = "ws"
	}
	query := target.Query()
	query.Set("model", model)
	target.RawQuery = query.Encode()
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	if account.IsGrokOAuth() && isGrokCLIProxyTarget(target.String()) {
		applyGrokCLIHeaders(headers)
	}
	account.ApplyHeaderOverrides(headers)
	dialer := newDefaultOpenAIWSClientDialer()
	if s.openAIGatewayService != nil {
		dialer = s.openAIGatewayService.getOpenAIWSPassthroughDialer()
	}
	s.sendEvent(c, TestEvent{Type: "test_start", Model: model})
	s.sendEvent(c, TestEvent{Type: "status", Text: "正在测试 /v1/realtime WebSocket 握手（不发送音频）"})
	dialCtx, cancel := context.WithTimeout(ctx, DefaultGrokRealtimeDialTimeout)
	defer cancel()
	conn, status, _, err := dialer.Dial(dialCtx, target.String(), headers, s.grokTestProxyURL(account), s.resolveTLSProfile(account))
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Grok Realtime handshake failed (HTTP %d): %s", status, err))
	}
	defer func() { _ = conn.Close() }()
	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()
	if data, readErr := conn.ReadMessage(readCtx); readErr == nil {
		eventType := gjson.GetBytes(data, "type").String()
		if eventType == "error" || gjson.GetBytes(data, "error").Exists() {
			return s.sendErrorAndEnd(c, "Grok Realtime returned an error event: "+truncateString(string(data), 500))
		}
		s.sendEvent(c, TestEvent{Type: "content", Text: "Realtime 握手成功，首个事件：" + eventType})
	} else if ctx.Err() != nil {
		return s.sendErrorAndEnd(c, "Grok Realtime test canceled")
	} else if errors.Is(readErr, context.DeadlineExceeded) || errors.Is(readCtx.Err(), context.DeadlineExceeded) {
		s.sendEvent(c, TestEvent{Type: "content", Text: "Realtime 握手成功；3 秒内未收到首个事件，仅确认连接可达"})
	} else {
		return s.sendErrorAndEnd(c, "Grok Realtime closed before a server event: "+readErr.Error())
	}
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

// decodeGrokTestDataURL 在解码前限制输入大小，不接收远程 URL，避免探针变成下载代理。
func decodeGrokTestDataURL(raw, prefix string, limit int) ([]byte, string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > base64.StdEncoding.EncodedLen(limit)+128 || !strings.HasPrefix(raw, "data:") {
		return nil, "", errors.New("Media must be a size-bounded base64 data URL")
	}
	meta, encoded, ok := strings.Cut(strings.TrimPrefix(raw, "data:"), ",")
	mediaType, suffix, hasEncoding := strings.Cut(meta, ";")
	mediaType = strings.ToLower(mediaType)
	if !ok || !hasEncoding || suffix != "base64" || !strings.HasPrefix(mediaType, prefix) {
		return nil, "", fmt.Errorf("Expected %s base64 data URL", prefix)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > limit {
		return nil, "", fmt.Errorf("Invalid media data or media exceeds %d bytes", limit)
	}
	return data, mediaType, nil
}

func normalizeGrokTestImage(raw string) (string, error) {
	data, _, err := decodeGrokTestDataURL(raw, "image/", maxGrokTestImageBytes)
	if err != nil {
		return "", err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 8 || config.Height < 8 {
		return "", errors.New("Source image must be a valid PNG/JPEG/GIF/WebP with dimensions of at least 8x8")
	}
	if format == "jpg" {
		format = "jpeg"
	}
	return "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// validGrokTestAudio 检查容器签名，不能仅凭客户端声明把脚本或任意文件送入语音端点。
func validGrokTestAudio(data []byte, mediaType string) bool {
	switch mediaType {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE"
	case "audio/mpeg", "audio/mp3":
		return len(data) >= 3 && (string(data[:3]) == "ID3" || (data[0] == 0xff && data[1]&0xe0 == 0xe0))
	case "audio/ogg", "audio/opus":
		return len(data) >= 4 && string(data[:4]) == "OggS"
	case "audio/webm":
		return len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3})
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return len(data) >= 12 && string(data[4:8]) == "ftyp"
	default:
		return false
	}
}

func safeGrokTestPreviewURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	return parsed.String()
}

// minimalGrokTestWAV 生成 0.05 秒单声道静音，作为未上传录音时的明确连通性探针。
func minimalGrokTestWAV() []byte {
	const dataSize = 800
	data := make([]byte, 44+dataSize)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], 36+dataSize)
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 8000)
	binary.LittleEndian.PutUint32(data[28:], 16000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], dataSize)
	return data
}
