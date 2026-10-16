package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const videoAccountTestTimeout = 10 * time.Minute

var videoAccountTestResolution = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
var videoAccountTestRatio = regexp.MustCompile(`^(adaptive|[1-9][0-9]{0,2}:[1-9][0-9]{0,2})$`)

// VideoAccountTestEndpoints 返回最终上游模型真正允许的端点，保持账号配置顺序供测试选择框使用。
func VideoAccountTestEndpoints(account *Account, requestedModel string) []VideoEndpoint {
	out := make([]VideoEndpoint, 0)
	if account == nil || !account.IsModelSupported(requestedModel) {
		return out
	}
	cfg, err := account.VideoConfiguration()
	if err != nil {
		return out
	}
	model := account.GetMappedModel(requestedModel)
	for _, endpoint := range cfg.Endpoints {
		if _, err := cfg.SelectEndpoint(model, string(endpoint), true); err == nil {
			out = append(out, endpoint)
		}
	}
	return out
}

// testVideoAccountConnection 只做管理员明确发起的诊断，不创建站内任务、预算预留或使用记录。
// @project-doc docs/interfaces/video_upstream.md#video_account_tests
func (s *AccountTestService) testVideoAccountConnection(c *gin.Context, account *Account, modelID, prompt string, options AccountTestOptions) error {
	if account == nil || account.Platform != PlatformVideo || account.Type != AccountTypeAPIKey || s.httpUpstream == nil {
		return s.sendErrorAndEnd(c, "Video API Key account and HTTP upstream are required")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || strings.ContainsAny(modelID, "*?") || !account.IsModelSupported(modelID) {
		return s.sendErrorAndEnd(c, "Select a supported video model before testing")
	}
	if options.AudioDataURL != "" {
		return s.sendErrorAndEnd(c, "Video account tests only accept an optional reference image")
	}
	duration := 5
	if options.Duration != nil {
		duration = *options.Duration
	}
	// 管理员试跑限制为一分钟内的显式时长，供应商更窄的模型限制仍由上游判定。
	if duration < 1 || duration > 60 {
		return s.sendErrorAndEnd(c, "Video test duration must be an integer between 1 and 60 seconds")
	}
	options.Resolution = strings.TrimSpace(options.Resolution)
	options.Ratio = strings.TrimSpace(options.Ratio)
	if options.Resolution != "" && !videoAccountTestResolution.MatchString(options.Resolution) {
		return s.sendErrorAndEnd(c, "Video resolution must be a short resolution name such as 768p or 2K")
	}
	if options.Ratio != "" && !videoAccountTestRatio.MatchString(options.Ratio) {
		return s.sendErrorAndEnd(c, "Video ratio must be adaptive or a positive ratio such as 16:9")
	}
	endpoint := VideoEndpoint(strings.TrimSpace(options.VideoEndpoint))
	allowed := false
	for _, candidate := range VideoAccountTestEndpoints(account, modelID) {
		allowed = allowed || candidate == endpoint
	}
	if !allowed {
		return s.sendErrorAndEnd(c, "Selected video endpoint is not enabled for this model")
	}
	imageURL := ""
	if options.ImageDataURL != "" {
		var err error
		imageURL, err = normalizeGrokTestImage(options.ImageDataURL)
		if err != nil {
			return s.sendErrorAndEnd(c, err.Error())
		}
	}
	model := strings.TrimSpace(account.GetMappedModel(modelID))
	if model == "" || strings.ContainsAny(model, "*?") {
		return s.sendErrorAndEnd(c, "Video test requires a concrete upstream model")
	}
	// 仅装配协议传输所需依赖，不接入正常网关的调度、停调、账务和持久任务服务。
	upstream := NewVideoUpstreamService(&OpenAIGatewayService{cfg: s.cfg, httpUpstream: s.httpUpstream, tlsFPProfileService: s.tlsFPProfileService})
	request := VideoTaskSubmitRequest{InboundProtocol: string(endpoint), Native: true}
	if endpoint == VideoEndpointKling {
		cfg, _ := account.VideoConfiguration()
		request.NativePath = cfg.ModelPaths[model]
		if request.NativePath == "" {
			request.NativePath = "/v1/videos/text2video"
			if imageURL != "" {
				request.NativePath = "/v1/videos/omni-video"
			}
		}
		if imageURL != "" && (request.NativePath == "/text-to-video/{model}" || request.NativePath == "/v1/videos/text2video") {
			return s.sendErrorAndEnd(c, "The configured Kling text-to-video path does not accept a reference image")
		}
		if imageURL == "" && request.NativePath == "/image-to-video/{model}" {
			return s.sendErrorAndEnd(c, "The configured Kling image-to-video path requires a reference image")
		}
	}
	target, err := upstream.resolveTarget(account, model, request)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	prompt = firstNonEmpty(strings.TrimSpace(prompt), "A red ball bouncing once on a white floor, short simple motion.")
	body, err := buildVideoAccountTestPayload(target, prompt, imageURL, duration, options)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), videoAccountTestTimeout)
	defer cancel()
	// 查询也禁跟随重定向，防止诊断把账号凭据发送到上游给出的其它主机。
	ctx = WithHTTPUpstreamRedirectsDisabled(ctx)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: model})
	s.sendEvent(c, TestEvent{Type: "status", Text: "正在通过 " + target.CreatePath + " 创建测试视频"})
	result, err := upstream.Submit(ctx, &VideoUpstreamSelection{Account: account, Target: target, Body: body})
	if err != nil {
		return s.sendErrorAndEnd(c, "视频创建请求失败或受理结果未知；不会自动重复提交。"+err.Error())
	}
	if videoResponseHasConflictingTaskIDs(endpoint, result.Body) {
		return s.sendErrorAndEnd(c, "Video creation returned conflicting task IDs; acceptance is unknown")
	}
	if err := videoAccountTestResponseError(endpoint, result); err != nil {
		message := err.Error()
		if result.TaskID != "" && validateUpstreamPathSegment("video task ID", result.TaskID) == nil {
			message += "；已收到任务 ID：" + result.TaskID + "，上游可能已受理并继续生成，不会自动重复提交"
		}
		return s.sendErrorAndEnd(c, message)
	}
	if result.TaskID == "" || validateUpstreamPathSegment("video task ID", result.TaskID) != nil {
		return s.sendErrorAndEnd(c, videoAccountTestTaskIDDiagnostic(endpoint, result))
	}
	taskID := result.TaskID
	s.sendEvent(c, TestEvent{Type: "content", Text: "视频任务已创建：" + taskID + "\n"})
	for {
		s.sendEvent(c, TestEvent{Type: "status", Text: "视频任务状态：" + firstNonEmpty(result.Status, "unknown")})
		switch result.Status {
		case "completed":
			return s.emitVideoAccountTestResult(c, ctx, upstream, account, target, taskID, result.VideoURL)
		case "failed", "cancelled", "expired":
			return s.sendErrorAndEnd(c, "Video task "+taskID+" ended with status "+result.Status)
		}
		// 第一轮立即查询；此后保持三秒一次，不因状态未知或请求失败重发创建。
		if result, err = upstream.Poll(ctx, account, target, taskID); err != nil {
			return s.sendErrorAndEnd(c, "视频查询失败或已取消；已提交任务可能继续生成（任务 ID："+taskID+"）。"+err.Error())
		}
		if err := videoAccountTestResponseError(endpoint, result); err != nil {
			return s.sendErrorAndEnd(c, "Video task "+taskID+": "+err.Error())
		}
		if result.Status == "completed" || result.Status == "failed" || result.Status == "cancelled" || result.Status == "expired" {
			continue
		}
		select {
		case <-ctx.Done():
			return s.sendErrorAndEnd(c, "视频测试等待超时或已取消；已提交任务可能继续生成（任务 ID："+taskID+"）")
		case <-time.After(3 * time.Second):
		}
	}
}

// videoAccountTestTaskIDDiagnostic 只在创建 ID 缺失或不能安全查询时展示固定字段的类型，不回显响应值或任意键名。
func videoAccountTestTaskIDDiagnostic(endpoint VideoEndpoint, result *VideoUpstreamResponse) string {
	reason := "视频创建响应中未识别到任务 ID"
	if result.TaskID != "" {
		reason = "视频创建响应的任务 ID 不能安全用于查询路径"
	}
	paths := []string{"id", "task_id", "output", "output.task_id", "data", "data.task_id", "data.id"}
	switch endpoint {
	case VideoEndpointMiniMax:
		paths = []string{"TaskId", "task", "task.id", "task.task_id", "task_id", "data", "data.task_id", "data.id", "id"}
	case VideoEndpointWan:
		paths = []string{"output", "output.task_id", "task_id"}
	case VideoEndpointKling:
		paths = []string{"data", "data.task_id", "task_id", "data.id", "id"}
	}
	root := gjson.ParseBytes(result.Body)
	fields := make([]string, 0, len(paths))
	for _, path := range paths {
		fields = append(fields, path+"="+videoAccountTestJSONFieldType(root.Get(path)))
	}
	return fmt.Sprintf("%s（HTTP %d）；已知响应字段：%s。上游可能已开始生成，不会自动重复提交", reason, result.StatusCode, strings.Join(fields, ", "))
}

// 类型名称来自固定集合，不能将供应商正文、凭据、链接或扩展键拼入诊断事件。
func videoAccountTestJSONFieldType(value gjson.Result) string {
	if !value.Exists() {
		return "missing"
	}
	switch value.Type {
	case gjson.Null:
		return "null"
	case gjson.False, gjson.True:
		return "boolean"
	case gjson.Number:
		return "number"
	case gjson.String:
		return "string"
	case gjson.JSON:
		if value.IsArray() {
			return "array"
		}
		return "object"
	default:
		return "missing"
	}
}

// buildVideoAccountTestPayload 仅构造诊断模板，显式适配时与正式网关复用同一参数校验。
func buildVideoAccountTestPayload(target VideoUpstreamTarget, prompt, imageURL string, duration int, options AccountTestOptions) ([]byte, error) {
	body := map[string]any{"model": target.Model, "duration": duration}
	params := body
	switch target.Endpoint {
	case VideoEndpointSeedance, VideoEndpointMiniMax:
		content := []map[string]any{{"type": "text", "text": prompt}}
		if imageURL != "" {
			content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": imageURL}, "role": "first_frame"})
		}
		body["content"] = content
	case VideoEndpointWan:
		input := map[string]any{"prompt": prompt}
		if imageURL != "" {
			input["img_url"] = imageURL
		}
		params = map[string]any{"duration": duration}
		delete(body, "duration")
		body["input"], body["parameters"] = input, params
	case VideoEndpointKling:
		body["prompt"] = prompt
		if imageURL != "" {
			if strings.HasPrefix(target.CreatePath, "/image-to-video/") {
				body["image"] = imageURL
			} else {
				body["image_list"] = []map[string]string{{"image_url": imageURL}}
			}
		}
	default:
		body["prompt"] = prompt
		if imageURL != "" {
			body["images"] = []string{imageURL}
		}
	}
	if options.Resolution != "" {
		params["resolution"] = options.Resolution
	}
	if options.Ratio != "" {
		key := "ratio"
		if target.Endpoint == VideoEndpointKling {
			key = "aspect_ratio"
		}
		params[key] = options.Ratio
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	// 历史账号和其他端点保留原参数，只有明确选择秘塔 H3 才转换为标准视频字段。
	if target.Endpoint == VideoEndpointOpenAIVideos && target.OpenAIProfile == VideoOpenAIProfileMetasoH3 {
		encoded, _, err = PrepareVideoOpenAIRequest(encoded, "", target.OpenAIProfile)
	}
	return encoded, err
}

// videoAccountTestResponseError 不回显供应商原始正文，避免自定义认证头或调试信息泄露。
func videoAccountTestResponseError(endpoint VideoEndpoint, result *VideoUpstreamResponse) error {
	if result == nil {
		return fmt.Errorf("Video upstream returned an invalid JSON response")
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return fmt.Errorf("Video upstream returned HTTP %d", result.StatusCode)
	}
	if !json.Valid(result.Body) || !gjson.ParseBytes(result.Body).IsObject() {
		return fmt.Errorf("Video upstream returned an invalid JSON response")
	}
	// 排队中的零值可能只是占位；只在生成完成后检查元数据，并避免泄露供应商原始正文。
	if result.Status == "completed" && result.MetadataError != "" {
		return fmt.Errorf("上游视频时长、尺寸或用量数据异常，请核对上游响应")
	}
	errorPaths := []string{"error"}
	codePaths := []string{}
	switch endpoint {
	case VideoEndpointMiniMax:
		errorPaths = append(errorPaths, "task.error")
		codePaths = append(codePaths, "base_resp.status_code")
	case VideoEndpointKling:
		codePaths = append(codePaths, "code")
	case VideoEndpointWan:
		errorPaths = append(errorPaths, "output.error")
		codePaths = append(codePaths, "code", "output.code")
	}
	for _, path := range errorPaths {
		if value := gjson.GetBytes(result.Body, path); value.Exists() && value.Type != gjson.Null {
			return fmt.Errorf("Video upstream returned a business error")
		}
	}
	// 只解释已知协议的业务码，兼容端点的同名扩展字段不作为新的失败契约。
	for _, path := range codePaths {
		value := gjson.GetBytes(result.Body, path)
		if value.Type == gjson.Number && value.Float() != 0 || value.Type == gjson.String && value.String() != "" && value.String() != "0" {
			return fmt.Errorf("Video upstream returned a business error")
		}
	}
	return nil
}

// emitVideoAccountTestResult 公共产物链接直接预览；只有固定内容端点才携带上游凭据。
func (s *AccountTestService) emitVideoAccountTestResult(c *gin.Context, ctx context.Context, upstream *VideoUpstreamService, account *Account, target VideoUpstreamTarget, taskID, videoURL string) error {
	if videoURL != "" {
		parsed, err := url.Parse(videoURL)
		if err != nil || !safeMediaPreviewURL(videoURL) || urlvalidator.IsBlockedHost(parsed.Hostname()) {
			return s.sendErrorAndEnd(c, "Video task completed but returned an unsafe preview URL")
		}
		s.sendEvent(c, TestEvent{Type: "video", VideoURL: videoURL})
		s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
		return nil
	}
	if target.Endpoint != VideoEndpointOpenAIVideos {
		return s.sendErrorAndEnd(c, "Video task completed but returned no usable video URL (task_id="+taskID+")")
	}
	response, err := upstream.OpenContent(ctx, account, target, taskID, "")
	if err != nil {
		return s.sendErrorAndEnd(c, "Video task completed but its content could not be downloaded")
	}
	if videoContentRedirect(response.StatusCode) {
		location := response.Header.Get("Location")
		_ = response.Body.Close()
		path, _ := videoTaskPath(target, taskID)
		base, _ := url.Parse(videoEndpointURL(target.BaseURL, path+"/content"))
		next, parseErr := url.Parse(location)
		if parseErr != nil || location == "" {
			return s.sendErrorAndEnd(c, "Video content returned an invalid redirect")
		}
		client := defaultImageDownloadHTTPClient()
		defer client.CloseIdleConnections()
		response, err = openPublicVideoContent(ctx, client, base.ResolveReference(next).String(), "")
		if err != nil {
			return s.sendErrorAndEnd(c, "Video content redirect could not be downloaded safely")
		}
	}
	// 可信长度已超过管理员预览上限时立即关闭流，未知长度仍由有界读取保护。
	if response.ContentLength > maxGrokTestVideoBytes {
		_ = response.Body.Close()
		return s.sendErrorAndEnd(c, "Video preview exceeds 64 MiB")
	}
	response, err = sanitizeVideoContentResponse(response, func() {})
	if err != nil {
		return s.sendErrorAndEnd(c, "Video task content is not a supported video")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxGrokTestVideoBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxGrokTestVideoBytes {
		return s.sendErrorAndEnd(c, "Video preview is empty, unavailable or exceeds 64 MiB")
	}
	mimeType := response.Header.Get("Content-Type")
	s.sendEvent(c, TestEvent{Type: "video", VideoURL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(body), MimeType: mimeType})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}
