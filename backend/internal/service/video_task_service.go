package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/google/uuid"
)

// VideoTaskService 是独立视频任务协调器，生成请求只提交一次，恢复只查询和结算。
type VideoTaskService struct {
	repo         VideoTaskRepository
	upstream     VideoTaskTransport
	pricing      *ModelPricingResolver
	billing      UsageBillingRepository
	logs         UsageLogRepository
	accounts     AccountRepository
	rates        UserGroupRateRepository
	authCache    APIKeyAuthCacheInvalidator
	billingCache *BillingCacheService
	media        *MediaTaskService
	cfg          *config.Config
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	stop         sync.Once
	contentOnce  sync.Once
	contentHTTP  *http.Client
	contentSlots chan struct{}
}

func ProvideVideoTaskService(repo VideoTaskRepository, upstream *VideoUpstreamService, pricing *ModelPricingResolver,
	billing UsageBillingRepository, logs UsageLogRepository, accounts AccountRepository, rates UserGroupRateRepository,
	keys *APIKeyService, cache *BillingCacheService, media *MediaTaskService, cfg *config.Config) *VideoTaskService {
	s := &VideoTaskService{repo: repo, upstream: upstream, pricing: pricing, billing: billing, logs: logs, accounts: accounts, rates: rates, authCache: keys, billingCache: cache, media: media, cfg: cfg}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go s.run(ctx)
	return s
}

func (s *VideoTaskService) Stop() {
	s.stop.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
	s.wg.Wait()
}

func (s *VideoTaskService) run(ctx context.Context) {
	defer s.wg.Done()
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			tasks, err := s.repo.ClaimReady(ctx, 8, 3*time.Minute)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("video task recovery scan failed", "error", err)
				}
				continue
			}
			var group sync.WaitGroup
			for _, task := range tasks {
				group.Add(1)
				go func(t *VideoTaskRecord) {
					defer group.Done()
					work, done := context.WithTimeout(ctx, 90*time.Second)
					defer done()
					s.advance(work, t)
				}(task)
			}
			group.Wait()
		}
	}
}

func videoPayloadHash(req VideoTaskSubmitRequest) string {
	sum := sha256.Sum256(append([]byte(req.InboundProtocol+"\x00"+req.NativePath+"\x00"+req.ModelPath+"\x00"), req.Body...))
	return hex.EncodeToString(sum[:])
}

func videoCredentialNumber(account *Account, name string) float64 {
	if account == nil {
		return 0
	}
	data, err := json.Marshal(account.Credentials[name])
	if err != nil {
		return 0
	}
	var number float64
	if json.Unmarshal(data, &number) != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0
	}
	return number
}

func (s *VideoTaskService) Submit(ctx context.Context, key *APIKey, req VideoTaskSubmitRequest) (*VideoTaskResponse, error) {
	if key == nil || key.User == nil || key.Group == nil || key.Group.Platform != PlatformVideo {
		return nil, ErrVideoTaskNotFound
	}
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return nil, ErrVideoTaskMode
	}
	if len(req.IdempotencyKey) > 128 {
		return nil, ErrVideoTaskConflict
	}
	hash := videoPayloadHash(req)
	if req.IdempotencyKey != "" {
		old, err := s.repo.FindIdempotent(ctx, key.ID, req.IdempotencyKey)
		if err == nil {
			if old.PayloadHash != hash || old.UserID != usageActorUserID(key, key.User) {
				return nil, ErrVideoTaskConflict
			}
			if !videoStoredResponseMatchesTask(old) {
				return nil, errVideoTaskResponseMismatch
			}
			return videoCreateProtocolResponse(old, req), nil
		}
		if !errors.Is(err, ErrVideoTaskNotFound) {
			return nil, err
		}
	}
	var task *VideoTaskRecord
	var selection *VideoUpstreamSelection
	var created bool
	var err error
	// 仅在创建本地任务被账号待处理上限拒绝时重选；预留或付费 POST 开始后绝不重提。
	for {
		task, selection, created, err = s.prepareTask(ctx, key, req, hash)
		if !errors.Is(err, ErrVideoTaskBusy) || selection == nil || selection.Account == nil {
			break
		}
		if req.ExcludedAccountIDs == nil {
			req.ExcludedAccountIDs = make(map[int64]struct{})
		}
		if _, alreadyExcluded := req.ExcludedAccountIDs[selection.Account.ID]; alreadyExcluded {
			break
		}
		req.ExcludedAccountIDs[selection.Account.ID] = struct{}{}
	}
	if err != nil {
		return nil, err
	}
	if !created {
		if task.PayloadHash != hash || task.UserID != usageActorUserID(key, key.User) {
			return nil, ErrVideoTaskConflict
		}
		if !videoStoredResponseMatchesTask(task) {
			return nil, errVideoTaskResponseMismatch
		}
		return videoCreateProtocolResponse(task, req), nil
	}
	if selection.Release != nil {
		defer selection.Release()
	}
	return s.submitPreparedTask(ctx, req, task, selection)
}

// prepareTask 保存前只做选号和价格校验；失败和幂等命中都会立即释放账号槽。
func (s *VideoTaskService) prepareTask(ctx context.Context, key *APIKey, req VideoTaskSubmitRequest, hash string) (*VideoTaskRecord, *VideoUpstreamSelection, bool, error) {
	selection, err := s.upstream.SelectAccount(ctx, key, req)
	if err != nil {
		return nil, nil, false, err
	}
	keepSlot := false
	defer func() {
		if !keepSlot && selection.Release != nil {
			selection.Release()
		}
	}()
	quote, err := s.pricing.QuoteVideo(ctx, VideoPriceInput{Group: key.Group, Model: selection.BillingModel, Resolution: selection.Metadata.Resolution,
		HasReferenceVideo: selection.Metadata.HasReferenceVideo, ReferenceImageCount: selection.Metadata.ReferenceImageCount, RateMultiplier: 1})
	if err != nil {
		return nil, selection, false, ErrVideoTaskPricing.WithCause(err)
	}
	seconds := selection.Metadata.DurationSeconds
	var tokens *int64
	deferredBilling := false
	tokenPrepay := quote.Mode == BillingModeVideoToken && quote.TokenPrepay != nil
	if quote.Mode == BillingMode("video_token") {
		value := int64(0)
		// 新任务只按模型预扣开关建账，遗留账号 Token 上限不再参与；旧任务继续读取自身快照。
		deferredBilling = !tokenPrepay
		tokens = &value
	}
	if (quote.Mode == BillingModeVideo || tokenPrepay) && seconds <= 0 {
		seconds = videoCredentialNumber(selection.Account, "video_max_duration_seconds")
		if seconds <= 0 {
			return nil, selection, false, ErrVideoTaskBudget
		}
	}
	estimate, err := quote.Calculate(seconds, tokens)
	if err != nil {
		return nil, selection, false, err
	}
	// 未开启预扣时仍按零预留模式建账，图片固定费随最终视频费用在同一事务结算。
	fixedImageCost := estimate.ImageInputCost
	if deferredBilling {
		estimate = &CostBreakdown{}
	}
	prepaySeconds := 0.0
	if tokenPrepay {
		if quote.TokenPrepay.PricePerSecond == nil || seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return nil, selection, false, ErrVideoTaskBudget
		}
		// 预扣秒价是独立固定金额，不能叠乘渠道、分组、套餐或余额倍率。
		prepaySeconds = seconds
		estimate.OutputCost = seconds * *quote.TokenPrepay.PricePerSecond
		if estimate.OutputCost < 0 || math.IsNaN(estimate.OutputCost) || math.IsInf(estimate.OutputCost, 0) {
			return nil, selection, false, ErrVideoTaskBudget
		}
	}
	now := time.Now().UTC()
	t := &VideoTaskRecord{ID: "vid_" + strings.ReplaceAll(uuid.NewString(), "-", ""), UserID: usageActorUserID(key, key.User), APIKeyID: key.ID, GroupID: key.Group.ID, AccountID: selection.Account.ID,
		IdempotencyKey: req.IdempotencyKey, PayloadHash: hash, Status: "prepared", BillingStatus: "pending", Target: selection.Target, Metadata: selection.Metadata,
		RequestedModel: selection.RequestedModel, InternalModel: selection.InternalModel, Native: req.Native, InboundPath: req.NativePath, Quote: quote,
		CreatedAt: now, NextPollAt: now.Add(15 * time.Second), LeaseToken: uuid.NewString(), LeaseUntil: now.Add(3 * time.Minute), AccountRateMultiplier: selection.Account.BillingRateMultiplier()}
	if t.InboundPath == "" {
		t.InboundPath = "/v1/video/generations"
		if req.InboundProtocol == string(VideoEndpointOpenAIVideos) {
			t.InboundPath = "/v1/videos"
		}
	}
	if t.RequestedModel == "" {
		t.RequestedModel = selection.InternalModel
	}
	subRate, balanceRate := key.Group.RateMultiplier, key.Group.RateMultiplier
	if s.rates != nil {
		rate, e := s.rates.GetByUserAndGroup(ctx, key.User.ID, key.Group.ID)
		if e != nil {
			return nil, selection, false, e
		}
		if rate != nil {
			balanceRate = *rate
		}
	}
	if key.Group.VideoRateIndependent {
		subRate, balanceRate = key.Group.VideoRateMultiplier, key.Group.VideoRateMultiplier
	}
	t.Hold = BatchImageBalanceHoldCommand{RequestID: "video_hold:" + t.ID, APIKeyID: key.ID, UserID: key.User.ID, ActorUserID: t.UserID, TeamID: key.TeamID, GroupID: &t.GroupID,
		APIKeyBillingMode: APIKeyEffectiveBillingMode(key), PreferredSubscriptionID: key.PreferredSubscriptionID, BatchID: t.ID, PricingSnapshotVersion: 3,
		BaseAmountUSD: estimate.OutputCost, VideoFixedAmountUSD: fixedImageCost, SubscriptionRateMultiplier: subRate, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: balanceRate,
		SettlementRateScale: 1, DisablePlanGroupRateMultiplier: key.Group.VideoRateIndependent, ReservedAt: now, RequestPayloadHash: hash, VideoEntity: true, VideoAccountID: t.AccountID, VideoLeaseToken: t.LeaseToken,
		VideoDeferredBilling: deferredBilling, VideoTokenPrepay: tokenPrepay, VideoPrepayDurationSeconds: prepaySeconds}
	maxPending := int(videoCredentialNumber(selection.Account, "video_max_pending_tasks"))
	if maxPending <= 0 {
		maxPending = 10
	}
	existing, created, err := s.repo.Create(ctx, t, maxPending)
	if err != nil {
		return nil, selection, false, err
	}
	keepSlot = created
	return existing, selection, created, nil
}

// submitPreparedTask 从预算事务开始固定账号，任何受理不明均保留原任务交后台恢复。
func (s *VideoTaskService) submitPreparedTask(ctx context.Context, req VideoTaskSubmitRequest, t *VideoTaskRecord, selection *VideoUpstreamSelection) (*VideoTaskResponse, error) {
	if _, err := s.billing.ReserveBatchImageBalance(ctx, &t.Hold); err != nil {
		// 提交事务的网络错误可能发生在成功之后；先重读账务状态，不能直接宣称已释放。
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer done()
		if fresh, readErr := s.repo.Get(cleanup, t.ID); readErr == nil && fresh.LeaseToken == t.LeaseToken {
			t = fresh
			t.Status = "failed"
			t.ErrorMessage = "视频预算预留失败"
			if t.BillingStatus == "pending" {
				t.BillingStatus = "released"
			}
			if s.repo.Save(cleanup, t, false) == nil {
				s.finish(cleanup, t)
			}
			_ = s.repo.Save(cleanup, t, true)
		}
		return nil, err
	}
	s.invalidate(ctx, t)
	ownedToken := t.LeaseToken
	var err error
	t, err = s.repo.Get(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	// 预留较慢时恢复器可能已经接管；原请求不能借用新租约继续发起生成。
	if t.LeaseToken != ownedToken {
		return nil, ErrVideoTaskConflict
	}
	t.Status = "submitting"
	if err = s.repo.Save(ctx, t, false); err != nil {
		return nil, err
	}
	// 客户端断连不能中断已经开始的上游受理与归属保存，所有操作仍有独立截止时间。
	work, done := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer done()
	response, callErr := s.upstream.Submit(work, selection)
	if callErr != nil {
		t.Status = "submission_unknown"
		t.ErrorMessage = "上游受理结果未确认，请勿重新提交"
		t.BillingStatus = "reconciliation"
		t.NextPollAt = time.Now().Add(time.Hour)
		_ = s.repo.Save(work, t, true)
		_ = s.observe(work, t)
		result := videoProtocolResponse(t, req.InboundProtocol, false)
		result.StatusCode = http.StatusBadGateway
		return result, nil
	}
	s.applySubmissionResponse(t, response)
	if err = s.repo.Save(work, t, false); err != nil {
		return &VideoTaskResponse{StatusCode: http.StatusServiceUnavailable, LocalTaskID: t.ID, Body: []byte(fmt.Sprintf(`{"error":{"code":"video_task_storage_unavailable","message":"任务可能已受理，请勿重新提交","task_id":%q,"upstream_task_id":%q}}`, t.ID, t.UpstreamTaskID))}, nil
	}
	if !videoStoredResponseMatchesTask(t) {
		// 首次原生创建也不能绕过投影门禁直接回传冲突包，预算留给人工核对。
		t.NextPollAt = time.Now().Add(time.Hour)
		_ = s.repo.Save(work, t, true)
		_ = s.observe(work, t)
		return videoTaskMismatchResponse(t), nil
	}
	s.finish(work, t)
	t.NextPollAt = time.Now().Add(15 * time.Second)
	_ = s.repo.Save(work, t, true)
	if req.Native || response.StatusCode < 200 || response.StatusCode >= 300 {
		return &VideoTaskResponse{StatusCode: response.StatusCode, Header: response.Header, Body: response.Body, LocalTaskID: t.ID}, nil
	}
	return videoProtocolResponse(t, req.InboundProtocol, false), nil
}

// applySubmissionResponse 只处理创建响应，不能用查询失败触发退款。
// 创建收到错误响应且没有任务 ID 时由平台承担不确定成本，向用户释放预算；不自动重提。
func (s *VideoTaskService) applySubmissionResponse(t *VideoTaskRecord, response *VideoUpstreamResponse) {
	t.ResponseStatus = response.StatusCode
	t.RawResponse = append([]byte(nil), response.Body...)
	t.CreateResponse = append([]byte(nil), response.Body...)
	t.CreateResponseStatus = response.StatusCode
	if videoResponseHasConflictingTaskIDs(t.Target.Endpoint, response.Body) {
		// 多个矛盾的受理 ID 不能被当作无 ID 的错误退款，也不能任选一个任务自动结算。
		t.UpstreamTaskID = ""
		t.Status = "submission_unknown"
		t.BillingStatus = "reconciliation"
		t.ErrorMessage = "上游创建响应任务归属冲突，需要核对"
		return
	}
	if response.TaskID != "" {
		// 错误响应也可能携带已经受理的任务，必须保留其归属供原账号继续查询。
		t.UpstreamTaskID = response.TaskID
	}
	if !videoUpstreamResponseMatchesTask(t.Target.Endpoint, response, t.UpstreamTaskID) {
		// 替代传输返回的结构化 ID 与原包冲突时保留归属证据，但不得停在提交中或自动结算。
		t.Status = "submission_unknown"
		t.BillingStatus = "reconciliation"
		t.ErrorMessage = "上游创建响应任务归属冲突，需要核对"
		return
	}
	if response.Status == "failed" || response.Status == "cancelled" {
		s.applyObservation(t, response)
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 400 && response.StatusCode <= 599 && t.UpstreamTaskID == "" {
			failed := *response
			failed.Status = "failed"
			s.applyObservation(t, &failed)
			t.ErrorMessage = "上游创建请求返回错误且未提供任务 ID"
			return
		}
		t.Status = "submission_unknown"
		t.BillingStatus = "reconciliation"
		t.ErrorMessage = "上游创建响应异常，需要核对任务状态"
		return
	}
	if t.UpstreamTaskID == "" {
		t.Status = "submission_unknown"
		t.BillingStatus = "reconciliation"
		t.ErrorMessage = "上游响应缺少任务 ID，请勿重新提交"
		return
	}
	s.applyObservation(t, response)
}

// recoverSubmissionResponse 按保存的创建包修复旧版误留预算；绝不使用后续查询的错误响应。
func (s *VideoTaskService) recoverSubmissionResponse(ctx context.Context, t *VideoTaskRecord) bool {
	if t.UpstreamTaskID != "" || (t.Status != "submission_unknown" && t.Status != "submitting") || t.CreateResponseStatus == 0 {
		return false
	}
	response := ExtractVideoUpstreamResponse(t.Target.Endpoint, t.CreateResponse, "")
	response.StatusCode, response.Body = t.CreateResponseStatus, t.CreateResponse
	if response.TaskID == "" && response.Status != "failed" && response.Status != "cancelled" &&
		(response.StatusCode < 400 || response.StatusCode > 599) {
		return false
	}
	s.applySubmissionResponse(t, response)
	if err := s.repo.Save(ctx, t, false); err != nil {
		// 终态持久化成功之后才能调用资金事务；失败由下一次持有租约的恢复器重试。
		return true
	}
	if t.Status == "failed" || t.Status == "cancelled" {
		s.finish(ctx, t)
		return true
	}
	return false
}

// 已保存的响应也需要校验；升级前错误保存的原包不能通过无租约查询、幂等重放或预览再次泄漏。
func videoStoredResponseMatchesTask(t *VideoTaskRecord) bool {
	return t != nil && !videoResponseHasConflictingTaskIDs(t.Target.Endpoint, t.RawResponse) &&
		!videoResponseHasConflictingTaskIDs(t.Target.Endpoint, t.CreateResponse) &&
		videoResponseMatchesTask(t.Target.Endpoint, t.RawResponse, t.UpstreamTaskID) &&
		videoResponseMatchesTask(t.Target.Endpoint, t.CreateResponse, t.UpstreamTaskID)
}

// 响应投影的最后一道防线只返回固定错误，不回传供应商正文、产物或费用元数据。
func videoTaskMismatchResponse(t *VideoTaskRecord) *VideoTaskResponse {
	return &VideoTaskResponse{StatusCode: http.StatusBadGateway, LocalTaskID: t.ID,
		Body: []byte(`{"error":{"code":"VIDEO_TASK_RESPONSE_MISMATCH","message":"Upstream video task response does not match the requested task"}}`)}
}

// 幂等提交重放原创建响应，避免原生轮询包被误当成创建包。
func videoCreateResponse(t *VideoTaskRecord, native bool) *VideoTaskResponse {
	if !videoStoredResponseMatchesTask(t) {
		return videoTaskMismatchResponse(t)
	}
	if native && len(t.CreateResponse) > 0 {
		return &VideoTaskResponse{StatusCode: t.CreateResponseStatus, Body: t.CreateResponse, LocalTaskID: t.ID}
	}
	return videoTaskResponse(t, native)
}

func videoTaskResponse(t *VideoTaskRecord, native bool) *VideoTaskResponse {
	if !videoStoredResponseMatchesTask(t) {
		return videoTaskMismatchResponse(t)
	}
	if native && len(t.RawResponse) > 0 {
		return &VideoTaskResponse{StatusCode: t.ResponseStatus, Body: t.RawResponse, LocalTaskID: t.ID}
	}
	status := t.Status
	if status == "prepared" || status == "submitting" {
		status = "pending"
	}
	if status == "submission_unknown" {
		status = "unknown"
	}
	data := []map[string]string{}
	if t.Status == "completed" && safeMediaPreviewURL(t.VideoURL) {
		data = append(data, map[string]string{"url": t.VideoURL})
	}
	value := map[string]any{"id": t.ID, "task_id": t.ID, "object": "video.generation", "model": t.RequestedModel, "status": status, "billing_status": t.BillingStatus, "data": data, "created": t.CreatedAt.Unix()}
	if t.ErrorMessage != "" {
		value["message"] = t.ErrorMessage
	}
	if t.Metadata.Tokens != nil {
		value["usage"] = map[string]any{"completion_tokens": *t.Metadata.Tokens}
	}
	body, _ := json.Marshal(value)
	return &VideoTaskResponse{StatusCode: http.StatusOK, Body: body, LocalTaskID: t.ID}
}

func (s *VideoTaskService) Query(ctx context.Context, key *APIKey, id, protocol string) (*VideoTaskResponse, error) {
	if key == nil {
		return nil, ErrVideoTaskNotFound
	}
	t, err := s.repo.Find(ctx, key.ID, usageActorUserID(key, key.User), id, protocol)
	if err != nil {
		return nil, err
	}
	if !videoStoredResponseMatchesTask(t) {
		return nil, errVideoTaskResponseMismatch
	}
	if t.NextPollAt.Before(time.Now()) && !t.EffectsDone {
		claimed, e := s.repo.Claim(ctx, t.ID, 3*time.Minute)
		if e == nil {
			work, done := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
			s.advance(work, claimed)
			done()
			if updated, e := s.repo.Get(ctx, t.ID); e == nil {
				t = updated
			}
		}
	}
	if !videoStoredResponseMatchesTask(t) {
		return nil, errVideoTaskResponseMismatch
	}
	return videoProtocolResponse(t, protocol, videoNativeProtocol(protocol)), nil
}

func (s *VideoTaskService) Cancel(ctx context.Context, key *APIKey, id, protocol string) (*VideoTaskResponse, error) {
	if key == nil {
		return nil, ErrVideoTaskNotFound
	}
	t, err := s.repo.Find(ctx, key.ID, usageActorUserID(key, key.User), id, protocol)
	if err != nil {
		return nil, err
	}
	if !videoStoredResponseMatchesTask(t) {
		return nil, errVideoTaskResponseMismatch
	}
	if t.Status == "completed" || t.Status == "failed" || t.Status == "cancelled" {
		return nil, ErrVideoTaskConflict
	}
	t, err = s.repo.Claim(ctx, t.ID, 3*time.Minute)
	if err != nil {
		return nil, ErrVideoTaskConflict
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer done()
		_ = s.repo.Save(cleanup, t, true)
	}()
	if !videoStoredResponseMatchesTask(t) {
		return nil, errVideoTaskResponseMismatch
	}
	// Find 与 Claim 之间可能已有恢复器完成任务，领取租约后必须再次核对终态。
	if t.Status == "completed" || t.Status == "failed" || t.Status == "cancelled" || t.Status == "expired" {
		return nil, ErrVideoTaskConflict
	}
	if t.UpstreamTaskID == "" {
		return nil, ErrVideoTaskConflict
	}
	account, err := s.accounts.GetByID(ctx, t.AccountID)
	if err != nil {
		return nil, err
	}
	response, err := s.upstream.Cancel(ctx, account, t.Target, t.UpstreamTaskID)
	if err != nil {
		return nil, err
	}
	if !videoUpstreamResponseMatchesTask(t.Target.Endpoint, response, t.UpstreamTaskID) {
		return nil, errVideoTaskResponseMismatch
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		s.applyObservation(t, response)
		if err = s.repo.Save(ctx, t, false); err != nil {
			return nil, err
		}
		s.finish(ctx, t)
	}
	if videoNativeProtocol(protocol) {
		return &VideoTaskResponse{StatusCode: response.StatusCode, Header: response.Header, Body: response.Body, LocalTaskID: t.ID}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &VideoTaskResponse{StatusCode: response.StatusCode, Body: response.Body, LocalTaskID: t.ID}, nil
	}
	return videoTaskResponse(t, false), nil
}

func (s *VideoTaskService) advance(ctx context.Context, t *VideoTaskRecord) {
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer done()
		if err := s.repo.Save(cleanup, t, true); err != nil {
			slog.Error("video task state save failed", "task_id", t.ID, "error", err)
		}
	}()
	t.NextPollAt = time.Now().Add(30 * time.Second)
	if !videoStoredResponseMatchesTask(t) {
		// 不追溯修改历史费用，已知不匹配的旧观测也不能继续结算或生成列表投影。
		t.ErrorMessage = "上游任务响应归属不匹配，需要核对"
		t.NextPollAt = time.Now().Add(time.Hour)
		return
	}
	if t.BillingStatus == "settled" || t.BillingStatus == "released" {
		s.finish(ctx, t)
		return
	}
	if t.Status == "prepared" {
		t.Status = "failed"
		t.ErrorMessage = "视频提交前执行中断"
		if t.BillingStatus == "pending" {
			t.BillingStatus = "released"
		}
		if s.repo.Save(ctx, t, false) == nil {
			s.finish(ctx, t)
		}
		return
	}
	if t.Status == "failed" || t.Status == "cancelled" {
		s.finish(ctx, t)
		return
	}
	if t.Status == "expired" {
		t.BillingStatus = "reconciliation"
		t.ErrorMessage = "上游任务已过期，费用需要核对"
		t.NextPollAt = time.Now().Add(time.Hour)
		_ = s.observe(ctx, t)
		return
	}
	if s.recoverSubmissionResponse(ctx, t) {
		return
	}
	if t.UpstreamTaskID == "" {
		t.Status = "submission_unknown"
		t.BillingStatus = "reconciliation"
		t.ErrorMessage = "受理结果未知，需要核对上游任务"
		t.NextPollAt = time.Now().Add(time.Hour)
		_ = s.observe(ctx, t)
		return
	}
	if t.Status == "completed" && !t.PricingMismatch && (t.Metadata.Tokens != nil || t.Quote.Mode == BillingModeVideo || t.Quote.Mode == BillingModeVideoPerRequest || t.Quote.UnitPrice == 0) {
		s.finish(ctx, t)
		return
	}
	account, err := s.accounts.GetByID(ctx, t.AccountID)
	if err != nil {
		t.ErrorMessage = "任务账号暂不可读取"
		return
	}
	response, err := s.upstream.Poll(ctx, account, t.Target, t.UpstreamTaskID)
	t.PollAttempts++
	if err != nil || !videoUpstreamResponseMatchesTask(t.Target.Endpoint, response, t.UpstreamTaskID) || response.StatusCode < 200 || response.StatusCode >= 300 {
		// HTTP查询失败不代表视频生成失败，更不能触发退款或重新生成。
		t.ErrorMessage = "上游任务状态查询暂不可用"
		t.NextPollAt = time.Now().Add(time.Duration(min(300, 15+t.PollAttempts*5)) * time.Second)
		_ = s.observe(ctx, t)
		return
	}
	s.applyObservation(t, response)
	if err = s.repo.Save(ctx, t, false); err != nil {
		return
	}
	s.finish(ctx, t)
}

func (s *VideoTaskService) applyObservation(t *VideoTaskRecord, r *VideoUpstreamResponse) {
	if !videoUpstreamResponseMatchesTask(t.Target.Endpoint, r, t.UpstreamTaskID) {
		return
	}
	// 生成终态不可被迟到的排队状态回退；用量和产物可以在成功后继续补齐。
	terminal := t.Status == "completed" || t.Status == "failed" || t.Status == "cancelled"
	if terminal && r.Status != t.Status {
		return
	}
	if !terminal && r.Status != "" {
		t.Status = r.Status
	}
	if t.Status == "submitting" {
		t.Status = "queued"
	}
	t.UpstreamStatus = r.UpstreamStatus
	t.ResponseStatus = r.StatusCode
	t.RawResponse = append([]byte(nil), r.Body...)
	// 排队包里的零通常是占位值，只有完成响应的显式用量才可用于结算。
	if r.Status == "completed" && r.Metadata.Tokens != nil {
		t.Metadata.Tokens = r.Metadata.Tokens
	}
	if r.Status == "completed" && r.Metadata.DurationSeconds > 0 {
		t.Metadata.DurationSeconds = r.Metadata.DurationSeconds
	}
	if r.Status == "completed" && r.Metadata.Resolution != "" {
		resolution, err := NormalizeVideoPriceResolution(r.Metadata.Resolution)
		if t.Quote.Mode == BillingModeVideoPerRequest && t.Quote.Resolution == "" && t.Quote.FallbackUsed {
			// 统一按次价没有分辨率条件，完成时的真实档位只用于展示，不重新读取或改写价卡。
			t.PricingMismatch = err != nil
			if err == nil {
				t.Metadata.Resolution = resolution
			}
		} else {
			t.PricingMismatch = err != nil || resolution != t.Quote.Resolution
		}
	}
	if r.VideoURL != "" && safeMediaPreviewURL(r.VideoURL) {
		t.VideoURL = r.VideoURL
	}
	if (t.Status == "completed" || t.Status == "failed" || t.Status == "cancelled") && t.CompletedAt == nil {
		now := time.Now().UTC()
		t.CompletedAt = &now
	}
	t.ErrorMessage = ""
	if t.Status == "failed" {
		t.ErrorMessage = "上游视频生成失败"
	}
}

func (s *VideoTaskService) finish(ctx context.Context, t *VideoTaskRecord) {
	if t.BillingStatus != "settled" && t.BillingStatus != "released" {
		command := t.Hold
		command.RequestFingerprint = ""
		command.VideoAccountID = t.AccountID
		var err error
		switch t.Status {
		case "completed":
			cost, e := videoTaskCost(t)
			if e != nil {
				t.BillingStatus = "reconciliation"
				t.ErrorMessage = "视频已完成，等待可靠用量结算"
				_ = s.observe(ctx, t)
				return
			}
			command.RequestID = "video_capture:" + t.ID
			command.ActualBaseAmountUSD = cost.OutputCost
			command.VideoActualFixedAmountUSD = cost.ImageInputCost
			// 账号成本属于独立的上游统计口径，保持与现有报表、账号配额计算一致；不参与用户收费。
			command.VideoAccountQuotaCost = cost.TotalCost * t.AccountRateMultiplier
			_, err = s.billing.CaptureBatchImageBalance(ctx, &command)
		case "failed", "cancelled":
			command.RequestID = "video_release:" + t.ID
			_, err = s.billing.ReleaseBatchImageBalance(ctx, &command)
		default:
			_ = s.observe(ctx, t)
			return
		}
		if err != nil {
			t.BillingStatus = "reconciliation"
			t.ErrorMessage = "视频任务费用待对账"
			slog.Error("video task settlement pending", "task_id", t.ID, "error", err)
			_ = s.observe(ctx, t)
			return
		}
		fresh, e := s.repo.Get(ctx, t.ID)
		if e != nil {
			return
		}
		t.Hold = fresh.Hold
		t.BillingResult = fresh.BillingResult
		t.BillingStatus = fresh.BillingStatus
		s.invalidate(ctx, t)
	}
	if t.BillingStatus == "settled" {
		// 完成任务补结算成功后清理临时对账错误，失败或取消任务的原始错误继续保留。
		if t.Status == "completed" {
			t.ErrorMessage = ""
		}
		if err := s.writeUsage(ctx, t); err != nil {
			slog.Error("video task usage log pending", "task_id", t.ID, "error", err)
			return
		}
	}
	if err := s.observe(ctx, t); err != nil {
		return
	}
	t.EffectsDone = true
}

func (s *VideoTaskService) invalidate(ctx context.Context, t *VideoTaskRecord) {
	if s.authCache != nil {
		s.authCache.InvalidateAuthCacheByUserID(ctx, t.Hold.UserID)
	}
	if s.billingCache != nil {
		_ = s.billingCache.InvalidateUserBalance(ctx, t.Hold.UserID)
		_ = s.billingCache.InvalidateAPIKeyRateLimit(ctx, t.APIKeyID)
	}
}

func (s *VideoTaskService) observe(ctx context.Context, t *VideoTaskRecord) error {
	if s.media == nil {
		return nil
	}
	status := t.Status
	if status == "prepared" || status == "submitting" {
		status = "queued"
	}
	if status == "submission_unknown" {
		status = "processing"
	}
	o := MediaTaskObservation{Source: "video", TaskID: t.ID, MediaType: "video", Platform: PlatformVideo, Model: t.RequestedModel, Status: status, UpstreamStatus: t.UpstreamStatus,
		UserID: t.UserID, APIKeyID: t.APIKeyID, GroupID: &t.GroupID, AccountID: &t.AccountID, HTTPStatus: t.ResponseStatus, ErrorMessage: t.ErrorMessage,
		RequestID: "video_capture:" + t.ID, CreatedAt: t.CreatedAt, CompletedAt: t.CompletedAt}
	if err := s.media.ObserveMediaTask(ctx, o); err != nil {
		return err
	}
	if t.VideoURL != "" {
		if t.CompletedAt != nil {
			expires := t.CompletedAt.Add(24 * time.Hour)
			o.ExpiresAt = &expires
		}
		_ = s.media.ObserveMediaTaskVideoPreview(ctx, o, &MediaTaskVideoSnapshot{URL: t.VideoURL, DurationSeconds: t.Metadata.DurationSeconds, MimeType: "video/mp4"})
	}
	return nil
}

func (s *VideoTaskService) writeUsage(ctx context.Context, t *VideoTaskRecord) error {
	if s.logs == nil || t.BillingResult == nil {
		return errors.New("video usage repository or settlement result missing")
	}
	cost, err := videoTaskCost(t)
	if err != nil {
		return err
	}
	mode := string(t.Quote.Mode)
	media := "video"
	var duration *int
	if t.Metadata.DurationSeconds > 0 {
		seconds := int(math.Ceil(t.Metadata.DurationSeconds))
		duration = &seconds
	}
	// 视频计价档位仅由分辨率决定，参考素材仍保留在任务输入元数据中。
	tier := t.Metadata.Resolution
	finishedAt := time.Now()
	if t.CompletedAt != nil {
		finishedAt = *t.CompletedAt
	}
	// 长期停机后的恢复耗时可能超出旧日志字段，遥测缺失不能阻断已经完成的结算。
	var elapsed *int
	if milliseconds := finishedAt.Sub(t.CreatedAt).Milliseconds(); milliseconds >= 0 && milliseconds <= math.MaxInt32 {
		value := int(milliseconds)
		elapsed = &value
	}
	rate := t.Hold.BalanceRateMultiplier
	if cost.OutputCost > 0 {
		// 展示视频部分的有效倍率，固定图片费不能被混入倍率造成虚假折扣。
		rate = math.Max(0, t.BillingResult.ActualAmountUSD-cost.ImageInputCost) / cost.OutputCost
	}
	log := &UsageLog{UserID: t.UserID, BillingUserID: t.Hold.UserID, TeamID: t.Hold.TeamID, APIKeyID: t.APIKeyID, AccountID: t.AccountID, GroupID: &t.GroupID,
		RequestID: "video_capture:" + t.ID, Model: t.InternalModel, RequestedModel: t.RequestedModel, UpstreamModel: &t.Target.Model,
		BillingMode: &mode, BillingTier: &tier, MediaType: &media, VideoCount: 1, VideoResolution: &t.Metadata.Resolution, VideoDurationSeconds: duration,
		OutputCost: cost.OutputCost, ImageInputCost: cost.ImageInputCost, TotalCost: cost.TotalCost, ActualCost: t.BillingResult.ActualAmountUSD,
		SubscriptionAmountUSD: t.BillingResult.SubscriptionAmountUSD, BalanceAmountUSD: t.BillingResult.BalanceAmountUSD,
		BillingAllocations: t.BillingResult.BillingAllocations, RateMultiplier: rate, AccountRateMultiplier: &t.AccountRateMultiplier,
		InboundEndpoint: &t.InboundPath, UpstreamEndpoint: &t.Target.CreatePath, DurationMs: elapsed, CreatedAt: t.CreatedAt}
	if t.Metadata.Tokens != nil {
		log.OutputTokens = int(*t.Metadata.Tokens)
	}
	if t.BillingResult.SubscriptionAmountUSD > 0 {
		log.BillingType = 1
		for _, a := range t.BillingResult.BillingAllocations {
			// 免费覆盖只用于固定原视频预算，不应被展示成实际支付图片费的套餐。
			if a.SubscriptionID != nil && a.AmountUSD > 0 {
				log.SubscriptionID = a.SubscriptionID
				break
			}
		}
	}
	_, err = s.logs.Create(ctx, log)
	return err
}

// 只有明确的零价可在供应商不返回用量时结算；缺价与缺用量不得按免费处理。
func videoTaskCost(t *VideoTaskRecord) (*CostBreakdown, error) {
	if t.Quote == nil || t.PricingMismatch {
		return nil, ErrVideoUsageUnavailable
	}
	// 使用记录沿用现有 INT32 Token 字段，异常上游用量先对账，避免扣费后无法落账。
	if t.Metadata.Tokens != nil && (*t.Metadata.Tokens < 0 || *t.Metadata.Tokens > math.MaxInt32) {
		return nil, ErrVideoUsageUnavailable
	}
	if math.IsNaN(t.Metadata.DurationSeconds) || math.IsInf(t.Metadata.DurationSeconds, 0) || t.Metadata.DurationSeconds > math.MaxInt32 {
		return nil, ErrVideoUsageUnavailable
	}
	if t.Quote.UnitPrice == 0 {
		// 零视频价无需上游用量，但明确配置的固定图片费仍须计算。
		zero := int64(0)
		return t.Quote.Calculate(1, &zero)
	}
	return t.Quote.Calculate(t.Metadata.DurationSeconds, t.Metadata.Tokens)
}

// 统一入口及 compat 都使用本地任务 ID；openai_videos 仅采用不同的客户端响应形状。
func videoNativeProtocol(protocol string) bool {
	return protocol != "" && protocol != "compat" && protocol != "unified" && protocol != string(VideoEndpointOpenAIVideos)
}
