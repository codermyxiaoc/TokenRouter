package service

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// VideoUpstreamService 只负责账号资格和原生HTTP协议；任务持久化、重试与结算由生命周期拥有。
// @project-doc docs/interfaces/video_upstream.md#video_protocols
type VideoUpstreamService struct{ gateway *OpenAIGatewayService }

func NewVideoUpstreamService(gateway *OpenAIGatewayService) *VideoUpstreamService {
	return &VideoUpstreamService{gateway: gateway}
}

// ParseVideoRequestModel 在选择供应商前只读取模型，计费参数必须等协议确定后解析。
func ParseVideoRequestModel(body []byte, modelPath string) (VideoRequestMetadata, error) {
	var out VideoRequestMetadata
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return out, infraerrors.BadRequest("VIDEO_INVALID_REQUEST", "Video request must be a JSON object")
	}
	if err := validateVideoJSONKeys(body); err != nil {
		return out, err
	}
	model := gjson.GetBytes(body, "model")
	modelFields, canonical := 0, true
	gjson.ParseBytes(body).ForEach(func(key, value gjson.Result) bool {
		if strings.EqualFold(key.String(), "model") {
			modelFields++
			canonical = canonical && key.String() == "model"
		}
		return true
	})
	if modelFields > 1 || !canonical {
		return out, infraerrors.BadRequest("VIDEO_MODEL_AMBIGUOUS", "model must appear once using its canonical field name")
	}
	out.Model = strings.TrimSpace(modelPath)
	if out.Model == "" {
		if model.Type != gjson.String {
			return out, infraerrors.BadRequest("VIDEO_MODEL_REQUIRED", "model is required")
		}
		out.Model = strings.TrimSpace(model.String())
	}
	if out.Model == "" {
		return out, infraerrors.BadRequest("VIDEO_MODEL_REQUIRED", "model is required")
	}
	if modelPath != "" && model.Exists() && (model.Type != gjson.String || strings.TrimSpace(model.String()) != out.Model) {
		return out, infraerrors.BadRequest("VIDEO_MODEL_CONFLICT", "Path model and body model must agree")
	}
	return out, nil
}

func videoInputHasURL(value gjson.Result) bool {
	if value.Type == gjson.String {
		return strings.TrimSpace(value.String()) != ""
	}
	if value.IsArray() {
		for _, item := range value.Array() {
			if videoInputHasURL(item) {
				return true
			}
		}
		return false
	}
	if value.IsObject() {
		return videoInputHasURL(value.Get("url")) || videoInputHasURL(value.Get("video_url"))
	}
	return false
}

// SelectAccount 支持尚未发送付费POST的容量重选；已发送请求绝不能据此故障转移。
func (s *VideoUpstreamService) SelectAccount(ctx context.Context, key *APIKey, request VideoTaskSubmitRequest) (*VideoUpstreamSelection, error) {
	if s == nil || s.gateway == nil || s.gateway.accountRepo == nil || s.gateway.concurrencyService == nil {
		return nil, infraerrors.ServiceUnavailable("VIDEO_UNAVAILABLE", "Video gateway is unavailable")
	}
	if key == nil || key.GroupID == nil || key.Group == nil || key.Group.Platform != PlatformVideo || !key.Group.IsActive() {
		return nil, infraerrors.BadRequest("VIDEO_GROUP_REQUIRED", "Video generation requires an active video group")
	}
	metadata, err := ParseVideoRequestModel(request.Body, request.ModelPath)
	if err != nil {
		return nil, err
	}
	internalModel := metadata.Model
	requestedModel := internalModel
	if value, ok := ctx.Value(ctxkey.ClientModel).(string); ok && strings.TrimSpace(value) != "" {
		requestedModel = strings.TrimSpace(value)
	}
	if s.gateway.checkChannelPricingRestriction(ctx, key.GroupID, internalModel) {
		return nil, infraerrors.BadRequest("VIDEO_MODEL_RESTRICTED", "Video model is restricted by channel pricing")
	}
	mapping, _ := s.gateway.ResolveChannelMappingAndRestrict(ctx, key.GroupID, internalModel)
	channelModel := mapping.MappedModel
	if channelModel == "" {
		channelModel = internalModel
	}
	accounts, err := s.gateway.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, *key.GroupID, PlatformVideo)
	if err != nil {
		return nil, err
	}
	preferred := make(map[int64]int)
	for index, id := range key.Group.GetRoutingAccountIDs(internalModel) {
		preferred[id] = index + 1
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		left, right := preferred[accounts[i].ID], preferred[accounts[j].ID]
		if left != right {
			if left == 0 {
				return false
			}
			if right == 0 {
				return true
			}
			return left < right
		}
		if accounts[i].Priority != accounts[j].Priority {
			return accounts[i].Priority < accounts[j].Priority
		}
		return accounts[i].ID < accounts[j].ID
	})
	var configErr error
	for i := range accounts {
		a := &accounts[i]
		if _, excluded := request.ExcludedAccountIDs[a.ID]; excluded {
			continue
		}
		if a.Platform != PlatformVideo || a.Type != AccountTypeAPIKey || !openAIStickyAccountMatchesGroup(a, key.GroupID) || !a.IsSchedulable() || !a.IsModelSupported(channelModel) || a.isModelRateLimitedWithContext(ctx, channelModel) {
			continue
		}
		model := a.GetMappedModel(channelModel)
		if s.gateway.needsUpstreamChannelRestrictionCheck(ctx, key.GroupID) && s.gateway.IsModelRestricted(ctx, *key.GroupID, model) {
			continue
		}
		target, targetErr := s.resolveTarget(a, model, request)
		if targetErr != nil {
			configErr = targetErr
			continue
		}
		metadata, err = ParseVideoEndpointRequestMetadata(request.Body, request.ModelPath, target.Endpoint)
		if err != nil {
			return nil, err
		}
		body := append([]byte(nil), request.Body...)
		if gjson.GetBytes(body, "model").Exists() {
			body, err = sjson.SetBytes(body, "model", model)
			if err != nil {
				return nil, err
			}
		}
		slot, slotErr := s.gateway.concurrencyService.AcquireAccountSlot(ctx, a.ID, a.Concurrency)
		if slotErr != nil {
			return nil, slotErr
		}
		if !slot.Acquired {
			continue
		}
		var once sync.Once
		metadata.Model = model
		billingModel := channelModel
		switch mapping.BillingModelSource {
		case BillingModelSourceRequested:
			billingModel = internalModel
		case BillingModelSourceUpstream:
			billingModel = model
		}
		observeVideoUpstream(ctx, VideoUpstreamObservation{AccountID: a.ID, AccountName: a.Name, Model: model, Endpoint: target.CreatePath, SlotAcquired: true})
		return &VideoUpstreamSelection{Account: a, Target: target, Body: body, Metadata: metadata, RequestedModel: requestedModel, InternalModel: internalModel, BillingModel: billingModel, Release: func() { once.Do(slot.ReleaseFunc) }}, nil
	}
	if configErr != nil {
		return nil, configErr
	}
	return nil, infraerrors.ServiceUnavailable("VIDEO_NO_ELIGIBLE_ACCOUNT", "No eligible video account or concurrency slot is available")
}

func (s *VideoUpstreamService) resolveTarget(a *Account, model string, request VideoTaskSubmitRequest) (VideoUpstreamTarget, error) {
	var target VideoUpstreamTarget
	cfg, err := a.VideoConfiguration()
	if err != nil {
		return target, err
	}
	endpoint, err := cfg.SelectEndpoint(model, request.InboundProtocol, request.Native)
	if err != nil {
		return target, err
	}
	base := cfg.BaseURLs[endpoint]
	if strings.TrimSpace(base) == "" {
		base = a.GetCredential("base_url")
	}
	base, err = s.gateway.validateUpstreamBaseURL(base)
	if err != nil {
		return target, infraerrors.BadRequest("VIDEO_BASE_URL_INVALID", "Video endpoint base URL is invalid")
	}
	path := ""
	switch endpoint {
	case VideoEndpointCompat:
		path = "/v1/video/generations"
	case VideoEndpointOpenAIVideos:
		path = "/v1/videos"
	case VideoEndpointSeedance:
		path = "/api/v3/contents/generations/tasks"
	case VideoEndpointWan:
		path = "/api/v1/services/aigc/video-generation/video-synthesis"
	case VideoEndpointMiniMax:
		path = "/v2/video_generation"
	case VideoEndpointKling:
		path = cfg.ModelPaths[model]
		if request.Native {
			path = request.NativePath
		}
		if !validKlingVideoPath(path) {
			return target, infraerrors.BadRequest("VIDEO_KLING_PATH_REQUIRED", "Kling requires an explicit video_model_paths template")
		}
		if err := validateUpstreamPathSegment("video model", model); err != nil {
			return target, infraerrors.BadRequest("VIDEO_MODEL_INVALID", "Video path model is invalid")
		}
		path = strings.ReplaceAll(path, "{model}", url.PathEscape(model))
	}
	return VideoUpstreamTarget{Version: 1, Endpoint: endpoint, BaseURL: base, CreatePath: path, Model: model, AccountID: a.ID}, nil
}

// Submit 不重试上游创建；HTTP返回与传输错误分别交给持久任务状态机处理。
func (s *VideoUpstreamService) Submit(ctx context.Context, selected *VideoUpstreamSelection) (*VideoUpstreamResponse, error) {
	if selected == nil {
		return nil, fmt.Errorf("video selection is required")
	}
	// 创建是不可安全重放的付费请求，禁止共享客户端因 307/308 跳转再次发送 POST。
	// 保留原始 3xx 响应交给任务状态机核对受理结果，不自动换地址或释放未知预算。
	return s.forward(WithHTTPUpstreamRedirectsDisabled(ctx), selected.Account, selected.Target, http.MethodPost, selected.Target.CreatePath, selected.Body, "")
}

func videoTaskPath(target VideoUpstreamTarget, taskID string) (string, error) {
	if strings.TrimSpace(taskID) == "" || validateUpstreamPathSegment("video task ID", taskID) != nil {
		return "", infraerrors.BadRequest("VIDEO_TASK_ID_INVALID", "Video task ID is invalid")
	}
	id := url.PathEscape(taskID)
	switch target.Endpoint {
	case VideoEndpointCompat:
		return "/v1/video/generations/" + id, nil
	case VideoEndpointOpenAIVideos:
		return "/v1/videos/" + id, nil
	case VideoEndpointSeedance:
		return "/api/v3/contents/generations/tasks/" + id, nil
	case VideoEndpointKling:
		return "/tasks?task_ids=" + url.QueryEscape(taskID), nil
	case VideoEndpointWan:
		return "/api/v1/tasks/" + id, nil
	case VideoEndpointMiniMax:
		return "/v2/query/video_generation/" + id, nil
	}
	return "", fmt.Errorf("unsupported video endpoint")
}

// Poll 始终使用持久化端点快照和任务原账号，账号能力修改不会改变任务归属。
func (s *VideoUpstreamService) Poll(ctx context.Context, account *Account, target VideoUpstreamTarget, taskID string) (*VideoUpstreamResponse, error) {
	path, err := videoTaskPath(target, taskID)
	if err != nil {
		return nil, err
	}
	return s.forward(ctx, account, target, http.MethodGet, path, nil, taskID)
}

// Cancel 只调用明确的任务详情DELETE；成功状态需进一步检查供应商确认。
func (s *VideoUpstreamService) Cancel(ctx context.Context, account *Account, target VideoUpstreamTarget, taskID string) (*VideoUpstreamResponse, error) {
	if target.Endpoint != VideoEndpointSeedance && target.Endpoint != VideoEndpointCompat {
		return nil, infraerrors.BadRequest("VIDEO_CANCEL_UNSUPPORTED", "This video endpoint does not support cancellation")
	}
	path, err := videoTaskPath(target, taskID)
	if err != nil {
		return nil, err
	}
	return s.forward(ctx, account, target, http.MethodDelete, path, nil, taskID)
}

func videoEndpointURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	for _, prefix := range []string{"/api/v3", "/api/v1", "/v3", "/v2", "/v1"} {
		if strings.HasSuffix(base, prefix) && strings.HasPrefix(path, prefix+"/") {
			return base + strings.TrimPrefix(path, prefix)
		}
	}
	return base + path
}

func (s *VideoUpstreamService) forward(ctx context.Context, account *Account, target VideoUpstreamTarget, method, path string, body []byte, taskID string) (*VideoUpstreamResponse, error) {
	if s == nil || s.gateway == nil || account == nil || account.ID != target.AccountID || account.Platform != PlatformVideo || account.Type != AccountTypeAPIKey || target.Version != 1 || !validVideoEndpoint(target.Endpoint) {
		return nil, fmt.Errorf("invalid video task account or endpoint snapshot")
	}
	base, err := s.gateway.validateUpstreamBaseURL(target.BaseURL)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(account.GetCredential("api_key"))
	if token == "" {
		return nil, fmt.Errorf("video API key is missing")
	}
	req, err := http.NewRequestWithContext(ctx, method, videoEndpointURL(base, path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if target.Endpoint == VideoEndpointWan && method == http.MethodPost {
		req.Header.Set("X-DashScope-Async", "enable")
	}
	account.ApplyHeaderOverrides(req.Header)
	var release func()
	if method != http.MethodPost && s.gateway.concurrencyService != nil {
		slot, slotErr := s.gateway.concurrencyService.AcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if slotErr != nil {
			return nil, slotErr
		}
		if !slot.Acquired {
			return nil, infraerrors.ServiceUnavailable("VIDEO_ACCOUNT_BUSY", "Original video account is busy")
		}
		release = slot.ReleaseFunc
	}
	if release != nil {
		defer release()
	}
	observation := VideoUpstreamObservation{AccountID: account.ID, AccountName: account.Name, Model: target.Model, Endpoint: req.URL.Path}
	observeVideoUpstream(ctx, observation)
	resp, err := s.gateway.httpUpstream.DoWithTLS(req, accountProxyURL(account), account.ID, account.Concurrency, s.gateway.resolveOpenAITLSProfile(account))
	if err != nil {
		observation.StatusCode, observation.ErrorMessage = http.StatusBadGateway, "Video upstream transport failed; acceptance is unknown"
		observeVideoUpstream(ctx, observation)
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, err := readUpstreamResponseBodyLimited(resp.Body, resolveUpstreamResponseReadLimit(s.gateway.cfg))
	if err != nil {
		observation.StatusCode, observation.ErrorMessage = http.StatusBadGateway, "Video upstream response could not be read; acceptance is unknown"
		observeVideoUpstream(ctx, observation)
		return nil, err
	}
	header := make(http.Header)
	writeOpenAIPassthroughResponseHeaders(header, resp.Header, s.gateway.responseHeaderFilter)
	if value := resp.Header.Get("Content-Type"); value != "" {
		header.Set("Content-Type", value)
	}
	if method == http.MethodPost && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// 创建跳转不得继续交给客户端自动重放；HTTP 状态和原包仍保留供核对。
		header.Del("Location")
	}
	if !videoResponseMatchesTask(target.Endpoint, responseBody, taskID) {
		// 查询或取消返回其它任务时整包拒绝，不能把其状态、产物或原生正文交给调用方。
		observation.StatusCode, observation.ErrorMessage = http.StatusBadGateway, "Video upstream returned a different task"
		observeVideoUpstream(ctx, observation)
		return nil, errVideoTaskResponseMismatch
	}
	result := ExtractVideoUpstreamResponse(target.Endpoint, responseBody, taskID)
	result.StatusCode, result.Header, result.Body = resp.StatusCode, header, responseBody
	if resp.StatusCode >= 400 {
		// 复用账号配置的错误码和临时停调规则，但不据此重放已发送的创建请求。
		if s.gateway.rateLimitService != nil {
			s.gateway.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, responseBody)
		}
		observation.StatusCode, observation.ErrorMessage = resp.StatusCode, sanitizeUpstreamErrorMessage(ExtractUpstreamErrorMessage(responseBody))
		if observation.ErrorMessage == "" {
			observation.ErrorMessage = http.StatusText(resp.StatusCode)
		}
		observeVideoUpstream(ctx, observation)
	}
	if method == http.MethodDelete && resp.StatusCode >= 200 && resp.StatusCode < 300 &&
		len(bytes.TrimSpace(responseBody)) == 0 && (target.Endpoint == VideoEndpointSeedance || resp.StatusCode == http.StatusNoContent) {
		result.Status = "cancelled"
	}
	return result, nil
}

// ExtractVideoUpstreamResponse 读取供应商观测，不改变客户端原生响应或凭空补齐用量。
// @project-doc docs/interfaces/video_upstream.md#video_protocols
func ExtractVideoUpstreamResponse(endpoint VideoEndpoint, body []byte, taskID string) *VideoUpstreamResponse {
	out := &VideoUpstreamResponse{TaskID: taskID}
	if !videoResponseMatchesTask(endpoint, body, taskID) {
		return out
	}
	root := gjson.ParseBytes(body)
	envelope := root
	miniMaxTaskEnvelope := false
	if endpoint == VideoEndpointMiniMax {
		// MiniMax 原生中继把任务状态、产物和用量放在 task 内；其它协议不解读这个同名扩展。
		if task := root.Get("task"); task.IsObject() {
			id := firstVideoString(task, "id")
			alias := firstVideoString(task, "task_id")
			if id == "" && alias == "" && (task.Get("id").Exists() || task.Get("task_id").Exists()) {
				// 有一个有效别名时仍沿用它；两者都不可解析时只保留已知归属，不应用终态。
				if out.TaskID == "" {
					out.TaskID = firstVideoString(envelope, "task_id", "data.task_id", "id")
				}
				return out
			}
			if id == "" {
				id = alias
			}
			if id == "" {
				// 不完整封装仍保留旧平铺位置已返回的受理 ID，不能把已受理错误包误判为无 ID 拒绝。
				id = firstVideoString(envelope, "task_id", "data.task_id", "id")
			}
			if alias != "" && ((id != "" && id != alias) || (taskID != "" && taskID != alias)) {
				// 规范 ID 与旧别名冲突时不能任选其一，否则创建和查询可能落到不同任务。
				return out
			}
			if taskID != "" && id != "" && id != taskID {
				// 原账号查询返回其它任务时保留未知状态，不能错误捕获或释放本任务预算。
				return out
			}
			if out.TaskID == "" {
				out.TaskID = id
			}
			root = task
			miniMaxTaskEnvelope = true
		}
	}
	// Kling 单ID查询可能返回数组；只读取精确ID的项，不投影其它账号任务。
	if !miniMaxTaskEnvelope {
		for _, path := range []string{"data", "tasks"} {
			arr := root.Get(path)
			if arr.IsArray() {
				for _, item := range arr.Array() {
					id := firstVideoString(item, "task_id", "id")
					if taskID != "" && id == taskID {
						root = item
						break
					}
				}
			}
		}
	}
	if out.TaskID == "" && !miniMaxTaskEnvelope {
		// request_id 在万相等协议中只是HTTP追踪号，绝不能作为异步任务归属。
		switch endpoint {
		case VideoEndpointWan:
			out.TaskID = firstVideoString(root, "output.task_id", "task_id")
		case VideoEndpointKling:
			out.TaskID = firstVideoString(root, "data.task_id", "task_id", "data.id", "id")
		case VideoEndpointMiniMax:
			out.TaskID = firstVideoString(root, "task_id", "data.task_id", "id")
		default:
			out.TaskID = firstVideoString(root, "id", "task_id", "output.task_id", "data.task_id", "data.id")
		}
	}
	out.UpstreamStatus = firstVideoString(root, "status", "task_status", "output.task_status", "data.task_status", "data.status")
	if miniMaxTaskEnvelope {
		// 封装内只读取 MiniMax 的规范字段，扩展 data/output/tasks 不属于这个任务合同。
		out.UpstreamStatus = firstVideoString(root, "status")
	}
	switch strings.ToLower(out.UpstreamStatus) {
	case "queued", "submitted", "pending":
		out.Status = "queued"
	case "running", "processing", "in_progress":
		out.Status = "processing"
	case "succeeded", "succeed", "success", "completed", "done":
		out.Status = "completed"
	case "failed", "fail", "error":
		out.Status = "failed"
	case "cancelled", "canceled", "deleted":
		out.Status = "cancelled"
	case "expired":
		out.Status = "expired"
	}
	// 已明确的原生业务拒绝保持原响应，只让任务状态机安全释放尚未受理的预留。
	if out.TaskID == "" && ((endpoint == VideoEndpointMiniMax && envelope.Get("base_resp.status_code").Type == gjson.Number && envelope.Get("base_resp.status_code").Int() != 0) || (endpoint == VideoEndpointKling && root.Get("code").Type == gjson.Number && root.Get("code").Int() != 0)) {
		out.Status = "failed"
	}
	out.VideoURL = firstVideoString(root, "content.video_url", "video.url", "output.video_url", "data.0.url", "data.video_url", "task_result.videos.0.url", "data.task_result.videos.0.url")
	if endpoint == VideoEndpointMiniMax {
		if videoURL := firstVideoString(root, "content.url"); videoURL != "" {
			out.VideoURL = videoURL
		}
	}
	out.Metadata.Model = firstVideoString(root, "model", "data.model")
	out.Metadata.Resolution = firstVideoString(root, "resolution", "video.resolution", "output.resolution")
	durationPaths := []string{"duration", "video.duration", "output.duration", "usage.duration", "data.duration", "task_result.videos.0.duration"}
	if miniMaxTaskEnvelope {
		out.VideoURL = firstVideoString(root, "content.url")
		out.Metadata.Model = firstVideoString(root, "model")
		out.Metadata.Resolution = firstVideoString(root, "resolution")
		durationPaths = []string{"usage.output_seconds", "duration"}
	} else if endpoint == VideoEndpointMiniMax {
		// 真实输出时长优先于请求时长，输入秒数和合计秒数都不能作为生成视频用量。
		durationPaths = append([]string{"usage.output_seconds"}, durationPaths...)
	}
	for _, path := range durationPaths {
		value := root.Get(path)
		if value.Type == gjson.Number && value.Float() > 0 && !math.IsInf(value.Float(), 0) {
			out.Metadata.DurationSeconds = value.Float()
			break
		}
	}
	// 已观测到 MiniMax 中继用三项全零 Token 配合真实输出秒数占位，不能据此免费结算 Token 模型。
	miniMaxPlaceholderTokens := false
	if seconds := root.Get("usage.output_seconds"); miniMaxTaskEnvelope && seconds.Type == gjson.Number && seconds.Float() > 0 && !math.IsInf(seconds.Float(), 0) {
		miniMaxPlaceholderTokens = true
		for _, path := range []string{"usage.completion_tokens", "usage.prompt_tokens", "usage.total_tokens"} {
			value := root.Get(path)
			if value.Type != gjson.Number || value.Float() != 0 {
				miniMaxPlaceholderTokens = false
				break
			}
		}
	}
	tokenPaths := []string{"usage.completion_tokens", "usage.output_tokens", "output.usage.completion_tokens", "data.usage.completion_tokens"}
	if miniMaxTaskEnvelope {
		tokenPaths = []string{"usage.completion_tokens", "usage.output_tokens"}
	}
	for _, path := range tokenPaths {
		value := root.Get(path)
		if value.Type == gjson.Number && value.Float() >= 0 && value.Float() < 1e15 && math.Trunc(value.Float()) == value.Float() {
			// 兼容文档约定未返回usage时可填0，因此零占位不能证明免费用量。
			if (endpoint == VideoEndpointCompat || endpoint == VideoEndpointOpenAIVideos || miniMaxPlaceholderTokens) && value.Float() == 0 {
				continue
			}
			n := value.Int()
			out.Metadata.Tokens = &n
			break
		}
	}
	return out
}

var errVideoTaskResponseMismatch = infraerrors.New(http.StatusBadGateway, "VIDEO_TASK_RESPONSE_MISMATCH", "Upstream video task response does not match the requested task")

// 已知任务的观测必须整体属于原任务；允许合法省略 ID，但不能用已知 ID 掩盖明确的其它归属。
func videoResponseMatchesTask(endpoint VideoEndpoint, body []byte, taskID string) bool {
	if taskID == "" {
		// 创建尚未取得受理 ID，继续沿用原有受理不明和带 ID 错误响应规则。
		return true
	}
	return videoResponseTaskIdentityMatches(endpoint, body, taskID, true)
}

// 创建无法预先指定 ID，但多个明确且矛盾的受理 ID 仍不能任选一个或当作无 ID 拒绝。
func videoResponseHasConflictingTaskIDs(endpoint VideoEndpoint, body []byte) bool {
	return !videoResponseTaskIdentityMatches(endpoint, body, "", false)
}

func videoResponseTaskIdentityMatches(endpoint VideoEndpoint, body []byte, taskID string, requireValid bool) bool {
	root := gjson.ParseBytes(body)
	if !videoResponseIdentityFieldsUnambiguous(endpoint, root) {
		return false
	}
	matchFields := func(root gjson.Result, paths ...string) bool {
		present, valid := false, false
		for _, path := range paths {
			value := root.Get(path)
			present = present || value.Exists()
			if value.Type != gjson.String || strings.TrimSpace(value.String()) == "" {
				continue
			}
			valid = true
			id := strings.TrimSpace(value.String())
			if taskID == "" {
				taskID = id
			} else if id != taskID {
				return false
			}
		}
		// 空值或非法别名可以与合法别名并存，查询中只有非法 ID 的包不能应用观测。
		return !requireValid || !present || valid
	}
	paths := []string{"id", "task_id", "output.task_id", "data.task_id", "data.id"}
	switch endpoint {
	case VideoEndpointWan:
		// 万相的 request_id 和外层 id 不是任务 ID。
		paths = []string{"output.task_id", "task_id"}
	case VideoEndpointKling:
		paths = []string{"data.task_id", "task_id", "data.id", "id"}
	case VideoEndpointMiniMax:
		if task := root.Get("task"); task.IsObject() {
			if task.Get("id").Exists() || task.Get("task_id").Exists() {
				// 原生 task 是规范封装，外层追踪 ID 和封装内协议扩展不能覆盖其归属。
				return matchFields(task, "id", "task_id")
			}
			return matchFields(root, "task_id", "data.task_id", "id")
		}
		paths = []string{"task_id", "data.task_id", "id"}
	}
	if !matchFields(root, paths...) {
		return false
	}
	if root.IsArray() {
		for _, item := range root.Array() {
			if !matchFields(item, "task_id", "id") {
				return false
			}
		}
	}
	// 原生列表不能过滤后再重放原包：只要含有其它任务，整次观测都必须拒绝。
	for _, path := range []string{"data", "tasks"} {
		list := root.Get(path)
		if !list.IsArray() {
			continue
		}
		for _, item := range list.Array() {
			// 普通 data 也可能是媒体列表，其文件 ID 不是任务 ID；仅检查可识别的任务项。
			taskItem := path == "tasks" || endpoint == VideoEndpointKling || item.Get("task_id").Exists() || item.Get("status").Exists() || item.Get("task_status").Exists()
			if taskItem && !matchFields(item, "task_id", "id") {
				return false
			}
		}
	}
	return true
}

// 只对协议中的任务 ID 和其父容器消歧，避免不同解析器取重复键首值或末值而绕过归属校验。
// 普通空/非 JSON 错误包及厂商扩展字段不在此处校验，继续遵循原有创建错误处理。
func videoResponseIdentityFieldsUnambiguous(endpoint VideoEndpoint, root gjson.Result) bool {
	if endpoint == VideoEndpointMiniMax {
		if !videoIdentityObjectKeysUnique(root, "task") {
			return false
		}
		if task := root.Get("task"); task.IsObject() {
			if !videoIdentityObjectKeysUnique(task, "id", "task_id") {
				return false
			}
			if task.Get("id").Exists() || task.Get("task_id").Exists() {
				// 规范封装已给出归属时，不把 MiniMax 的外层追踪 ID 或封装内扩展误当成任务。
				return true
			}
		}
	}
	keys := []string{"id", "task_id", "data", "tasks"}
	if endpoint == VideoEndpointWan {
		keys = []string{"task_id", "data", "tasks", "output"}
	} else if endpoint != VideoEndpointKling && endpoint != VideoEndpointMiniMax {
		keys = append(keys, "output")
	}
	if !videoIdentityObjectKeysUnique(root, keys...) ||
		!videoIdentityObjectKeysUnique(root.Get("data"), "id", "task_id") ||
		(endpoint != VideoEndpointKling && endpoint != VideoEndpointMiniMax && !videoIdentityObjectKeysUnique(root.Get("output"), "task_id")) {
		return false
	}
	lists := []gjson.Result{root, root.Get("data"), root.Get("tasks")}
	for _, list := range lists {
		if !list.IsArray() {
			continue
		}
		for _, item := range list.Array() {
			if !videoIdentityObjectKeysUnique(item, "id", "task_id") {
				return false
			}
		}
	}
	return true
}

func videoIdentityObjectKeysUnique(root gjson.Result, keys ...string) bool {
	if !root.IsObject() {
		return true
	}
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		seen[key] = false
	}
	unique := true
	root.ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		if used, relevant := seen[name]; relevant {
			if used {
				unique = false
				return false
			}
			seen[name] = true
		}
		return true
	})
	return unique
}

// 服务边界再次验证结构化 ID 和原包，避免替代传输实现只过滤状态却保留其它任务正文。
func videoUpstreamResponseMatchesTask(endpoint VideoEndpoint, response *VideoUpstreamResponse, taskID string) bool {
	return response != nil && (taskID == "" || response.TaskID == "" || response.TaskID == taskID) && videoResponseMatchesTask(endpoint, response.Body, taskID)
}

func firstVideoString(root gjson.Result, paths ...string) string {
	for _, path := range paths {
		value := root.Get(path)
		if value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
			return strings.TrimSpace(value.String())
		}
	}
	return ""
}
