package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// legacyVideoEvidence 只保存结算和状态依据，不保存上游原文、凭据或签名媒体地址。
type legacyVideoEvidence struct {
	Usage                                          OpenAIUsage
	Model, BillingModel, UpstreamModel, Resolution string
	Count, Seconds                                 int
	Duration                                       time.Duration
	Observation                                    *MediaTaskObservation
	Quote                                          *LegacyVideoBillingQuote
}

// LegacyVideoRecovery 通过原账号查询旧协议任务，后台恢复不得再次调用生成接口。
// @project-doc docs/interfaces/grok_upstream.md#grok_video_ownership
type LegacyVideoRecovery struct {
	repo       LegacyVideoTaskRepository
	gateway    *OpenAIGatewayService
	keys       APIKeyRepository
	keyService *APIKeyService
	observer   MediaTaskObserver
}

func NewLegacyVideoRecovery(repo LegacyVideoTaskRepository, gateway *OpenAIGatewayService, keys APIKeyRepository, keyService *APIKeyService, observer *MediaTaskService) *LegacyVideoRecovery {
	gateway.legacyVideoTasks = repo
	return &LegacyVideoRecovery{repo: repo, gateway: gateway, keys: keys, keyService: keyService, observer: observer}
}

// LegacyVideoTaskIdentity 保留方舟命名空间，数据库中的 TaskID 采用原始供应商 ID。
func LegacyVideoTaskIdentity(id string) (string, string) {
	if strings.HasPrefix(id, "seedance:") {
		return "seedance_video", strings.TrimPrefix(id, "seedance:")
	}
	return "grok_video", id
}

func legacyVideoTaskKey(task *LegacyVideoTask) string {
	if task.Source == "seedance_video" {
		return SeedanceTaskKey(task.TaskID)
	}
	return task.TaskID
}

// HasLegacyVideoRecovery 表示服务器已装配持久恢复，客户端查询不再负责扣费。
func (s *OpenAIGatewayService) HasLegacyVideoRecovery() bool {
	return s != nil && s.legacyVideoTasks != nil
}

func (s *OpenAIGatewayService) legacyVideoTask(ctx context.Context, id string, userID, keyID int64) (*LegacyVideoTask, error) {
	if !s.HasLegacyVideoRecovery() {
		return nil, ErrLegacyVideoTaskNotFound
	}
	source, taskID := LegacyVideoTaskIdentity(id)
	return s.legacyVideoTasks.Get(ctx, source, taskID, userID, keyID)
}

// CheckLegacyVideoStorage 在发起付费创建前拒绝已知的数据库故障。
func (s *OpenAIGatewayService) CheckLegacyVideoStorage(ctx context.Context) error {
	if !s.HasLegacyVideoRecovery() {
		return nil
	}
	_, err := s.legacyVideoTasks.Get(ctx, "grok_video", "storage-check", 1, 1)
	if errors.Is(err, ErrLegacyVideoTaskNotFound) {
		return nil
	}
	return err
}

// TrackLegacyVideoTask 必须在返回创建成功之前完成；缓存不再是恢复的唯一依据。
func (s *OpenAIGatewayService) TrackLegacyVideoTask(ctx context.Context, task *LegacyVideoTask) error {
	if !s.HasLegacyVideoRecovery() {
		return nil
	}
	if task.RequestID == "" {
		task.RequestID = StableGrokVideoBillingRequestID(legacyVideoTaskKey(task))
	}
	return s.legacyVideoTasks.Save(ctx, task)
}

// legacyVideoDiscardWriter 丢弃后台查询响应，只由转发层提取经过验证的状态和用量。
type legacyVideoDiscardWriter struct{ header http.Header }

func (w *legacyVideoDiscardWriter) Header() http.Header       { return w.header }
func (*legacyVideoDiscardWriter) WriteHeader(int)             {}
func (*legacyVideoDiscardWriter) Write(b []byte) (int, error) { return len(b), nil }

func (r *LegacyVideoRecovery) poll(ctx context.Context, task *LegacyVideoTask, account *Account) (*OpenAIForwardResult, error) {
	if r.gateway.concurrencyService != nil {
		slot, err := r.gateway.concurrencyService.AcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if err != nil {
			return nil, err
		}
		if slot == nil || !slot.Acquired {
			return nil, errors.New("视频原账号查询容量暂满")
		}
		defer slot.ReleaseFunc()
	}
	c, _ := gin.CreateTestContext(&legacyVideoDiscardWriter{header: make(http.Header)})
	c.Request, _ = http.NewRequestWithContext(ctx, http.MethodGet, "/v1/videos/"+task.TaskID, nil)
	if task.Source == "seedance_video" {
		return r.gateway.ForwardSeedance(ctx, c, account, SeedanceEndpointStatus, legacyVideoTaskKey(task), nil)
	}
	return r.gateway.ForwardGrokMedia(ctx, c, account, GrokMediaEndpointVideoStatus, task.TaskID, nil, "")
}

func (r *LegacyVideoRecovery) checkpoint(ctx context.Context, task *LegacyVideoTask, evidence *legacyVideoEvidence) error {
	data, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	task.SettlementResult = data
	return r.repo.Checkpoint(ctx, task)
}

// RecoverLegacyVideoTask 将终态证据、报价、实际扣费和展示记录分阶段恢复。
// 任一阶段失败都保留原任务，重试不会生成第二个视频，也不会跳过缺失的使用记录。
func (r *LegacyVideoRecovery) RecoverLegacyVideoTask(ctx context.Context, task *LegacyVideoTask) error {
	key, err := r.keys.GetByID(ctx, task.APIKeyID)
	if err == nil && key != nil && key.UserID != task.UserID {
		return errors.New("视频原密钥归属不匹配")
	}
	// 历史记录仅从原付款人的缓存取回快照，团队归属未知时禁止猜测付款人。
	if task.BillingUserID == 0 && key != nil && key.TeamID == nil {
		task.BillingUserID = key.UserID
	}
	if task.Pending == nil && task.BillingUserID > 0 {
		task.Pending, _ = r.gateway.LoadGrokVideoPendingBilling(ctx, legacyVideoTaskKey(task), task.BillingUserID, task.APIKeyID)
	}
	evidence := &legacyVideoEvidence{}
	if len(task.SettlementResult) > 0 {
		if err := json.Unmarshal(task.SettlementResult, evidence); err != nil {
			return errors.New("视频终态证据损坏，需人工核对")
		}
	}
	var preview *MediaTaskVideoSnapshot
	terminalEvidence := evidence.Observation != nil && (evidence.Observation.Status == "completed" || evidence.Observation.Status == "failed" || evidence.Observation.Status == "cancelled" || evidence.Observation.Status == "expired")
	missingUsage := evidence.Observation != nil && evidence.Observation.Status == "completed" && evidence.Quote == nil && ((task.Source == "seedance_video" && evidence.Usage.OutputTokens <= 0) || (task.Source == "grok_video" && evidence.Count <= 0))
	// 已保存的终态和冻结报价不再依赖上游账号仍处于可调度状态。
	account, accountErr := r.gateway.accountRepo.GetByID(ctx, task.AccountID)
	if !terminalEvidence || missingUsage {
		deleted, err := r.legacyVideoDeletionSeen(ctx, task)
		if err != nil {
			return err
		}
		if deleted {
			// 空删除响应不证明免费；已删除且缺失用量的任务只能人工核对，不能继续 GET 404。
			task.BillingStatus = "reconciliation"
			task.NextPollAt = time.Now().Add(time.Hour)
			return errors.New("上游视频任务已删除，缺少完整结算依据，需人工核对")
		}
		if accountErr != nil || account == nil {
			return errors.New("视频原账号无法恢复，需人工核对")
		}
		result, err := r.poll(ctx, task, account)
		if err != nil {
			return errors.New("视频原任务查询失败，稍后重试")
		}
		if result == nil || result.MediaTaskObservation == nil {
			return errors.New("视频原任务未返回可识别状态")
		}
		// 补查晚到用量时，上游短暂回退不得覆盖已保存的完成证据。
		if missingUsage && result.MediaTaskObservation.Status != "completed" {
			return errors.New("视频已完成，上游用量尚未可用，稍后补查")
		}
		evidence = &legacyVideoEvidence{Usage: result.Usage, Model: result.Model, BillingModel: result.BillingModel,
			UpstreamModel: result.UpstreamModel, Resolution: result.VideoResolution,
			Count: result.VideoCount, Seconds: result.VideoDurationSeconds, Duration: result.Duration,
			Observation: result.MediaTaskObservation}
		preview = result.MediaTaskVideoPreview
		task.Status = evidence.Observation.Status
		// 原始错误可能包含带签名地址，持久证据只保留固定提示。
		if evidence.Observation.ErrorMessage != "" {
			evidence.Observation.ErrorMessage = "上游视频任务未成功完成"
		}
		if err := r.checkpoint(ctx, task, evidence); err != nil {
			return err
		}
	}
	observation := *evidence.Observation
	observation.Source, observation.TaskID = task.Source, task.TaskID
	observation.UserID, observation.APIKeyID = task.UserID, task.APIKeyID
	observation.GroupID, observation.AccountID = &task.GroupID, &task.AccountID
	observation.CreatedAt, observation.Model = task.CreatedAt, task.Model
	observation.RequestID = StableGrokVideoBillingRequestID(legacyVideoTaskKey(task))
	if err := r.observer.ObserveMediaTask(ctx, observation); err != nil {
		return err
	}
	if p, ok := r.observer.(MediaTaskVideoPreviewObserver); ok && preview != nil {
		// 媒体缓存不是扣费的前提，地址不会写进持久结算依据。
		_ = p.ObserveMediaTaskVideoPreview(ctx, observation, preview)
	}
	switch task.Status {
	case "failed", "cancelled", "expired":
		task.BillingStatus = "not_billable"
		return nil
	case "completed":
	default:
		task.NextPollAt = time.Now().Add(10 * time.Second)
		return nil
	}
	billed, err := r.repo.HasBilling(ctx, observation.RequestID, task.APIKeyID)
	if err != nil {
		return err
	}
	logged, err := r.repo.HasUsage(ctx, observation.RequestID, task.APIKeyID)
	if err != nil {
		return err
	}
	if billed && logged {
		task.BillingStatus = "settled"
		return nil
	}
	if billed && evidence.Quote == nil {
		task.BillingStatus = "reconciliation"
		task.NextPollAt = time.Now().Add(time.Hour)
		return errors.New("视频已扣费但缺少原使用记录快照，需人工核对")
	}
	if accountErr != nil || account == nil {
		if evidence.Quote == nil || evidence.Quote.Command == nil {
			return errors.New("视频原账号无法恢复报价，需人工核对")
		}
		// 原扣款命令已冻结账号统计口径，此处不读取凭据、不调用上游。
		account = &Account{ID: task.AccountID, Type: evidence.Quote.Command.AccountType}
	}
	// 完成状态不等于结算成功；缺少原资金快照时公开显示完成，但保留账务待核对。
	if task.Pending == nil || task.Pending.SeedanceBilling == nil || task.BillingUserID <= 0 {
		task.BillingStatus = "reconciliation"
		task.NextPollAt = time.Now().Add(time.Hour)
		return errors.New("视频已完成但缺少原计费快照，需人工核对")
	}
	pending := task.Pending
	snapshot := pending.SeedanceBilling
	if snapshot.Group == nil || snapshot.Group.ID != task.GroupID || (snapshot.Subscription != nil && snapshot.Subscription.UserID != task.BillingUserID) {
		return errors.New("视频原资金快照归属不匹配")
	}
	payer, err := r.gateway.userRepo.GetByID(ctx, task.BillingUserID)
	if err != nil {
		return errors.New("视频原付款人无法恢复")
	}
	if key == nil {
		// 删除/停用 Key 只能禁止新请求，不能免除已受理任务的原付款责任。
		key = &APIKey{ID: task.APIKeyID, UserID: task.UserID}
	}
	billingKey := *key
	billingKey.TeamID = snapshot.TeamID
	billingKey.ActorUser = &User{ID: task.UserID}
	billingKey.User, billingKey.Group, billingKey.GroupID = payer, snapshot.Group, &task.GroupID
	billingKey.BillingMode, billingKey.PreferredSubscriptionID = snapshot.BillingMode, snapshot.PreferredSubscriptionID
	result := &OpenAIForwardResult{RequestID: observation.RequestID, ResponseID: legacyVideoTaskKey(task),
		Model: pending.Model, BillingModel: firstNonEmpty(pending.BillingModel, pending.Model),
		UpstreamModel: firstNonEmpty(pending.UpstreamModel, evidence.UpstreamModel), Usage: evidence.Usage,
		VideoCount: evidence.Count, VideoResolution: pending.VideoResolution,
		VideoDurationSeconds: evidence.Seconds, Duration: GrokVideoE2EDuration(pending.CreatedAt, observationTime(evidence.Observation))}
	if task.Source == "grok_video" {
		if result.VideoCount <= 0 {
			return errors.New("视频已完成但缺少可计费成果")
		}
		if result.VideoDurationSeconds <= 0 {
			result.VideoDurationSeconds = pending.VideoDurationSeconds
		}
		if result.VideoDurationSeconds <= 0 || result.VideoResolution == "" {
			return errors.New("视频已完成但缺少时长或分辨率依据")
		}
	} else if result.Usage.OutputTokens <= 0 {
		return errors.New("视频已完成但上游未提供真实 Token 用量")
	}
	endpoint := "/v1/videos/generations"
	if task.Source == "seedance_video" {
		endpoint = "/api/v3/contents/generations/tasks"
	}
	input := &OpenAIRecordUsageInput{LegacyVideoTaskID: task.ID, Result: result, APIKey: &billingKey, User: payer, Account: account,
		Subscription: snapshot.Subscription, APIKeyService: r.keyService,
		InboundEndpoint: firstNonEmpty(snapshot.InboundEndpoint, endpoint), UpstreamEndpoint: firstNonEmpty(snapshot.UpstreamEndpoint, endpoint), QuotaPlatform: firstNonEmpty(snapshot.QuotaPlatform, snapshot.Group.Platform),
		RequestPayloadHash: HashUsageRequestPayload([]byte(legacyVideoTaskKey(task))),
		PricingAt:          task.CreatedAt, ChannelUsageFields: snapshot.ChannelMapping.ToUsageFields(pending.OriginalModel, result.UpstreamModel)}
	if snapshot.Subscription != nil {
		id := snapshot.Subscription.ID
		input.SubscriptionScopeID = &id
	}
	if evidence.Quote == nil {
		input.PrepareLegacyVideoQuote = true
		if err := r.gateway.RecordUsage(ctx, input); err != nil {
			return fmt.Errorf("视频报价失败: %w", err)
		}
		evidence.Quote = input.LegacyVideoQuote
		if err := r.checkpoint(ctx, task, evidence); err != nil {
			return err
		}
	}
	input.PrepareLegacyVideoQuote, input.LegacyVideoQuote = false, evidence.Quote
	if err := r.gateway.RecordUsage(ctx, input); err != nil {
		return errors.New("视频结算或使用记录保存失败，后台将重试")
	}
	task.BillingStatus, task.LastError = "settled", ""
	return nil
}

func observationTime(observation *MediaTaskObservation) time.Time {
	if observation != nil && observation.CompletedAt != nil {
		return *observation.CompletedAt
	}
	return time.Now()
}
