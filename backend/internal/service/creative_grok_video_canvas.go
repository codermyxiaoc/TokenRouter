package service

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

const creativeGrokVideoTaskPrefix = "creative_grok_video:"

var ErrCreativeGrokVideoParameters = infraerrors.BadRequest("CREATIVE_GROK_VIDEO_PARAMETERS_UNSUPPORTED", "当前 Grok 视频支持 1–15 秒和 480p、720p、1080p，请选择可精确计费的参数")

// SetGrokVideoGateway 在原视频执行器外按冻结端点分派，现有 Video 任务仍走原实现。
func (s *CreativeCanvasService) SetGrokVideoGateway(gateway CreativeGrokVideoGateway) {
	repo, _ := s.repo.(CreativeGrokVideoRepository)
	current := s.executor("video")
	if routed, ok := current.(*creativeCanvasVideoRouter); ok {
		current = routed.video
	}
	s.SetExecutor("video", &creativeCanvasVideoRouter{video: current, grok: &creativeGrokVideoCanvasExecutor{public: s.creative, repo: repo, gateway: gateway}})
}

type creativeCanvasVideoRouter struct {
	video CreativeCanvasExecutor
	grok  CreativeCanvasExecutor
}

func (r *creativeCanvasVideoRouter) selectExecutor(e *CreativeCanvasExecution) CreativeCanvasExecutor {
	if e != nil && (e.Params.Endpoint == string(CreativeVideoEndpointGrok) || strings.HasPrefix(e.TaskID, creativeGrokVideoTaskPrefix)) {
		return r.grok
	}
	return r.video
}
func (r *creativeCanvasVideoRouter) Submit(ctx context.Context, e *CreativeCanvasExecution, assets []CreativeCanvasInputAsset) (string, error) {
	x := r.selectExecutor(e)
	if x == nil {
		return "", ErrCanvasUnavailable
	}
	return x.Submit(ctx, e, assets)
}
func (r *creativeCanvasVideoRouter) Recover(ctx context.Context, e *CreativeCanvasExecution) (string, error) {
	x := r.selectExecutor(e)
	if x == nil {
		return "", ErrCanvasUnavailable
	}
	if recoverer, ok := x.(CreativeCanvasExecutorRecoverer); ok {
		return recoverer.Recover(ctx, e)
	}
	return "", nil
}
func (r *creativeCanvasVideoRouter) Poll(ctx context.Context, e *CreativeCanvasExecution) (*CreativeCanvasExecutionResult, error) {
	x := r.selectExecutor(e)
	if x == nil {
		return nil, ErrCanvasUnavailable
	}
	return x.Poll(ctx, e)
}

// Grok 不套用 Video 的预扣账本；只在原 LegacyVideoRuntime 确认结算后归档成品。
// @project-doc docs/interfaces/grok_upstream.md#grok_video_ownership
type creativeGrokVideoCanvasExecutor struct {
	public  *CreativePublicService
	repo    CreativeGrokVideoRepository
	gateway CreativeGrokVideoGateway
}

func (x *creativeGrokVideoCanvasExecutor) Recover(ctx context.Context, e *CreativeCanvasExecution) (string, error) {
	if x == nil || x.repo == nil {
		return "", ErrCanvasUnavailable
	}
	if e == nil || e.ID == "" || e.UserID <= 0 || e.Kind != "video" || e.Params.Endpoint != string(CreativeVideoEndpointGrok) {
		return "", ErrCanvasInvalid
	}
	state, err := x.repo.GetGrokVideoExecutionState(ctx, e.UserID, e.ID)
	if err != nil {
		return "", err
	}
	if state != nil && state.Phase != "" {
		return creativeGrokVideoTaskPrefix + e.ID, nil
	}
	return "", nil
}

func (x *creativeGrokVideoCanvasExecutor) Submit(ctx context.Context, e *CreativeCanvasExecution, assets []CreativeCanvasInputAsset) (string, error) {
	if x == nil || x.public == nil || x.repo == nil || x.gateway == nil {
		return "", ErrCanvasUnavailable
	}
	if id, err := x.Recover(ctx, e); id != "" || err != nil {
		return id, err
	}
	if !x.public.enabled(ctx) {
		return "", ErrCreativeDisabled
	}
	user, err := x.public.UserRepo.GetByID(ctx, e.UserID)
	if err != nil || user == nil || user.ID != e.UserID || !user.IsActive() {
		return "", ErrUserNotFound
	}
	group, err := x.public.GroupRepo.GetByIDLite(ctx, e.Params.GroupID)
	if err != nil || group == nil || !group.IsActive() || group.Platform != PlatformGrok || !user.CanBindGroup(group.ID, group.IsExclusive) || !GroupAllowsImageGeneration(group) {
		return "", ErrCreativeGroupForbidden
	}
	models, err := x.public.creativeGrokVideoModelsForGroup(ctx, group, x.public.creativeModelSettings(ctx))
	if err != nil {
		return "", err
	}
	var model *CreativeModelPublic
	for i := range models {
		if models[i].Model == e.Params.Model && models[i].VideoEndpoint == CreativeVideoEndpointGrok {
			model = &models[i]
			break
		}
	}
	if model == nil {
		return "", ErrCreativeInvalidModel
	}
	body, err := creativeGrokVideoCanvasRequest(e, model, assets, x.public.maxPromptChars())
	if err != nil {
		return "", err
	}
	key, err := x.public.ensureCreativeManagedKey(ctx, user.ID, group.ID)
	if err != nil {
		return "", err
	}
	if !creativeVideoKeyMatches(key, user.ID, group.ID) || key.Key == "" || key.Status != StatusAPIKeyActive {
		return "", ErrCreativeGroupForbidden
	}
	copyKey := *key
	copyKey.User, copyKey.ActorUser, copyKey.Group = user, nil, group
	key = &copyKey
	id := creativeGrokVideoTaskPrefix + e.ID
	started, err := x.repo.BeginGrokVideoExecution(ctx, e, key.ID)
	if err != nil {
		return "", err
	}
	if !started {
		return x.Recover(ctx, e)
	}
	// 发送前栅栏已经落库；断连、受理不明及关联保存失败都只能恢复原请求。
	work, sink := creativeGrokVideoContextFor(ctx, e, key, "")
	response, _ := x.gateway.Generate(work, key, body)
	status := response.StatusCode
	if status == 0 {
		status = http.StatusBadGateway
	}
	code := ""
	if !sink.sent.Load() && status >= 400 {
		code = creativeProviderFailureCode(status, "")
	}
	if originalStatus := int(sink.rejectedStatus.Load()); originalStatus >= 400 {
		status, code = originalStatus, creativeProviderFailureCode(originalStatus, "")
	}
	// 只持久化本站安全类别，不保留提示词、完整错误、签名 URL 或供应商密钥。
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = x.repo.SaveGrokVideoResponse(cleanup, e.UserID, e.ID, status, sink.sent.Load(), code)
	return id, nil
}

// 参考图来自已鉴权资产，先沿用画布的真实格式/大小校验，再转换成 xAI 官方 JSON。
func creativeGrokVideoCanvasRequest(e *CreativeCanvasExecution, model *CreativeModelPublic, assets []CreativeCanvasInputAsset, maxPrompt int) ([]byte, error) {
	if e.Params.GenerateAudio != nil || e.Params.Watermark != nil {
		return nil, ErrCanvasInvalid
	}
	// 新画布入口只接受原 Grok 账务能精确表示的档位，不能把未知分辨率或超长视频按最低价结算。
	resolution, recognized := LookupVideoBillingResolution(e.Params.Resolution)
	if !recognized || resolution != e.Params.Resolution || e.Params.Duration < VideoBillingMinDurationSeconds || e.Params.Duration > VideoBillingMaxDurationSeconds {
		return nil, ErrCreativeGrokVideoParameters
	}
	_, canonical, images, err := creativeVideoCanvasRequest(e, model, assets, maxPrompt)
	if err != nil {
		return nil, err
	}
	// Grok 沿用原 xAI JSON 合同：比例写入 aspect_ratio（自动比例不发送），单张参考图写入 image。
	values := map[string]any{"model": canonical.Model, "prompt": canonical.Prompt, "duration": *canonical.Duration, "resolution": canonical.Resolution}
	if canonical.AspectRatio != "adaptive" {
		values["aspect_ratio"] = canonical.AspectRatio
	}
	if len(images) > 1 {
		return nil, ErrCanvasInvalid
	}
	if len(images) == 1 {
		values["image"] = map[string]string{"url": images[0]["image_url"], "type": "image_url"}
	}
	return json.Marshal(values)
}

func (x *creativeGrokVideoCanvasExecutor) Poll(ctx context.Context, e *CreativeCanvasExecution) (*CreativeCanvasExecutionResult, error) {
	if x == nil || x.repo == nil || x.gateway == nil || x.public == nil {
		return nil, ErrCanvasUnavailable
	}
	if e == nil || e.Kind != "video" || e.TaskID != creativeGrokVideoTaskPrefix+e.ID {
		return nil, ErrCanvasInvalid
	}
	state, err := x.repo.GetGrokVideoExecutionState(ctx, e.UserID, e.ID)
	if err != nil {
		return nil, err
	}
	if state == nil || state.Phase == "" || state.GroupID != e.Params.GroupID || state.APIKeyID <= 0 {
		return nil, ErrCanvasConflict
	}
	if state.TaskID == "" {
		if state.ErrorCode != "" && state.HTTPStatus >= 400 {
			return &CreativeCanvasExecutionResult{Status: "failed", ErrorCode: state.ErrorCode, ErrorMessage: creativeProviderFailureMessage(state.ErrorCode)}, nil
		}
		return &CreativeCanvasExecutionResult{Status: "running", ErrorCode: "CREATIVE_VIDEO_SUBMISSION_UNCONFIRMED", ErrorMessage: "上游受理结果待核对，请勿重复生成；尚未确认扣费"}, nil
	}
	task, err := x.gateway.Task(ctx, e.UserID, state.APIKeyID, state.TaskID)
	if err != nil {
		return nil, err
	}
	if task == nil || task.Source != "grok_video" || task.TaskID != state.TaskID || task.UserID != e.UserID || task.BillingUserID != e.UserID || task.APIKeyID != state.APIKeyID || task.GroupID != state.GroupID || task.AccountID != state.AccountID {
		return nil, ErrCanvasConflict
	}
	switch task.Status {
	case "failed", "cancelled", "expired":
		if task.BillingStatus != "not_billable" {
			return &CreativeCanvasExecutionResult{Status: "running", ErrorCode: "CREATIVE_VIDEO_RECONCILIATION", ErrorMessage: "视频任务状态与费用正在核对，请勿重复生成"}, nil
		}
		return &CreativeCanvasExecutionResult{Status: "failed", ErrorCode: "CREATIVE_GROK_VIDEO_FAILED", ErrorMessage: "视频生成未完成，本任务未扣费"}, nil
	case "completed":
		if task.BillingStatus != "settled" {
			return &CreativeCanvasExecutionResult{Status: "running", ErrorCode: "CREATIVE_VIDEO_SETTLEMENT_PENDING", ErrorMessage: "视频已生成，正在核对结算，请勿重复生成"}, nil
		}
	default:
		return &CreativeCanvasExecutionResult{Status: "running"}, nil
	}
	// 读取原隐藏 Key，禁止因当前目录、模型或账号变化重新生成及改变归属。
	key, err := x.public.ApiKeyRepo.GetManagedKeyByUserAndGroup(ctx, e.UserID, state.GroupID, CreativeManagedBy)
	if err != nil {
		return nil, err
	}
	if !creativeVideoKeyMatches(key, e.UserID, state.GroupID) || key.ID != state.APIKeyID || key.Key == "" {
		return nil, ErrCanvasConflict
	}
	work, _ := creativeGrokVideoContextFor(ctx, e, key, state.TaskID)
	response, err := x.gateway.OpenContent(work, key, state.TaskID)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, ErrCanvasUnavailable
	}
	contentType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || parseErr != nil || !strings.HasPrefix(contentType, "video/") || response.ContentLength > CreativeCanvasOutputLimit {
		_ = response.Body.Close()
		return nil, fmt.Errorf("creative grok video content is unavailable")
	}
	return &CreativeCanvasExecutionResult{Status: "succeeded", Outputs: []CreativeCanvasOutput{{Index: 0, MIMEType: contentType, Reader: response.Body, Size: response.ContentLength}}}, nil
}

// 原任务只读查询供私有桥使用，不能从画布路径启动额外结算或轮询 worker。
func (s *OpenAIGatewayService) CreativeGrokVideoTask(ctx context.Context, userID, keyID int64, id string) (*LegacyVideoTask, error) {
	if strings.HasPrefix(id, "seedance:") {
		return nil, ErrLegacyVideoTaskNotFound
	}
	task, err := s.legacyVideoTask(ctx, id, userID, keyID)
	if err != nil {
		return nil, err
	}
	if task.Source != "grok_video" || task.UserID != userID || task.APIKeyID != keyID {
		return nil, ErrLegacyVideoTaskNotFound
	}
	return task, nil
}

var _ CreativeCanvasExecutorRecoverer = (*creativeGrokVideoCanvasExecutor)(nil)
