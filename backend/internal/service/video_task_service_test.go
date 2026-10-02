package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/stretchr/testify/require"
)

// 生命周期夹具通过 JSON 往返模拟进程重启后的读写，避免指针共享掩盖持久化遗漏。
type videoLifecycleRepo struct {
	mu    sync.Mutex
	tasks map[string]*VideoTaskRecord
}

// 免费订阅覆盖视频时，旧单值套餐字段仍应指向真正承担图片费用的订阅。
func TestVideoTaskImageFeeUsageSelectsPayingSubscription(t *testing.T) {
	s, _, _, _, logs := newVideoLifecycleFixture(BillingModeVideo)
	count, imagePrice, freeID, paidID := 1, .5, int64(8), int64(9)
	task := &VideoTaskRecord{ID: "fixed_subscription", CreatedAt: time.Now(),
		Metadata: VideoRequestMetadata{DurationSeconds: 1, Resolution: "720p"},
		Quote: &VideoPriceQuote{Version: 1, Mode: BillingModeVideo, UnitPrice: 1, ReferenceImageCount: &count,
			ImageInputPricing: &VideoImageInputPricing{Price: &imagePrice}},
		BillingResult: &BatchImageBalanceHoldResult{ActualAmountUSD: .5, SubscriptionAmountUSD: .5, BillingAllocations: []domain.BillingAllocation{
			{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &freeID, BaseAmountUSD: 1},
			{Type: domain.BillingAllocationTypeSubscription, SubscriptionID: &paidID, AmountUSD: .5, Component: VideoImageInputBillingComponent},
		}}}
	require.NoError(t, s.writeUsage(context.Background(), task))
	log := logs.logs["video_capture:"+task.ID]
	require.Equal(t, paidID, *log.SubscriptionID)
	require.Zero(t, log.RateMultiplier)
	require.Equal(t, .5, log.ActualCost)
	require.Equal(t, .5, log.ImageInputCost)
}

func cloneVideoLifecycleTask(t *VideoTaskRecord) *VideoTaskRecord {
	if t == nil {
		return nil
	}
	raw, _ := json.Marshal(t)
	var v VideoTaskRecord
	_ = json.Unmarshal(raw, &v)
	return &v
}
func (r *videoLifecycleRepo) Get(_ context.Context, id string) (*VideoTaskRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.tasks[id]
	if v == nil {
		return nil, ErrVideoTaskNotFound
	}
	return cloneVideoLifecycleTask(v), nil
}
func (r *videoLifecycleRepo) Find(ctx context.Context, key, user int64, id, protocol string) (*VideoTaskRecord, error) {
	v, e := r.Get(ctx, id)
	if e != nil {
		return nil, e
	}
	if v.APIKeyID != key || v.UserID != user {
		return nil, ErrVideoTaskNotFound
	}
	return v, nil
}
func (r *videoLifecycleRepo) FindIdempotent(_ context.Context, key int64, idem string) (*VideoTaskRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.tasks {
		if v.APIKeyID == key && v.IdempotencyKey == idem {
			return cloneVideoLifecycleTask(v), nil
		}
	}
	return nil, ErrVideoTaskNotFound
}
func (r *videoLifecycleRepo) Create(_ context.Context, t *VideoTaskRecord, limit int) (*VideoTaskRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.tasks {
		if t.IdempotencyKey != "" && v.APIKeyID == t.APIKeyID && v.IdempotencyKey == t.IdempotencyKey {
			return cloneVideoLifecycleTask(v), false, nil
		}
	}
	r.tasks[t.ID] = cloneVideoLifecycleTask(t)
	return cloneVideoLifecycleTask(t), true, nil
}
func (r *videoLifecycleRepo) Claim(ctx context.Context, id string, _ time.Duration) (*VideoTaskRecord, error) {
	return r.Get(ctx, id)
}
func (r *videoLifecycleRepo) ClaimReady(context.Context, int, time.Duration) ([]*VideoTaskRecord, error) {
	return nil, nil
}
func (r *videoLifecycleRepo) Save(_ context.Context, t *VideoTaskRecord, _ bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := cloneVideoLifecycleTask(t)
	if old := r.tasks[t.ID]; old != nil {
		if old.BillingStatus == "settled" || old.BillingStatus == "released" {
			v.BillingStatus = old.BillingStatus
		}
		v.Hold = old.Hold
		v.BillingResult = old.BillingResult
	}
	r.tasks[t.ID] = v
	return nil
}

type videoLifecycleBilling struct {
	UsageBillingRepository
	repo                         *videoLifecycleRepo
	reserves, captures, releases int
	reserveErr                   error
	releaseErr                   error
	captureErr                   error
	captureCommands              []BatchImageBalanceHoldCommand
	committedError               bool
}

func (b *videoLifecycleBilling) ReserveBatchImageBalance(_ context.Context, c *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	b.repo.mu.Lock()
	defer b.repo.mu.Unlock()
	if b.reserveErr != nil && !b.committedError {
		return nil, b.reserveErr
	}
	v := b.repo.tasks[c.BatchID]
	v.Hold = *c
	v.Hold.HoldAmount = 0
	if !c.VideoDeferredBilling {
		v.Hold.HoldAmount = c.BaseAmountUSD*c.BalanceRateMultiplier + c.VideoFixedAmountUSD
	}
	if c.VideoTokenPrepay {
		v.Hold.HoldAmount = c.BaseAmountUSD + c.VideoFixedAmountUSD
	}
	v.Hold.BalanceHoldAmount = v.Hold.HoldAmount
	v.Hold.AllowanceReserved = !c.VideoDeferredBilling
	v.BillingStatus = "reserved"
	b.reserves++
	return &BatchImageBalanceHoldResult{Applied: true}, b.reserveErr
}
func (b *videoLifecycleBilling) CaptureBatchImageBalance(_ context.Context, c *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	b.repo.mu.Lock()
	defer b.repo.mu.Unlock()
	v := b.repo.tasks[c.BatchID]
	if v.BillingStatus == "settled" {
		return v.BillingResult, nil
	}
	b.captureCommands = append(b.captureCommands, *c)
	if b.captureErr != nil {
		return nil, b.captureErr
	}
	if c.VideoDeferredBilling || c.VideoTokenPrepay {
		// 夹具模拟余额真实结算，预扣的退补差与配额原子检查由仓储集成测试覆盖。
		if v.Hold.VideoDeferredBilling != c.VideoDeferredBilling || v.Hold.VideoTokenPrepay != c.VideoTokenPrepay || !c.VideoEntity {
			return nil, ErrVideoTaskConflict
		}
		amount := c.ActualBaseAmountUSD*c.BalanceRateMultiplier*c.SettlementRateScale + c.VideoActualFixedAmountUSD
		b.captures++
		v.BillingStatus = "settled"
		v.BillingResult = &BatchImageBalanceHoldResult{Applied: true, ActualAmountUSD: amount, BalanceAmountUSD: amount}
		return v.BillingResult, nil
	}
	plan, err := PlanBatchImageBillingCapture(c)
	if err != nil {
		return nil, err
	}
	b.captures++
	v.BillingStatus = "settled"
	v.BillingResult = &BatchImageBalanceHoldResult{Applied: true, ActualAmountUSD: plan.ActualAmountUSD, BalanceAmountUSD: plan.BalanceAmountUSD}
	return v.BillingResult, nil
}
func (b *videoLifecycleBilling) ReleaseBatchImageBalance(_ context.Context, c *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	b.repo.mu.Lock()
	defer b.repo.mu.Unlock()
	if b.releaseErr != nil {
		return nil, b.releaseErr
	}
	v := b.repo.tasks[c.BatchID]
	if v.BillingStatus != "released" {
		b.releases++
	}
	v.BillingStatus = "released"
	v.BillingResult = &BatchImageBalanceHoldResult{Applied: true}
	return v.BillingResult, nil
}

type videoLifecycleUpstream struct {
	selection                 *VideoUpstreamSelection
	submitted, polls, cancels int
	submit, poll, cancel      *VideoUpstreamResponse
	submitErr, pollErr        error
	lastTarget                VideoUpstreamTarget
}

func (u *videoLifecycleUpstream) SelectAccount(context.Context, *APIKey, VideoTaskSubmitRequest) (*VideoUpstreamSelection, error) {
	return u.selection, nil
}
func (u *videoLifecycleUpstream) Submit(context.Context, *VideoUpstreamSelection) (*VideoUpstreamResponse, error) {
	u.submitted++
	return u.submit, u.submitErr
}
func (u *videoLifecycleUpstream) Poll(_ context.Context, _ *Account, target VideoUpstreamTarget, _ string) (*VideoUpstreamResponse, error) {
	u.polls++
	u.lastTarget = target
	return u.poll, u.pollErr
}
func (u *videoLifecycleUpstream) Cancel(context.Context, *Account, VideoUpstreamTarget, string) (*VideoUpstreamResponse, error) {
	u.cancels++
	return u.cancel, nil
}

type videoLifecycleAccounts struct {
	AccountRepository
	account *Account
}

func (r videoLifecycleAccounts) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

type videoLifecycleLogs struct {
	UsageLogRepository
	logs map[string]*UsageLog
	err  error
}

func (l *videoLifecycleLogs) Create(_ context.Context, v *UsageLog) (bool, error) {
	if l.err != nil {
		return false, l.err
	}
	_, exists := l.logs[v.RequestID]
	l.logs[v.RequestID] = v
	return !exists, nil
}

func newVideoLifecycleFixture(mode BillingMode) (*VideoTaskService, *APIKey, *videoLifecycleUpstream, *videoLifecycleBilling, *videoLifecycleLogs) {
	price := 1.0
	group := &Group{ID: 3, Platform: PlatformVideo, RateMultiplier: 2, ModelPricing: []ChannelModelPricing{{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: mode, VideoPrices: []VideoPriceTier{{Resolution: "720p", Price: &price}}}}}
	key := &APIKey{ID: 2, UserID: 1, User: &User{ID: 1}, GroupID: &group.ID, Group: group, BillingMode: APIKeyBillingModeBalance}
	account := &Account{ID: 4, Platform: PlatformVideo, Type: AccountTypeAPIKey, Credentials: map[string]any{"video_max_output_tokens": 1_000_000}}
	repo := &videoLifecycleRepo{tasks: map[string]*VideoTaskRecord{}}
	billing := &videoLifecycleBilling{repo: repo}
	logs := &videoLifecycleLogs{logs: map[string]*UsageLog{}}
	upstream := &videoLifecycleUpstream{selection: &VideoUpstreamSelection{Account: account, InternalModel: "video-model", BillingModel: "video-model", Target: VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointCompat, BaseURL: "https://video.example.com", Model: "video-model", AccountID: account.ID}, Metadata: VideoRequestMetadata{Model: "video-model", Resolution: "720p", DurationSeconds: 8}}, submit: &VideoUpstreamResponse{StatusCode: 200, TaskID: "upstream-task", Status: "queued", Body: []byte(`{"id":"upstream-task","status":"pending"}`)}}
	s := &VideoTaskService{repo: repo, upstream: upstream, billing: billing, pricing: NewModelPricingResolver(nil, nil), accounts: videoLifecycleAccounts{account: account}, logs: logs}
	return s, key, upstream, billing, logs
}

func TestVideoTaskLifecycleRestartSettlesOnceAndRetainsOwner(t *testing.T) {
	s, key, u, b, logs := newVideoLifecycleFixture(BillingModeVideoToken)
	req := VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","resolution":"720p"}`), IdempotencyKey: "request-one"}
	created, err := s.Submit(context.Background(), key, req)
	require.NoError(t, err)
	id := created.LocalTaskID
	require.Equal(t, 1, b.reserves)
	require.Equal(t, 1, u.submitted)
	// 新协调器只有数据库快照，不能再次发送生成请求。
	restarted := *s
	count := int64(200_000)
	u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Metadata: VideoRequestMetadata{Tokens: &count, DurationSeconds: 8}, VideoURL: "https://cdn.example.com/video.mp4", Body: []byte(`{"status":"completed"}`)}
	task, err := s.repo.Get(context.Background(), id)
	require.NoError(t, err)
	restarted.advance(context.Background(), task)
	fresh, err := s.repo.Get(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "settled", fresh.BillingStatus)
	require.True(t, fresh.EffectsDone)
	require.Equal(t, .4, fresh.BillingResult.ActualAmountUSD)
	require.Equal(t, 1, b.captures)
	require.Equal(t, u.selection.Target, u.lastTarget)
	query, err := s.Query(context.Background(), key, id, "unified")
	require.NoError(t, err)
	var queried map[string]any
	require.NoError(t, json.Unmarshal(query.Body, &queried))
	require.Equal(t, id, queried["id"])
	_, err = s.Submit(context.Background(), key, req)
	require.NoError(t, err)
	require.Equal(t, 1, u.submitted)
	restarted.advance(context.Background(), fresh)
	require.Equal(t, 1, b.captures)
	require.Len(t, logs.logs, 1)
	other := *key
	other.ID++
	_, err = s.Query(context.Background(), &other, id, "compat")
	require.ErrorIs(t, err, ErrVideoTaskNotFound)
	req.Body = []byte(`{"model":"changed"}`)
	_, err = s.Submit(context.Background(), key, req)
	require.ErrorIs(t, err, ErrVideoTaskConflict)
}

func TestVideoTaskLifecycleUnknownSubmissionNeverRepostsOrRefunds(t *testing.T) {
	for _, network := range []bool{false, true} {
		t.Run(map[bool]string{false: "success_missing_id", true: "connection_lost"}[network], func(t *testing.T) {
			s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideo)
			if network {
				u.submitErr = errors.New("connection lost after write")
			} else {
				u.submit = &VideoUpstreamResponse{StatusCode: 200, Body: []byte(`{"status":"pending"}`)}
			}
			response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.NoError(t, err)
			if network {
				require.Equal(t, 502, response.StatusCode)
			} else {
				require.Equal(t, 200, response.StatusCode)
			}
			task, err := s.repo.Get(context.Background(), response.LocalTaskID)
			require.NoError(t, err)
			s.advance(context.Background(), task)
			require.Equal(t, "submission_unknown", task.Status)
			require.Equal(t, "reconciliation", task.BillingStatus)
			require.Equal(t, 1, u.submitted)
			require.Zero(t, u.polls)
			require.Zero(t, b.releases)
			require.Zero(t, b.captures)
			_, err = json.Marshal(task)
			require.NoError(t, err)
		})
	}
}

func TestVideoTaskLifecycleMissingUsageThenRecovery(t *testing.T) {
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoToken)
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, _ := s.repo.Get(context.Background(), response.LocalTaskID)
	u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Body: []byte(`{"status":"completed"}`)}
	s.advance(context.Background(), task)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.Zero(t, b.captures)
	count := int64(100_000)
	u.poll.Metadata.Tokens = &count
	s.advance(context.Background(), task)
	require.Equal(t, "settled", task.BillingStatus)
	require.Equal(t, 1, b.captures)
	// 迟到的 queued 不能覆盖已经完成的结果。
	s.applyObservation(task, &VideoUpstreamResponse{Status: "queued"})
	require.Equal(t, "completed", task.Status)
}

// 中间状态的零用量不能在完成响应缺失用量时变成免费结算。
func TestVideoTaskLifecycleIgnoresPlaceholderUsage(t *testing.T) {
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideoToken)
	zero := int64(0)
	u.submit.Metadata.Tokens = &zero
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, _ := s.repo.Get(context.Background(), response.LocalTaskID)
	require.Nil(t, task.Metadata.Tokens)
	u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Body: []byte(`{"status":"completed"}`)}
	s.advance(context.Background(), task)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.Zero(t, b.captures)
	s.applyObservation(task, &VideoUpstreamResponse{Status: "processing", Metadata: VideoRequestMetadata{Tokens: &zero}})
	require.Nil(t, task.Metadata.Tokens)
	u.poll.Metadata.Tokens = &zero
	s.advance(context.Background(), task)
	require.Equal(t, "settled", task.BillingStatus)
	require.Equal(t, 1, b.captures)
}

func TestVideoTaskLifecyclePollFailureAndCancellationConfirmation(t *testing.T) {
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideo)
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, _ := s.repo.Get(context.Background(), response.LocalTaskID)
	u.poll = &VideoUpstreamResponse{StatusCode: 503}
	s.advance(context.Background(), task)
	require.Zero(t, b.releases)
	require.Equal(t, "queued", task.Status)
	u.cancel = &VideoUpstreamResponse{StatusCode: 200, Status: "processing", Body: []byte(`{"status":"processing"}`)}
	_, err = s.Cancel(context.Background(), key, task.ID, "compat")
	require.NoError(t, err)
	require.Zero(t, b.releases)
	u.cancel.Status = "cancelled"
	_, err = s.Cancel(context.Background(), key, task.ID, "compat")
	require.NoError(t, err)
	require.Equal(t, 1, b.releases)
	fresh, _ := s.repo.Get(context.Background(), task.ID)
	s.advance(context.Background(), fresh)
	require.Equal(t, 1, b.releases)
}

func TestVideoTaskLifecycleRejectAndAmbiguousReserveDoNotLeakHold(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "committed_but_network_failed"}[committed], func(t *testing.T) {
			s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideo)
			b.reserveErr = errors.New("reserve failure")
			b.committedError = committed
			_, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.Error(t, err)
			require.Zero(t, u.submitted)
			for _, task := range b.repo.tasks {
				require.Equal(t, "released", task.BillingStatus)
			}
			if committed {
				require.Equal(t, 1, b.releases)
			} else {
				require.Zero(t, b.releases)
			}
		})
	}
	s, key, u, b, _ := newVideoLifecycleFixture(BillingModeVideo)
	u.submit = &VideoUpstreamResponse{StatusCode: http.StatusBadRequest, Body: []byte(`{"error":"invalid model"}`)}
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	require.Equal(t, 400, response.StatusCode)
	require.Equal(t, 1, b.releases)
}

func TestVideoTaskLifecycleUsageLogFailureRetriesWithoutCharging(t *testing.T) {
	s, key, u, b, logs := newVideoLifecycleFixture(BillingModeVideo)
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, _ := s.repo.Get(context.Background(), response.LocalTaskID)
	u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Body: []byte(`{"status":"completed"}`)}
	logs.err = errors.New("temporary database issue")
	s.advance(context.Background(), task)
	require.Equal(t, 1, b.captures)
	require.False(t, task.EffectsDone)
	logs.err = nil
	s.advance(context.Background(), task)
	require.Equal(t, 1, b.captures)
	require.True(t, task.EffectsDone)
	require.Len(t, logs.logs, 1)
}

func TestVideoTaskCostRejectsResolutionMismatchAndExemptsExplicitZero(t *testing.T) {
	task := &VideoTaskRecord{Quote: &VideoPriceQuote{Version: 1, Mode: BillingModeVideoToken, UnitPrice: 1, RateMultiplier: 1}, Metadata: VideoRequestMetadata{Resolution: "720p"}}
	_, err := videoTaskCost(task)
	require.ErrorIs(t, err, ErrVideoUsageUnavailable)
	task.Quote.UnitPrice = 0
	cost, err := videoTaskCost(task)
	require.NoError(t, err)
	require.Zero(t, cost.TotalCost)
	task.PricingMismatch = true
	_, err = videoTaskCost(task)
	require.ErrorIs(t, err, ErrVideoUsageUnavailable)
}

// 超出既有用量存储范围时不能先扣费，零价任务也必须能够记录真实用量。
func TestVideoTaskCostRejectsUnrecordableUsage(t *testing.T) {
	for _, tokens := range []int64{-1, 1 << 31} {
		for _, price := range []float64{0, 1} {
			task := &VideoTaskRecord{Quote: &VideoPriceQuote{Mode: BillingModeVideoToken, UnitPrice: price}, Metadata: VideoRequestMetadata{Tokens: &tokens}}
			_, err := videoTaskCost(task)
			require.ErrorIs(t, err, ErrVideoUsageUnavailable)
		}
	}
	for _, seconds := range []float64{1 << 31, math.Inf(1), math.NaN()} {
		task := &VideoTaskRecord{Quote: &VideoPriceQuote{Mode: BillingModeVideo, UnitPrice: 0}, Metadata: VideoRequestMetadata{DurationSeconds: seconds}}
		_, err := videoTaskCost(task)
		require.ErrorIs(t, err, ErrVideoUsageUnavailable)
	}
}

// 多周停机后继续恢复，遥测耗时不能溢出旧日志字段并阻断任务收尾。
func TestVideoTaskLifecycleLongRecoveryOmitsOverflowingLatency(t *testing.T) {
	s, key, u, b, logs := newVideoLifecycleFixture(BillingModeVideo)
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	task.CreatedAt = time.Now().Add(-60 * 24 * time.Hour)
	u.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Body: []byte(`{"status":"completed"}`)}
	s.advance(context.Background(), task)
	require.Equal(t, 1, b.captures)
	require.True(t, task.EffectsDone)
	log := logs.logs["video_capture:"+task.ID]
	require.NotNil(t, log)
	require.Nil(t, log.DurationMs)
	require.Equal(t, 8, *log.VideoDurationSeconds)
}

// 数据库待处理上限与短期 HTTP 并发槽独立，未提交的满账号可以让位给其它账号。
type videoPendingLimitRepo struct {
	VideoTaskRepository
	full map[int64]bool
}

func (r videoPendingLimitRepo) Create(ctx context.Context, task *VideoTaskRecord, limit int) (*VideoTaskRecord, bool, error) {
	if r.full[task.AccountID] {
		return nil, false, ErrVideoTaskBusy
	}
	return r.VideoTaskRepository.Create(ctx, task, limit)
}

type videoPendingLimitUpstream struct {
	*videoLifecycleUpstream
	accounts []*Account
	selected []int64
	released map[int64]int
}

func (u *videoPendingLimitUpstream) SelectAccount(_ context.Context, _ *APIKey, req VideoTaskSubmitRequest) (*VideoUpstreamSelection, error) {
	for _, account := range u.accounts {
		if _, excluded := req.ExcludedAccountIDs[account.ID]; excluded {
			continue
		}
		u.selected = append(u.selected, account.ID)
		selected := *u.selection
		selected.Account = account
		selected.Target.AccountID = account.ID
		selected.Release = func() { u.released[account.ID]++ }
		return &selected, nil
	}
	return nil, ErrVideoTaskBusy
}

func TestVideoTaskLifecycleReselectsFullAccountBeforeReserving(t *testing.T) {
	for _, allFull := range []bool{false, true} {
		t.Run(map[bool]string{false: "second_account_available", true: "all_accounts_full"}[allFull], func(t *testing.T) {
			s, key, original, b, _ := newVideoLifecycleFixture(BillingModeVideoToken)
			second := *original.selection.Account
			second.ID++
			second.Credentials = map[string]any{"video_max_output_tokens": 2_000_000}
			full := map[int64]bool{original.selection.Account.ID: true, second.ID: allFull}
			s.repo = videoPendingLimitRepo{VideoTaskRepository: s.repo, full: full}
			u := &videoPendingLimitUpstream{videoLifecycleUpstream: original, accounts: []*Account{original.selection.Account, &second}, released: map[int64]int{}}
			s.upstream = u
			response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
			require.Equal(t, []int64{original.selection.Account.ID, second.ID}, u.selected)
			require.Equal(t, 1, u.released[original.selection.Account.ID])
			require.Equal(t, 1, u.released[second.ID])
			if allFull {
				require.ErrorIs(t, err, ErrVideoTaskBusy)
				require.Zero(t, b.reserves)
				require.Zero(t, u.submitted)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, b.reserves)
			require.Equal(t, 1, u.submitted)
			task, err := s.repo.Get(context.Background(), response.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, second.ID, task.AccountID)
			require.Zero(t, task.Hold.BaseAmountUSD, "旧账号 Token 预算不再决定新任务预扣")
		})
	}
}

// 并发创建在数据库唯一键处命中另一主体时，仍须执行与前置幂等查询相同的归属检查。
type videoCreateRaceRepo struct {
	VideoTaskRepository
	existing *VideoTaskRecord
}

func (r videoCreateRaceRepo) Create(context.Context, *VideoTaskRecord, int) (*VideoTaskRecord, bool, error) {
	return r.existing, false, nil
}

func TestVideoTaskLifecycleIdempotencyRaceRetainsActorIsolation(t *testing.T) {
	s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideo)
	req := VideoTaskSubmitRequest{Body: []byte(`{}`), IdempotencyKey: "concurrent-create"}
	s.repo = videoCreateRaceRepo{VideoTaskRepository: s.repo, existing: &VideoTaskRecord{ID: "other-actor-task", UserID: key.User.ID + 1, PayloadHash: videoPayloadHash(req)}}
	response, err := s.Submit(context.Background(), key, req)
	require.ErrorIs(t, err, ErrVideoTaskConflict)
	require.Nil(t, response)
	require.Zero(t, upstream.submitted)
	require.Zero(t, billing.reserves)
}

// 固定图片费的夹具保留独立输入张数，不能由上游产物或后来编辑的价卡决定。
func enableVideoLifecycleImagePricing(key *APIKey, upstream *videoLifecycleUpstream, count, free int, price float64) {
	key.Group.ModelPricing[0].Platform = PlatformVideo
	key.Group.ModelPricing[0].VideoImageInputPricing = &VideoImageInputPricing{FreeImages: free, Price: &price}
	upstream.selection.Metadata.ReferenceImageCount = &count
}

// 预扣的固定秒价与最终视频 Token 单价独立配置。
func enableVideoLifecycleTokenPrepay(key *APIKey, price float64) {
	key.Group.ModelPricing[0].VideoTokenPrepay = &VideoTokenPrepayConfig{PricePerSecond: &price}
}

// 模拟升级前已经受理并持久化的 Token 上限任务，不能通过新提交重新制造该模式。
func restoreLegacyVideoLifecycleHold(s *VideoTaskService, task *VideoTaskRecord) {
	task.Hold.VideoDeferredBilling = false
	task.Hold.VideoTokenPrepay = false
	task.Hold.BaseAmountUSD = 1
	task.Hold.HoldAmount = 2 + task.Hold.VideoFixedAmountUSD
	task.Hold.BalanceHoldAmount = task.Hold.HoldAmount
	task.Hold.AllowanceReserved = true
	repo := s.repo.(*videoLifecycleRepo)
	repo.mu.Lock()
	repo.tasks[task.ID] = cloneVideoLifecycleTask(task)
	repo.mu.Unlock()
}

func TestVideoTaskImageFeeSplitsCostsAndFreezesPriceThroughRestart(t *testing.T) {
	for _, tc := range []struct {
		mode                             BillingMode
		reservedBase, actualBase, actual float64
	}{
		{BillingModeVideoToken, 1, .2, .9},
		{BillingModeVideo, 8, 5, 10.5},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			s, key, upstream, billing, logs := newVideoLifecycleFixture(tc.mode)
			enableVideoLifecycleImagePricing(key, upstream, 4, 2, .25)
			if tc.mode == BillingModeVideoToken {
				enableVideoLifecycleTokenPrepay(key, .125)
			}
			accountRate := 3.0
			upstream.selection.Account.RateMultiplier = &accountRate
			req := VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","images":["a","a","b","c"]}`), IdempotencyKey: "fixed-images"}
			response, err := s.Submit(context.Background(), key, req)
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), response.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, tc.reservedBase, task.Hold.BaseAmountUSD)
			require.Equal(t, .5, task.Hold.VideoFixedAmountUSD)
			expectedHold := tc.reservedBase*2 + .5
			if tc.mode == BillingModeVideoToken {
				expectedHold = tc.reservedBase + .5
			}
			require.Equal(t, expectedHold, task.Hold.HoldAmount)
			require.Equal(t, 4, *task.Quote.ReferenceImageCount)
			// 修改当前配置和输入指针不改变已经持久化的价卡与张数。
			*key.Group.ModelPricing[0].VideoImageInputPricing.Price = 99
			key.Group.ModelPricing[0].VideoImageInputPricing.FreeImages = 99
			*upstream.selection.Metadata.ReferenceImageCount = 99
			*upstream.selection.Account.RateMultiplier = 99
			tokens, outputImages := int64(200_000), 777
			upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Body: []byte(`{"status":"completed"}`), Metadata: VideoRequestMetadata{Tokens: &tokens, DurationSeconds: 5, ReferenceImageCount: &outputImages}}
			restarted := *s
			restarted.advance(context.Background(), task)
			fresh, err := s.repo.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.Equal(t, "settled", fresh.BillingStatus)
			require.InDelta(t, tc.actual, fresh.BillingResult.ActualAmountUSD, 1e-9)
			require.Equal(t, 4, *fresh.Metadata.ReferenceImageCount)
			require.Equal(t, 4, *fresh.Quote.ReferenceImageCount)
			require.Equal(t, .25, *fresh.Quote.ImageInputPricing.Price)
			require.Len(t, billing.captureCommands, 1)
			capture := billing.captureCommands[0]
			require.InDelta(t, tc.actualBase, capture.ActualBaseAmountUSD, 1e-9)
			require.Equal(t, .5, capture.VideoActualFixedAmountUSD)
			require.InDelta(t, (tc.actualBase+.5)*3, capture.VideoAccountQuotaCost, 1e-9, "账号成本统计沿用总基础成本乘账号倍率")
			log := logs.logs["video_capture:"+task.ID]
			require.NotNil(t, log)
			require.InDelta(t, tc.actualBase, log.OutputCost, 1e-9)
			require.Equal(t, .5, log.ImageInputCost)
			require.InDelta(t, tc.actualBase+.5, log.TotalCost, 1e-9)
			require.InDelta(t, tc.actual, log.ActualCost, 1e-9)
			require.InDelta(t, 2.0, log.RateMultiplier, 1e-9, "日志倍率应描述视频部分，不把固定费揉进倍率")
			restarted.advance(context.Background(), fresh)
			_, err = restarted.Submit(context.Background(), key, req)
			require.NoError(t, err)
			require.Equal(t, 1, upstream.submitted)
			require.Equal(t, 1, billing.captures)
			require.Len(t, logs.logs, 1)
		})
	}
}

func TestVideoTaskImageFeeDeferredSettlementRequiresUsageAndCapturesTogether(t *testing.T) {
	s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
	enableVideoLifecycleImagePricing(key, upstream, 4, 2, .25)
	delete(upstream.selection.Account.Credentials, "video_max_output_tokens")
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	require.True(t, task.Hold.VideoDeferredBilling)
	require.Zero(t, task.Hold.BaseAmountUSD)
	require.Zero(t, task.Hold.HoldAmount)
	require.Equal(t, .5, task.Hold.VideoFixedAmountUSD)
	upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Body: []byte(`{"status":"completed"}`)}
	s.advance(context.Background(), task)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.Empty(t, billing.captureCommands, "视频用量未知时不能先单独收取图片费")
	require.Empty(t, logs.logs)
	tokens := int64(300_000)
	upstream.poll.Metadata.Tokens = &tokens
	billing.captureErr = errors.New("insufficient balance")
	s.advance(context.Background(), task)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.Zero(t, billing.captures)
	require.Empty(t, logs.logs)
	require.Equal(t, .5, billing.captureCommands[0].VideoActualFixedAmountUSD)
	billing.captureErr = nil
	s.advance(context.Background(), task)
	require.Equal(t, "settled", task.BillingStatus)
	require.InDelta(t, 1.1, task.BillingResult.ActualAmountUSD, 1e-9)
	require.Equal(t, .5, logs.logs["video_capture:"+task.ID].ImageInputCost)
	require.Equal(t, 1, billing.captures)
	require.Equal(t, 1, upstream.submitted)
}

func TestVideoTaskImageFeeZeroVideoPriceDefersImageChargeWithoutUsage(t *testing.T) {
	for _, rate := range []float64{0, .25, 3} {
		s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
		key.Group.RateMultiplier = rate
		*key.Group.ModelPricing[0].VideoPrices[0].Price = 0
		enableVideoLifecycleImagePricing(key, upstream, 4, 2, .25)
		delete(upstream.selection.Account.Credentials, "video_max_output_tokens")
		response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
		require.NoError(t, err)
		task, err := s.repo.Get(context.Background(), response.LocalTaskID)
		require.NoError(t, err)
		require.True(t, task.Hold.VideoDeferredBilling)
		require.Zero(t, task.Hold.BaseAmountUSD)
		require.Equal(t, .5, task.Hold.VideoFixedAmountUSD)
		require.Zero(t, task.Hold.HoldAmount, "预扣关闭时零视频价也不能提前冻结图片费")
		upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Body: []byte(`{"status":"completed"}`)}
		s.advance(context.Background(), task)
		require.Equal(t, "settled", task.BillingStatus)
		require.Equal(t, .5, task.BillingResult.ActualAmountUSD)
		require.Equal(t, 1, billing.captures)
		require.Zero(t, billing.captureCommands[0].ActualBaseAmountUSD)
		require.Equal(t, .5, billing.captureCommands[0].VideoActualFixedAmountUSD)
		log := logs.logs["video_capture:"+task.ID]
		require.Zero(t, log.OutputCost)
		require.Equal(t, .5, log.ImageInputCost)
		require.Equal(t, .5, log.TotalCost)
		require.Equal(t, .5, log.ActualCost)
	}
}

func TestVideoTaskImageFeeUnknownCountOnlyBlocksEnabledPricing(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideo)
		if enabled {
			enableVideoLifecycleImagePricing(key, upstream, 0, 2, .25)
		}
		upstream.selection.Metadata.ReferenceImageCount = nil
		_, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","images":["a"],"image_urls":["a","b"]}`)})
		if enabled {
			require.ErrorIs(t, err, ErrVideoTaskPricing)
			require.Zero(t, upstream.submitted)
			require.Zero(t, billing.reserves)
		} else {
			require.NoError(t, err)
			require.Equal(t, 1, upstream.submitted)
		}
	}
}

func TestVideoTaskImageFeeFailureOrCancellationReleasesWithoutCharge(t *testing.T) {
	for _, state := range []string{"failed", "cancelled"} {
		s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
		enableVideoLifecycleImagePricing(key, upstream, 4, 2, .25)
		enableVideoLifecycleTokenPrepay(key, .25)
		response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
		require.NoError(t, err)
		task, err := s.repo.Get(context.Background(), response.LocalTaskID)
		require.NoError(t, err)
		require.Equal(t, 2.5, task.Hold.HoldAmount)
		upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: state, Body: []byte(`{}`)}
		s.advance(context.Background(), task)
		require.Equal(t, "released", task.BillingStatus)
		require.Equal(t, 1, billing.releases)
		require.Zero(t, billing.captures)
		require.Empty(t, logs.logs)
	}
}

func TestVideoTaskImageFeePreservesLegacyVideoBudgetLimit(t *testing.T) {
	for _, tokens := range []int64{0, 2_000_000} {
		s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
		enableVideoLifecycleImagePricing(key, upstream, 4, 2, .25)
		response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
		require.NoError(t, err)
		task, err := s.repo.Get(context.Background(), response.LocalTaskID)
		require.NoError(t, err)
		restoreLegacyVideoLifecycleHold(s, task)
		upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Metadata: VideoRequestMetadata{Tokens: &tokens}, Body: []byte(`{}`)}
		s.advance(context.Background(), task)
		if tokens == 0 {
			require.Equal(t, "settled", task.BillingStatus)
			require.Equal(t, .5, task.BillingResult.ActualAmountUSD, "可信零视频用量仍收取输入图片固定费")
			require.Equal(t, 1, billing.captures)
		} else {
			require.Equal(t, "reconciliation", task.BillingStatus)
			require.Zero(t, billing.captures)
			require.Empty(t, logs.logs)
		}
	}
}
