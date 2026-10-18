package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type creativeVideoKeyReader interface {
	GetByID(context.Context, int64) (*APIKey, error)
}
type creativeVideoUserSlots interface {
	AcquireUserSlot(context.Context, int64, int) (*AcquireResult, error)
}
type creativeVideoBillingAdmission interface {
	CheckBillingEligibility(context.Context, *User, *APIKey, *Group, *UserSubscription, string) error
}
type creativeVideoSubscriptions interface {
	GetUsableSubscription(context.Context, int64, ...int64) (*UserSubscription, bool, error)
}
type creativeVideoModeration interface {
	Check(context.Context, ContentModerationCheckInput) (*ContentModerationDecision, error)
}

// CreativeVideoCanvasExecutor 只桥接现有视频任务，画布不另建预扣、结算或退款账本。
// @project-doc docs/domains/video_tasks.md#video_task_lifecycle
type CreativeVideoCanvasExecutor struct {
	public        *CreativePublicService
	tasks         VideoTaskLifecycle
	content       VideoTaskContentLifecycle
	repo          VideoTaskRepository
	keys          creativeVideoKeyReader
	concurrency   creativeVideoUserSlots
	billing       creativeVideoBillingAdmission
	subscriptions creativeVideoSubscriptions
	moderation    creativeVideoModeration
}

func NewCreativeVideoCanvasExecutor(public *CreativePublicService, tasks *VideoTaskService, keys APIKeyRepository, concurrency *ConcurrencyService, billing *BillingCacheService, subscriptions *SubscriptionService) *CreativeVideoCanvasExecutor {
	executor := &CreativeVideoCanvasExecutor{public: public, keys: keys}
	// 避免将空具体指针装进非空接口后绕过依赖检查。
	if tasks != nil {
		executor.tasks, executor.content, executor.repo = tasks, tasks, tasks.repo
	}
	if concurrency != nil {
		executor.concurrency = concurrency
	}
	if billing != nil {
		executor.billing = billing
	}
	if subscriptions != nil {
		executor.subscriptions = subscriptions
	}
	if public != nil && public.Moderation != nil {
		executor.moderation = public.Moderation
	}
	return executor
}

func (s *CreativeVideoCanvasExecutor) Submit(ctx context.Context, e *CreativeCanvasExecution, assets []CreativeCanvasInputAsset) (string, error) {
	if s == nil || s.public == nil || s.tasks == nil || s.repo == nil || s.concurrency == nil || s.billing == nil || s.subscriptions == nil {
		return "", ErrCanvasUnavailable
	}
	if e == nil || e.ID == "" || len(e.ID) > 100 || e.UserID <= 0 || e.Kind != "video" || e.CanvasID == "" {
		return "", ErrCanvasInvalid
	}
	idempotency := "creative_video:" + e.ID
	// 已受理任务恢复先于开关、目录及用户余额检查；这里只读原隐藏 Key，不补建或更换。
	if recovered, recoverErr := s.Recover(ctx, e); recoverErr != nil || recovered != "" {
		return recovered, recoverErr
	}
	if !s.public.enabled(ctx) {
		return "", ErrCreativeDisabled
	}
	user, err := s.public.UserRepo.GetByID(ctx, e.UserID)
	if err != nil || user == nil || user.ID != e.UserID || !user.IsActive() {
		return "", ErrUserNotFound
	}
	group, err := s.public.GroupRepo.GetByIDLite(ctx, e.Params.GroupID)
	if err != nil || group == nil || group.ID != e.Params.GroupID || !group.IsActive() || group.Platform != PlatformVideo || !user.CanBindGroup(group.ID, group.IsExclusive) {
		return "", ErrCreativeGroupForbidden
	}
	key, err := s.public.ensureCreativeManagedKey(ctx, user.ID, group.ID)
	if err != nil {
		return "", err
	}
	if !creativeVideoKeyMatches(key, user.ID, group.ID) {
		return "", ErrCreativeGroupForbidden
	}
	copyKey := *key
	copyKey.User, copyKey.ActorUser, copyKey.Group = user, nil, group
	key = &copyKey
	// 进程可能在上游受理后、保存画布 task_id 前退出；先找原账本，绝不重新准入或扣费。
	if old, findErr := s.repo.FindIdempotent(ctx, key.ID, idempotency); findErr == nil {
		if old == nil || old.UserID != e.UserID || old.GroupID != e.Params.GroupID || old.APIKeyID != key.ID {
			return "", ErrVideoTaskConflict
		}
		return old.ID, nil
	} else if !errors.Is(findErr, ErrVideoTaskNotFound) {
		return "", findErr
	}
	if key.Status != StatusAPIKeyActive {
		return "", ErrCreativeGroupForbidden
	}
	models, err := s.public.creativeVideoModelsForGroup(ctx, group, s.public.creativeModelSettings(ctx))
	if err != nil {
		return "", err
	}
	var model *CreativeModelPublic
	for i := range models {
		if models[i].Model == e.Params.Model && string(models[i].VideoEndpoint) == e.Params.Endpoint {
			model = &models[i]
			break
		}
	}
	if model == nil {
		return "", ErrCreativeInvalidModel
	}
	body, images, err := creativeVideoCanvasRequest(e, model, assets, s.public.maxPromptChars())
	if err != nil {
		return "", err
	}
	// 原图保留在素材库；仅把可信原始字节上限交给真实账号的协议准入检查，不压缩或改写素材。
	var maxReferenceImageBytes int64
	for _, asset := range assets {
		if size := int64(len(asset.Content)); size > maxReferenceImageBytes {
			maxReferenceImageBytes = size
		}
	}
	adaptiveRatio, err := creativeVideoCanvasAdaptiveRatio(e, model, assets)
	if err != nil {
		return "", err
	}
	if s.moderation != nil {
		payload, _ := json.Marshal(map[string]any{"model": e.Params.Model, "prompt": e.Params.Prompt, "images": images})
		decision, moderationErr := s.moderation.Check(ctx, ContentModerationCheckInput{RequestID: idempotency, UserID: user.ID, BillingUserID: user.ID, APIKeyID: key.ID, GroupID: &group.ID, GroupName: group.Name, Endpoint: "/api/v1/creative/canvases/executions", Provider: PlatformVideo, Model: e.Params.Model, Protocol: ContentModerationProtocolOpenAIImages, Body: payload, NoMediaRetention: true})
		// 审核服务错误仍遵循现有 fail-open；命中阻断必须发生在并发和预扣之前。
		if moderationErr == nil && decision != nil && decision.Blocked {
			return "", ErrCreativeContentBlocked
		}
	}
	slot, err := s.concurrency.AcquireUserSlot(ctx, user.ID, user.Concurrency)
	if err != nil {
		return "", err
	}
	if slot == nil || !slot.Acquired {
		return "", ErrVideoTaskBusy
	}
	if slot.ReleaseFunc != nil {
		defer slot.ReleaseFunc()
	}
	subscription, _, err := s.subscriptions.GetUsableSubscription(ctx, user.ID, group.ID)
	if err != nil && !errors.Is(err, ErrSubscriptionNotFound) {
		return "", err
	}
	if err = s.billing.CheckBillingEligibility(ctx, user, key, group, subscription, PlatformVideo); err != nil {
		return "", err
	}
	inboundProtocol := e.Params.Endpoint
	if inboundProtocol == string(VideoEndpointCompat) {
		inboundProtocol = "unified"
	}
	response, err := s.tasks.Submit(ctx, key, VideoTaskSubmitRequest{Body: body, ContentType: "application/json", InboundProtocol: inboundProtocol, Native: e.Params.Endpoint != string(VideoEndpointCompat) && e.Params.Endpoint != string(VideoEndpointOpenAIVideos), IdempotencyKey: idempotency, canvasAdaptiveRatio: adaptiveRatio, canvasMaxReferenceImageBytes: maxReferenceImageBytes})
	if err != nil {
		// 预扣后保存失败也可能已经存在任务，必须先绑定原账本，不能让画布丢掉冻结金额。
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if recovered, recoverErr := s.Recover(cleanup, e); recovered != "" || recoverErr != nil {
			return recovered, recoverErr
		}
		return "", err
	}
	if response == nil || response.LocalTaskID == "" {
		return "", ErrCanvasUnavailable
	}
	// HTTP 失败但已有本地任务时保留任务绑定，由视频状态机完成释放或核对。
	return response.LocalTaskID, nil
}

// Recover 在参考素材已过期或配置已停用时仍能找回原任务，不发送上游请求。
func (s *CreativeVideoCanvasExecutor) Recover(ctx context.Context, e *CreativeCanvasExecution) (string, error) {
	if s == nil || s.public == nil || s.public.ApiKeyRepo == nil || s.repo == nil {
		return "", ErrCanvasUnavailable
	}
	if e == nil || e.ID == "" || e.Kind != "video" || e.UserID <= 0 || e.Params.GroupID <= 0 {
		return "", ErrCanvasInvalid
	}
	key, err := s.public.ApiKeyRepo.GetManagedKeyByUserAndGroup(ctx, e.UserID, e.Params.GroupID, CreativeManagedBy)
	if errors.Is(err, ErrAPIKeyNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !creativeVideoKeyMatches(key, e.UserID, e.Params.GroupID) {
		return "", ErrCreativeGroupForbidden
	}
	task, err := s.repo.FindIdempotent(ctx, key.ID, "creative_video:"+e.ID)
	if errors.Is(err, ErrVideoTaskNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if task == nil || task.UserID != e.UserID || task.GroupID != e.Params.GroupID || task.APIKeyID != key.ID {
		return "", ErrVideoTaskConflict
	}
	return task.ID, nil
}

func creativeVideoKeyMatches(key *APIKey, userID, groupID int64) bool {
	return key != nil && key.ID > 0 && key.UserID == userID && key.GroupID != nil && *key.GroupID == groupID && key.ManagedBy != nil && *key.ManagedBy == CreativeManagedBy && key.TeamID == nil && !key.IsComposite && !key.SmartRouting
}

// 素材只取已经鉴权读取的本站资产字节；不接收 URL，不触发额外网络下载。
func creativeVideoCanvasRequest(e *CreativeCanvasExecution, model *CreativeModelPublic, assets []CreativeCanvasInputAsset, maxPrompt int) ([]byte, []map[string]string, error) {
	p := e.Params
	caps := model.VideoCapabilities
	if caps == nil || p.Operation != CreativeOperationGenerate || p.Prompt == "" || strings.TrimSpace(p.Prompt) == "" || utf8.RuneCountInString(p.Prompt) > maxPrompt || math.IsNaN(p.Duration) || math.IsInf(p.Duration, 0) || p.ImageSize != "" || p.Quality != "" || p.Background != "" || p.ThinkingLevel != "" || p.MaskAssetID != "" {
		return nil, nil, ErrCanvasInvalid
	}
	durationAllowed := false
	for _, duration := range caps.Durations {
		if duration == p.Duration {
			durationAllowed = true
		}
	}
	resolution, resolutionAllowed := creativeVideoCanvasResolution(caps, p.Resolution)
	ratio, ratioAllowed := creativeVideoCanvasAspectRatio(caps, p.AspectRatio)
	if !durationAllowed || !resolutionAllowed || !ratioAllowed || len(assets) > caps.MaxReferenceImages || len(assets) != len(p.References) {
		return nil, nil, ErrCanvasInvalid
	}
	urls := []string{}
	moderationImages := []map[string]string{}
	seen := map[string]bool{}
	var total int64
	for i, asset := range assets {
		if asset.Asset == nil || asset.Asset.UserID != e.UserID || asset.Asset.ID != p.References[i].AssetID || asset.Asset.MediaType != "image" || seen[asset.Asset.ID] || asset.Role != p.References[i].Role {
			return nil, nil, ErrCanvasInvalid
		}
		if asset.Role != "" && asset.Role != "first_frame" && asset.Role != "reference_image" {
			return nil, nil, ErrCanvasInvalid
		}
		seen[asset.Asset.ID] = true
		total += int64(len(asset.Content))
		if total > 64<<20 {
			return nil, nil, ErrCreativeAssetTooLarge
		}
		image, err := normalizeCreativeImageInput(CreativeInputImage{Bytes: asset.Content, Mime: asset.Asset.MIMEType}, 32<<20)
		if err != nil {
			return nil, nil, err
		}
		width, height, err := creativeImageDimensions(image.Bytes, image.Mime)
		if err != nil || width < 8 || height < 8 || width > 16384 || height > 16384 || int64(width)*int64(height) > 67108864 {
			return nil, nil, ErrCreativeInvalidMime
		}
		url := "data:" + image.Mime + ";base64," + base64.StdEncoding.EncodeToString(image.Bytes)
		urls = append(urls, url)
		moderationImages = append(moderationImages, map[string]string{"image_url": url})
	}
	values := map[string]any{"model": p.Model, "prompt": p.Prompt, "duration": p.Duration, "resolution": resolution, "ratio": ratio}
	if p.GenerateAudio != nil {
		values["generate_audio"] = *p.GenerateAudio
	}
	if p.Watermark != nil {
		values["watermark"] = *p.Watermark
	}
	switch model.VideoEndpoint {
	case VideoEndpointSeedance, VideoEndpointMiniMax:
		content := []any{map[string]string{"type": "text", "text": p.Prompt}}
		for i, url := range urls {
			role := p.References[i].Role
			if role == "" {
				if len(urls) == 1 {
					role = "first_frame"
				} else {
					role = "reference_image"
				}
			}
			content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}, "role": role})
		}
		delete(values, "prompt")
		values["content"] = content
	case VideoEndpointWan:
		input := map[string]any{"prompt": p.Prompt}
		if len(urls) > 0 {
			input["img_url"] = urls[0]
		}
		values = map[string]any{"model": p.Model, "input": input, "parameters": map[string]any{"duration": p.Duration, "resolution": resolution, "ratio": ratio}}
		if p.GenerateAudio != nil {
			values["parameters"].(map[string]any)["generate_audio"] = *p.GenerateAudio
		}
		if p.Watermark != nil {
			values["parameters"].(map[string]any)["watermark"] = *p.Watermark
		}
	default:
		if len(urls) > 0 {
			values["images"] = urls
		}
		if model.VideoEndpoint == VideoEndpointKling || model.VideoEndpoint == VideoEndpointVidu {
			delete(values, "ratio")
			values["aspect_ratio"] = ratio
		}
	}
	body, err := json.Marshal(values)
	return body, moderationImages, err
}

// 自定义分辨率只开放数字 p；2K/4K 和精确尺寸仍须位于管理员白名单中。
// 与价卡使用同一规范化函数，保留真实档位送入原视频任务预扣链路，不降档或补造价格。
func creativeVideoCanvasResolution(caps *CreativeVideoCapabilities, raw string) (string, bool) {
	if caps == nil {
		return "", false
	}
	if containsCreativeOperation(caps.Resolutions, raw) {
		return raw, true
	}
	if !caps.AllowCustomResolution || !strings.HasSuffix(strings.ToLower(strings.TrimSpace(raw)), "p") {
		return "", false
	}
	normalized, err := NormalizeVideoPriceResolution(raw)
	if err != nil || !strings.HasSuffix(normalized, "p") {
		return "", false
	}
	return normalized, true
}

// 比例只接受运行目录的预设与原配置，取消自由输入不影响已受理任务优先恢复。
func creativeVideoCanvasAspectRatio(caps *CreativeVideoCapabilities, raw string) (string, bool) {
	if caps == nil {
		return "", false
	}
	if containsCreativeOperation(caps.AspectRatios, raw) {
		return raw, true
	}
	return "", false
}

func (s *CreativeVideoCanvasExecutor) Poll(ctx context.Context, e *CreativeCanvasExecution) (*CreativeCanvasExecutionResult, error) {
	if s == nil || s.repo == nil || s.tasks == nil || s.content == nil || s.keys == nil {
		return nil, ErrCanvasUnavailable
	}
	if e == nil || e.Kind != "video" || e.UserID <= 0 || e.TaskID == "" {
		return nil, ErrCanvasInvalid
	}
	task, err := s.repo.Get(ctx, e.TaskID)
	if err != nil {
		return nil, err
	}
	if task == nil || task.ID != e.TaskID || task.UserID != e.UserID || task.GroupID != e.Params.GroupID || task.IdempotencyKey != "creative_video:"+e.ID {
		return nil, ErrVideoTaskNotFound
	}
	key, err := s.keys.GetByID(ctx, task.APIKeyID)
	if err != nil {
		return nil, err
	}
	if !creativeVideoKeyMatches(key, e.UserID, task.GroupID) || key.ID != task.APIKeyID {
		return nil, ErrVideoTaskNotFound
	}
	// 原账本决定查询身份；目录、模型端点、账号配置或分组开关变化不改变已有任务。
	if _, err = s.tasks.Query(ctx, key, task.ID, "unified"); err != nil {
		return nil, err
	}
	task, err = s.repo.Get(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if task == nil || task.UserID != e.UserID || task.APIKeyID != key.ID || task.GroupID != e.Params.GroupID {
		return nil, ErrVideoTaskNotFound
	}
	// 只投影原任务的核对阶段；不把受理不明当成失败，也不在画布层退款或重提。
	if task.Status == "submission_unknown" {
		return &CreativeCanvasExecutionResult{Status: "running", ErrorCode: "CREATIVE_VIDEO_SUBMISSION_UNCONFIRMED", ErrorMessage: "上游受理结果待核对，请勿重复生成；预扣尚未结算"}, nil
	}
	switch task.Status {
	case "completed":
		if task.BillingStatus != "settled" {
			return &CreativeCanvasExecutionResult{Status: "running", ErrorCode: "CREATIVE_VIDEO_SETTLEMENT_PENDING", ErrorMessage: "视频已生成，正在核对结算，请勿重复生成"}, nil
		}
		response, err := s.content.OpenContent(ctx, key, task.ID, "unified", "")
		if err != nil {
			return nil, err
		}
		if response == nil || response.Body == nil {
			return nil, ErrCanvasUnavailable
		}
		contentType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if response.StatusCode != http.StatusOK || parseErr != nil || !strings.HasPrefix(contentType, "video/") || response.ContentLength > CreativeCanvasOutputLimit {
			_ = response.Body.Close()
			return nil, fmt.Errorf("creative video content is unavailable")
		}
		return &CreativeCanvasExecutionResult{Status: "succeeded", Outputs: []CreativeCanvasOutput{{Index: 0, MIMEType: contentType, Reader: response.Body, Size: response.ContentLength}}}, nil
	case "failed", "cancelled", "expired":
		if task.BillingStatus != "released" {
			return &CreativeCanvasExecutionResult{Status: "running", ErrorCode: "CREATIVE_VIDEO_RELEASE_PENDING", ErrorMessage: "视频任务未完成，正在核对预扣处理结果，请勿重复生成"}, nil
		}
		return &CreativeCanvasExecutionResult{Status: "failed", ErrorCode: "CREATIVE_VIDEO_FAILED", ErrorMessage: "视频任务未完成，预扣已释放"}, nil
	default:
		if task.BillingStatus == "reconciliation" {
			return &CreativeCanvasExecutionResult{Status: "running", ErrorCode: "CREATIVE_VIDEO_RECONCILIATION", ErrorMessage: "视频任务状态与费用正在核对，请勿重复生成"}, nil
		}
		return &CreativeCanvasExecutionResult{Status: "running"}, nil
	}
}
