//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type videoKemeiCase struct {
	endpoint                 service.VideoEndpoint
	model, createPath, query string
	body, noUsage, zeroUsage string
	resolution               string
}

// 各样例只使用虚构素材地址；真正的协议解析、报价和 PostgreSQL 账务在本地执行。
func videoKemeiCases() []videoKemeiCase {
	return []videoKemeiCase{
		{endpoint: service.VideoEndpointCompat, model: "compat-video", createPath: "/v1/video/generations", query: "/v1/video/generations/kemei-task", resolution: "720p",
			body:      `{"model":"compat-video","prompt":"隔离计费测试","duration":8,"resolution":"720p","images":["https://example.com/reference.png"]}`,
			noUsage:   `{"id":"kemei-task","task_id":"kemei-task","status":"completed","data":[{"url":"https://example.com/video.mp4"}]}`,
			zeroUsage: `{"id":"kemei-task","status":"completed","data":[{"url":"https://example.com/video.mp4"}],"usage":{"completion_tokens":0,"prompt_tokens":0,"total_tokens":0}}`},
		{endpoint: service.VideoEndpointSeedance, model: "seedance-video", createPath: "/api/v3/contents/generations/tasks", query: "/api/v3/contents/generations/tasks/kemei-task", resolution: "720p",
			body:    `{"model":"seedance-video","duration":8,"resolution":"720p","content":[{"type":"text","text":"隔离计费测试"},{"type":"image_url","image_url":{"url":"https://example.com/reference.png"},"role":"first_frame"}]}`,
			noUsage: `{"id":"kemei-task","status":"succeeded","content":{"video_url":"https://example.com/video.mp4"}}`},
		{endpoint: service.VideoEndpointMiniMax, model: "MiniMax-H3", createPath: "/v2/video_generation", query: "/v2/query/video_generation/kemei-task", resolution: "768p",
			body:      `{"model":"MiniMax-H3","prompt":"隔离计费测试","duration":8,"resolution":"768P","images":["https://example.com/reference.png"]}`,
			noUsage:   `{"task":{"id":"kemei-task","status":"completed","content":{"url":"https://example.com/video.mp4"},"usage":{"output_seconds":5}}}`,
			zeroUsage: `{"task":{"id":"kemei-task","status":"completed","content":{"url":"https://example.com/video.mp4"},"usage":{"output_seconds":5,"completion_tokens":0,"prompt_tokens":0,"total_tokens":0}}}`},
		{endpoint: service.VideoEndpointWan, model: "wan-video", createPath: "/api/v1/services/aigc/video-generation/video-synthesis", query: "/api/v1/tasks/kemei-task", resolution: "720p",
			body:    `{"model":"wan-video","input":{"prompt":"隔离计费测试","media":[{"type":"reference_image","url":"https://example.com/reference.png"}]},"parameters":{"duration":8,"resolution":"720P"}}`,
			noUsage: `{"output":{"task_id":"kemei-task","task_status":"SUCCEEDED","video_url":"https://example.com/video.mp4"},"usage":{"output_video_duration":5}}`},
		{endpoint: service.VideoEndpointKling, model: "kling-video", createPath: "/image-to-video/kling-video", query: "/tasks?task_ids=kemei-task", resolution: "720p",
			body:    `{"model":"kling-video","prompt":"隔离计费测试","duration":8,"resolution":"720p","images":["https://example.com/reference.png"]}`,
			noUsage: `{"data":[{"id":"kemei-task","status":"completed","outputs":[{"id":"artifact-id","type":"video","url":"https://example.com/video.mp4","duration":5}]}]}`},
		{endpoint: service.VideoEndpointVidu, model: "viduq2", createPath: "/ent/v2/img2video", query: "/ent/v2/tasks/kemei-task/creations", resolution: "720p",
			body:    `{"model":"viduq2","prompt":"隔离计费测试","duration":8,"resolution":"720p","images":["https://example.com/reference.png"]}`,
			noUsage: `{"id":"kemei-task","state":"success","credits":800,"creations":[{"id":"artifact-id","url":"https://example.com/video.mp4"}]}`},
	}
}

func videoKemeiBillingTask(t *testing.T, f *videoBillingFixture, tc videoKemeiCase, mode videoProfileBillingCase) *service.VideoTaskRecord {
	t.Helper()
	ctx := context.Background()
	defaultAdapter := service.VideoProtocolAdapterLegacy
	if tc.endpoint == service.VideoEndpointVidu {
		defaultAdapter = "kemei_v1"
	}
	account := &service.Account{ID: f.accountID, Platform: service.PlatformVideo, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url":                "https://frozen.example",
			"video_endpoints":         []string{string(tc.endpoint)},
			"video_endpoint_adapters": map[string]string{string(tc.endpoint): defaultAdapter},
			"video_model_adapters":    map[string]map[string]string{tc.model: {string(tc.endpoint): "kemei_v1"}},
		}}
	cfg, err := account.VideoConfiguration()
	require.NoError(t, err)
	adapter, explicit := cfg.ProtocolAdapter(tc.model, tc.endpoint)
	require.True(t, explicit)
	require.Equal(t, "kemei_v1", adapter)
	target := service.VideoUpstreamTarget{Version: 2, ProtocolAdapter: adapter, Endpoint: tc.endpoint,
		AccountID: f.accountID, BaseURL: "https://frozen.example", CreatePath: tc.createPath, Model: tc.model}
	_, metadata, err := service.PrepareVideoProtocolRequest([]byte(tc.body), "", target)
	require.NoError(t, err)
	// 原生正文保留供应商要求的 768P/720P；计价层按既有规则归一化同一分辨率档位。
	normalizedResolution, err := service.NormalizeVideoPriceResolution(metadata.Resolution)
	require.NoError(t, err)
	require.Equal(t, tc.resolution, normalizedResolution)
	require.Equal(t, float64(8), metadata.DurationSeconds)
	require.NotNil(t, metadata.ReferenceImageCount)
	require.Equal(t, 1, *metadata.ReferenceImageCount)

	price := .2
	if mode.mode == service.BillingModeVideoToken {
		price = 10
	} else if mode.mode == service.BillingModeVideoPerRequest {
		price = 1.25
	}
	imagePrice, priceMultiplier, prepay := .15, 2.0, .3
	pricing := service.ChannelModelPricing{Platform: service.PlatformVideo, Models: []string{tc.model}, BillingMode: mode.mode,
		PriceMultiplier: &priceMultiplier, VideoPrices: []service.VideoPriceTier{{Resolution: tc.resolution, Price: &price}},
		VideoImageInputPricing: &service.VideoImageInputPricing{FreeImages: 0, Price: &imagePrice}}
	if mode.mode == service.BillingModeVideoToken && !mode.deferToken {
		pricing.VideoTokenPrepay = &service.VideoTokenPrepayConfig{PricePerSecond: &prepay}
	}
	group := &service.Group{ID: f.groupID, Platform: service.PlatformVideo, RateMultiplier: 3, ModelPricing: []service.ChannelModelPricing{pricing}}
	quote, err := service.NewModelPricingResolver(nil, nil).QuoteVideo(ctx, service.VideoPriceInput{
		Group: group, Model: metadata.Model, Resolution: metadata.Resolution, ReferenceImageCount: metadata.ReferenceImageCount, RateMultiplier: 3})
	require.NoError(t, err)
	require.Equal(t, tc.resolution, quote.Resolution)
	zero := int64(0)
	estimate, err := quote.Calculate(metadata.DurationSeconds, &zero)
	require.NoError(t, err)
	base := estimate.OutputCost
	if quote.TokenPrepay != nil {
		base = metadata.DurationSeconds * *quote.TokenPrepay.PricePerSecond
	}
	id, now, lease := "vid_"+uuid.NewString(), time.Now().UTC(), uuid.NewString()
	task := &service.VideoTaskRecord{ID: id, UserID: f.userID, APIKeyID: f.keyID, GroupID: f.groupID, AccountID: f.accountID,
		PayloadHash: uuid.NewString(), Status: "prepared", BillingStatus: "pending", CreatedAt: now,
		LeaseToken: lease, LeaseUntil: now.Add(10 * time.Minute), Metadata: metadata, Quote: quote, AccountRateMultiplier: 1,
		Target: target, RequestedModel: tc.model, InternalModel: tc.model, InboundPath: tc.createPath}
	task.Hold = service.BatchImageBalanceHoldCommand{RequestID: "video_hold:" + id, BatchID: id, UserID: f.userID, ActorUserID: f.userID,
		APIKeyID: f.keyID, GroupID: &f.groupID, APIKeyBillingMode: service.APIKeyBillingModeAuto, PricingSnapshotVersion: 3,
		BaseAmountUSD: base, SubscriptionRateMultiplier: 3, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 3,
		SettlementRateScale: 1, ReservedAt: now, VideoEntity: true, VideoAccountID: f.accountID, VideoLeaseToken: lease,
		VideoFixedAmountUSD: estimate.ImageInputCost, VideoDeferredBilling: mode.deferToken,
		VideoTokenPrepay: mode.mode == service.BillingModeVideoToken && !mode.deferToken}
	if task.Hold.VideoTokenPrepay {
		task.Hold.VideoPrepayDurationSeconds = metadata.DurationSeconds
	}
	_, created, err := f.tasks.Create(ctx, task, 1000)
	require.NoError(t, err)
	require.True(t, created)
	return task
}

// 新供应商必须继承旧资金合同：原账号与模型配置改变后，冻结路由、价格和图片附加费都不能漂移。
func TestVideoKemeiFrozenBillingLifecycle(t *testing.T) {
	for _, tc := range videoKemeiCases() {
		for _, mode := range videoProfileModes() {
			for _, subscription := range []bool{false, true} {
				for _, status := range []string{"completed", "failed"} {
					t.Run(fmt.Sprintf("%s/%s/deferred_%t/subscription_%t/%s", tc.endpoint, mode.mode, mode.deferToken, subscription, status), func(t *testing.T) {
						f := newVideoBillingFixture(t)
						f.quota(1000)
						var sub *service.UserSubscription
						if subscription {
							sub = fixedVideoSubscription(t, f, 1000, .5)
						}
						task := f.reserve(videoKemeiBillingTask(t, f, tc, mode))
						mode.image = true
						hold, due, accountCost := videoProfileExpected(mode, subscription)
						require.InDelta(t, hold, task.Hold.HoldAmount, 1e-8)
						videoProfileMoney(t, f, sub, hold, hold, 0)
						frozen, quote := task.Target, *task.Quote
						_, err := integrationDB.ExecContext(context.Background(), `UPDATE accounts SET credentials=$2::jsonb WHERE id=$1`, f.accountID,
							`{"base_url":"https://changed.example","video_endpoints":["minimax"],"video_endpoint_adapters":{"minimax":"minimax_v2"},"video_model_adapters":{}}`)
						require.NoError(t, err)
						task, err = NewVideoTaskRepository(integrationDB).Get(context.Background(), task.ID)
						require.NoError(t, err)
						require.Equal(t, frozen, task.Target)
						require.Equal(t, quote, *task.Quote)
						path, err := service.VideoProtocolTaskPath(task.Target, "kemei-task")
						require.NoError(t, err)
						require.Equal(t, tc.query, path)
						command, _ := videoProfileFinalCommand(t, f, task, status, 5, 100000)
						apply, wantStatus := f.repo.CaptureBatchImageBalance, "settled"
						if status == "failed" {
							apply, due, accountCost, wantStatus = f.repo.ReleaseBatchImageBalance, 0, 0, "released"
						}
						for attempt := range 3 {
							local := command
							result, err := apply(context.Background(), &local)
							require.NoError(t, err)
							require.Equal(t, attempt == 0, result.Applied)
							fresh, err := NewVideoTaskRepository(integrationDB).Get(context.Background(), task.ID)
							require.NoError(t, err)
							require.Equal(t, frozen, fresh.Target)
							require.Equal(t, quote, *fresh.Quote)
							require.Equal(t, wantStatus, fresh.BillingStatus)
						}
						videoProfileMoney(t, f, sub, due, 0, accountCost)
					})
				}
			}
		}
	}
}

// 不同恢复器同时申请捕获和释放时，只有与持久终态一致的一方能够生效一次。
func TestVideoKemeiConcurrentCaptureRelease(t *testing.T) {
	for _, tc := range videoKemeiCases() {
		for _, status := range []string{"completed", "failed"} {
			t.Run(string(tc.endpoint)+"/"+status, func(t *testing.T) {
				f := newVideoBillingFixture(t)
				f.quota(1000)
				task := f.reserve(videoKemeiBillingTask(t, f, tc, videoProfileBillingCase{mode: service.BillingModeVideoToken}))
				command, _ := videoProfileFinalCommand(t, f, task, status, 5, 100000)
				var captures, releases atomic.Int64
				var wg sync.WaitGroup
				errs := make(chan error, 40)
				for range 20 {
					for _, capture := range []bool{true, false} {
						wg.Add(1)
						go func(capture bool) {
							defer wg.Done()
							local := command
							apply, requestPrefix, counter := f.repo.ReleaseBatchImageBalance, "video_release:", &releases
							if capture {
								apply, requestPrefix, counter = f.repo.CaptureBatchImageBalance, "video_capture:", &captures
							}
							local.RequestID = requestPrefix + task.ID
							result, err := apply(context.Background(), &local)
							if capture != (status == "completed") {
								if !errors.Is(err, service.ErrVideoTaskConflict) {
									errs <- fmt.Errorf("相反终态必须被拒绝: capture=%t err=%v", capture, err)
								}
								return
							}
							if err == nil && result.Applied {
								counter.Add(1)
							}
							errs <- err
						}(capture)
					}
				}
				wg.Wait()
				close(errs)
				for err := range errs {
					require.NoError(t, err)
				}
				if status == "completed" {
					require.EqualValues(t, 1, captures.Load())
					require.Zero(t, releases.Load())
					f.money(993.85, 0, 6.15, 2.15)
					f.quotaUsage(6.15, 6.15, 6.15)
				} else {
					require.EqualValues(t, 1, releases.Load())
					require.Zero(t, captures.Load())
					f.money(1000, 0, 0, 0)
					f.quotaUsage(0, 0, 0)
				}
				var count int
				require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1 AND (request_id=$2 OR request_id=$3)`, f.keyID, "video_capture:"+task.ID, "video_release:"+task.ID).Scan(&count))
				require.Equal(t, 1, count)
			})
		}
	}
}

// 未提供可信 Token 时恢复器必须先补查；此替身只阻断外部网络，不替代账务实现。
type videoKemeiUnavailableAccount struct {
	service.AccountRepository
	queries atomic.Int64
}

func (r *videoKemeiUnavailableAccount) GetByID(context.Context, int64) (*service.Account, error) {
	r.queries.Add(1)
	return nil, errors.New("隔离测试禁止访问真实视频上游")
}

func TestVideoKemeiUnknownTokensNeverCompleteForFree(t *testing.T) {
	for _, tc := range videoKemeiCases() {
		for _, body := range []string{tc.noUsage, tc.zeroUsage} {
			if body == "" {
				continue
			}
			t.Run(string(tc.endpoint)+"/"+fmt.Sprint(len(body)), func(t *testing.T) {
				f := newVideoBillingFixture(t)
				f.quota(1000)
				task := f.reserve(videoKemeiBillingTask(t, f, tc, videoProfileBillingCase{mode: service.BillingModeVideoToken}))
				response, err := service.ExtractVideoProtocolResponse(task.Target, 200, []byte(body), "kemei-task")
				require.NoError(t, err)
				require.Equal(t, "completed", response.Status)
				require.Nil(t, response.Metadata.Tokens, "没有返回用量、占位零或Vidu credits不能成为免费完成依据")
				_, err = task.Quote.Calculate(5, response.Metadata.Tokens)
				require.ErrorIs(t, err, service.ErrVideoUsageUnavailable)
				task.Status, task.BillingStatus, task.UpstreamTaskID = "completed", "reconciliation", "kemei-task"
				task.RawResponse, task.ResponseStatus = []byte(body), 200
				require.NoError(t, f.tasks.Save(context.Background(), task, true))
				accounts := &videoKemeiUnavailableAccount{}
				s := service.ProvideVideoTaskService(f.tasks, nil, nil, f.repo, nil, accounts, nil, nil, nil, nil, nil)
				s.Stop()
				key := &service.APIKey{ID: f.keyID, UserID: f.userID, User: &service.User{ID: f.userID}}
				for range 3 {
					_, err := integrationDB.ExecContext(context.Background(), `UPDATE video_tasks SET lease_until=NULL,next_poll_at=NOW()-INTERVAL '1 second' WHERE id=$1`, task.ID)
					require.NoError(t, err)
					_, err = s.Query(context.Background(), key, task.ID, "unified")
					require.NoError(t, err)
				}
				require.EqualValues(t, 3, accounts.queries.Load())
				fresh, err := NewVideoTaskRepository(integrationDB).Get(context.Background(), task.ID)
				require.NoError(t, err)
				require.Equal(t, "reconciliation", fresh.BillingStatus)
				require.False(t, fresh.EffectsDone)
				require.Nil(t, fresh.BillingResult)
				f.money(997.45, 2.55, 2.55, 0)
				f.quotaUsage(2.55, 2.55, 2.55)
			})
		}
	}
}

// 同供应商版本换号保留一次预扣；跨供应商、跨端点及未知版本不得复用已经冻结的生成请求。
func TestVideoKemeiRetryTransactionIsolation(t *testing.T) {
	for _, scenario := range []string{"same_adapter", "legacy_adapter", "different_endpoint", "future_adapter", "future_snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			task := f.reserve(videoKemeiBillingTask(t, f, videoKemeiCases()[0], videoProfileBillingCase{mode: service.BillingModeVideo}))
			task.Status, task.CreateResponseStatus = "failed", 429
			task.CreateResponse = []byte(`{"error":{"code":"QuotaExceeded"}}`)
			task.RawResponse = task.CreateResponse
			require.NoError(t, f.tasks.Save(context.Background(), task, false))
			next := newVideoRetryTarget(t)
			target := task.Target
			target.AccountID, target.Model = next.ID, "next-video-model"
			switch scenario {
			case "legacy_adapter":
				target.ProtocolAdapter = service.VideoProtocolAdapterLegacy
			case "different_endpoint":
				target.Endpoint, target.CreatePath = service.VideoEndpointMiniMax, "/v2/video_generation"
			case "future_adapter":
				target.ProtocolAdapter = "kemei_v999"
			case "future_snapshot":
				target.Version = 99
			}
			quote, err := json.Marshal(task.Quote)
			require.NoError(t, err)
			moved, err := f.tasks.(service.VideoTaskRetryRepository).SwitchRejected(context.Background(), task.ID, task.LeaseToken, target, task.Metadata, 2, 10)
			if scenario != "same_adapter" {
				require.ErrorIs(t, err, service.ErrVideoTaskConflict)
				fresh, err := f.tasks.Get(context.Background(), task.ID)
				require.NoError(t, err)
				require.Equal(t, task.Target, fresh.Target)
				require.Empty(t, fresh.RejectedAttempts)
			} else {
				require.NoError(t, err)
				require.Equal(t, target, moved.Target)
				require.Equal(t, task.ID, moved.ID)
				require.Equal(t, task.Hold.RequestID, moved.Hold.RequestID)
				require.Equal(t, task.Hold.HoldAmount, moved.Hold.HoldAmount)
				movedQuote, err := json.Marshal(moved.Quote)
				require.NoError(t, err)
				require.Equal(t, quote, movedQuote)
				require.Len(t, moved.RejectedAttempts, 1)
			}
			f.money(990.25, 9.75, 9.75, 0)
		})
	}
}

// HTTP 错误不能抹去原生协议的受理证据；即使没有任务 ID，事务也不能把可能已生成的请求换号重提。
func TestVideoKemeiAcceptanceEvidenceBlocksRetry(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		allowed        bool
		kling          bool
	}{
		{name: "created", response: `{"state":"created","err_code":"QuotaExceeded"}`},
		{name: "queueing", response: `{"state":"queueing","err_code":"QuotaExceeded"}`},
		{name: "processing", response: `{"state":"processing","err_code":"QuotaExceeded"}`},
		{name: "success", response: `{"state":"success","err_code":"QuotaExceeded"}`},
		{name: "video_artifact", response: `{"creations":[{"url":"https://example.com/generated.mp4"}],"err_code":"QuotaExceeded"}`},
		{name: "explicit_rejection", response: `{"state":"failed","err_code":"QuotaExceeded"}`, allowed: true},
		{name: "kling_video_artifact", response: `{"outputs":[{"type":"video","url":"https://example.com/generated.mp4"}],"error":{"code":"QuotaExceeded"}}`, kling: true},
		{name: "kling_task_video_artifact", response: `{"data":[{"outputs":[{"type":"video","url":"https://example.com/generated.mp4"}]}],"error":{"code":"QuotaExceeded"}}`, kling: true},
		{name: "kling_empty_artifacts_rejection", response: `{"outputs":[],"error":{"code":"QuotaExceeded"}}`, allowed: true, kling: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVideoBillingFixture(t)
			fixture := videoKemeiCases()[5]
			if tc.kling {
				fixture = videoKemeiCases()[4]
			}
			task := f.reserve(videoKemeiBillingTask(t, f, fixture, videoProfileBillingCase{mode: service.BillingModeVideo}))
			task.Status, task.CreateResponseStatus = "failed", 429
			task.CreateResponse, task.RawResponse = []byte(tc.response), []byte(tc.response)
			require.NoError(t, f.tasks.Save(context.Background(), task, false))
			target := task.Target
			target.AccountID = newVideoRetryTarget(t).ID
			moved, err := f.tasks.(service.VideoTaskRetryRepository).SwitchRejected(context.Background(), task.ID, task.LeaseToken, target, task.Metadata, 1, 10)
			if tc.allowed {
				require.NoError(t, err)
				require.Equal(t, target, moved.Target)
				require.Len(t, moved.RejectedAttempts, 1)
			} else {
				require.ErrorIs(t, err, service.ErrVideoTaskConflict)
				fresh, err := f.tasks.Get(context.Background(), task.ID)
				require.NoError(t, err)
				require.Equal(t, task.Target, fresh.Target)
				require.Empty(t, fresh.RejectedAttempts)
			}
			f.money(990.25, 9.75, 9.75, 0)
		})
	}
}
