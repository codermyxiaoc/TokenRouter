package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// 使用真实选号、协议适配、任务和报价服务，仅 HTTP 与持久仓储替换为离线夹具。
type videoYezonghuiLifecycleHTTP struct {
	createBody   []byte
	requests     []*http.Request
	posts, polls int
	contents     int
	pollStatus   int
	pollBody     string
	pollError    error
}

func (f *videoYezonghuiLifecycleHTTP) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	f.requests = append(f.requests, req.Clone(req.Context()))
	status, body, mime := http.StatusOK, "", "application/json"
	switch {
	case req.Method == http.MethodPost:
		f.posts++
		f.createBody, _ = io.ReadAll(req.Body)
		status, body = http.StatusAccepted, `{"id":"yezonghui-lifecycle-task","object":"video","status":"queued"}`
	case strings.HasSuffix(req.URL.Path, "/content"):
		f.contents++
		body, mime = "test-video-content", "video/mp4"
	default:
		f.polls++
		if f.pollError != nil {
			return nil, f.pollError
		}
		status, body = f.pollStatus, f.pollBody
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {mime}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (f *videoYezonghuiLifecycleHTTP) DoWithTLS(req *http.Request, proxy string, accountID int64, limit int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return f.Do(req, proxy, accountID, limit)
}

func newVideoYezonghuiLifecycleFixture(mode BillingMode) (*VideoTaskService, *APIKey, *Account, *videoYezonghuiLifecycleHTTP, *videoLifecycleBilling, *videoLifecycleLogs) {
	s, key, _, billing, logs := newVideoLifecycleFixture(mode)
	key.Group.Status = StatusActive
	price, imagePrice, lowerTierPrice := .3, .25, .01
	if mode == BillingModeVideoPerRequest {
		price = 1.25
	}
	key.Group.ModelPricing = []ChannelModelPricing{{Platform: PlatformVideo, Models: []string{"video-model"}, BillingMode: mode,
		VideoPrices:            []VideoPriceTier{{Resolution: "720p", Price: &lowerTierPrice}, {Resolution: "1080p", Price: &price}},
		VideoImageInputPricing: &VideoImageInputPricing{Price: &imagePrice}}}
	account := videoFixtureAccount(VideoEndpointOpenAIVideos)
	account.GroupIDs = []int64{key.Group.ID}
	account.Credentials["video_endpoint_adapters"] = map[string]string{"openai_videos": VideoProtocolAdapterYezonghui}
	account.Credentials["video_base_urls"] = map[string]string{"openai_videos": "https://frozen-yezonghui.example/relay"}
	transport := &videoYezonghuiLifecycleHTTP{pollStatus: http.StatusOK,
		pollBody: `{"id":"yezonghui-lifecycle-task","object":"video","status":"completed","seconds":"6","resolution":"1080p","size":"16:9"}`}
	s.upstream = NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*account}},
		concurrencyService: NewConcurrencyService(nil), httpUpstream: transport})
	s.accounts = videoLifecycleAccounts{account: account}
	return s, key, account, transport, billing, logs
}

func videoYezonghuiLifecycleRequest() VideoTaskSubmitRequest {
	return VideoTaskSubmitRequest{InboundProtocol: "openai_videos", IdempotencyKey: "yezonghui-lifecycle-once",
		Body: []byte(`{"model":"video-model","prompt":"离线视频回归","duration":10,"resolution":"1080P","ratio":"16:9","images":["https://assets.example/reference-a.png","https://assets.example/reference-b.png"]}`)}
}

// 实际发送的档位与参考图数量决定预扣；重启、修改账号和反复读取不能改变冻结价格或再次创建。
func TestVideoYezonghuiLifecycleQuoteRestartAndReadOnlyDownload(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoPerRequest} {
		t.Run(string(mode), func(t *testing.T) {
			s, key, account, transport, billing, logs := newVideoYezonghuiLifecycleFixture(mode)
			req := videoYezonghuiLifecycleRequest()
			created, err := s.Submit(context.Background(), key, req)
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, "queued", task.Status)
			require.Equal(t, 2, task.Target.Version)
			require.Equal(t, VideoProtocolAdapterYezonghui, task.Target.ProtocolAdapter)
			require.Equal(t, "1080p", task.Metadata.Resolution)
			require.Equal(t, 10.0, task.Metadata.DurationSeconds)
			require.NotNil(t, task.Metadata.ReferenceImageCount)
			require.Equal(t, 2, *task.Metadata.ReferenceImageCount)
			require.Equal(t, 2, *task.Quote.ReferenceImageCount)
			require.JSONEq(t, `{"model":"video-model","prompt":"离线视频回归","seconds":"10","resolution":"1080p","size":"16:9","image_urls":["https://assets.example/reference-a.png","https://assets.example/reference-b.png"]}`, string(transport.createBody))
			wantHold, wantCharge := 6.5, 4.1
			if mode == BillingModeVideoPerRequest {
				wantHold, wantCharge = 3, 3
			}
			require.InDelta(t, wantHold, task.Hold.HoldAmount, 1e-9)
			require.Equal(t, 1, billing.reserves)
			require.Equal(t, 1, transport.posts)
			require.Equal(t, "https://frozen-yezonghui.example/relay/v1/videos", transport.requests[0].URL.String())
			require.Equal(t, "Bearer fixture-secret", transport.requests[0].Header.Get("Authorization"))
			quote, err := json.Marshal(task.Quote)
			require.NoError(t, err)
			frozen := task.Target

			// 受理后修改配置与价卡，仅影响新任务，查询及下载继续使用原地址和协议。
			account.Credentials["video_endpoints"] = []string{"minimax"}
			account.Credentials["video_endpoint_adapters"] = map[string]string{"minimax": "minimax_v2"}
			account.Credentials["video_base_urls"] = map[string]string{"minimax": "https://changed.example"}
			*key.Group.ModelPricing[0].VideoPrices[1].Price = 99
			restarted := *s
			replayed, err := restarted.Submit(context.Background(), key, req)
			require.NoError(t, err)
			require.Equal(t, task.ID, replayed.LocalTaskID)
			restarted.advance(context.Background(), task)
			fresh, err := s.repo.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.Equal(t, "completed", fresh.Status)
			require.Equal(t, "settled", fresh.BillingStatus)
			require.Empty(t, fresh.VideoURL)
			require.Equal(t, frozen, fresh.Target)
			require.Equal(t, 6.0, fresh.Metadata.DurationSeconds)
			require.Equal(t, "1080p", fresh.Metadata.Resolution)
			afterQuote, err := json.Marshal(fresh.Quote)
			require.NoError(t, err)
			require.Equal(t, quote, afterQuote)
			require.InDelta(t, wantCharge, fresh.BillingResult.ActualAmountUSD, 1e-9)
			require.Equal(t, 1, billing.captures)
			require.Zero(t, billing.releases)
			require.Equal(t, "https://frozen-yezonghui.example/relay/v1/videos/yezonghui-lifecycle-task", transport.requests[1].URL.String())
			require.Equal(t, "Bearer fixture-secret", transport.requests[1].Header.Get("Authorization"))
			require.Len(t, logs.logs, 1)
			require.InDelta(t, .5, logs.logs["video_capture:"+task.ID].ImageInputCost, 1e-9)
			restarted.advance(context.Background(), fresh)
			_, err = restarted.Query(context.Background(), key, task.ID, "openai_videos")
			require.NoError(t, err)
			require.Equal(t, 1, transport.polls)

			for range 2 {
				content, err := restarted.OpenContent(context.Background(), key, task.ID, "openai_videos", "bytes=0-")
				require.NoError(t, err)
				payload, err := io.ReadAll(content.Body)
				require.NoError(t, err)
				require.NoError(t, content.Body.Close())
				require.Equal(t, "test-video-content", string(payload))
				require.Equal(t, "video/mp4", content.Header.Get("Content-Type"))
				request := transport.requests[len(transport.requests)-1]
				require.Equal(t, "https://frozen-yezonghui.example/relay/v1/videos/yezonghui-lifecycle-task/content", request.URL.String())
				require.Equal(t, "Bearer fixture-secret", request.Header.Get("Authorization"))
				require.Equal(t, "bytes=0-", request.Header.Get("Range"))
				require.True(t, HTTPUpstreamRedirectsDisabled(request.Context()))
			}
			require.Equal(t, 2, transport.contents)
			require.Equal(t, 1, transport.posts)
			require.Equal(t, 1, transport.polls)
			require.Equal(t, 1, billing.reserves)
			require.Equal(t, 1, billing.captures)
			require.Zero(t, billing.releases)
		})
	}
}

// 上游明确失败释放视频和图片两部分预扣，恢复和幂等重放均不再次扣费或提交。
func TestVideoYezonghuiLifecycleFailureReleasesOnce(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoPerRequest} {
		t.Run(string(mode), func(t *testing.T) {
			s, key, _, transport, billing, logs := newVideoYezonghuiLifecycleFixture(mode)
			created, err := s.Submit(context.Background(), key, videoYezonghuiLifecycleRequest())
			require.NoError(t, err)
			transport.pollBody = `{"id":"yezonghui-lifecycle-task","object":"video","status":"failed","error":{"message":"rejected"}}`
			task, err := s.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			s.advance(context.Background(), task)
			fresh, err := s.repo.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.Equal(t, "failed", fresh.Status)
			require.Equal(t, "released", fresh.BillingStatus)
			s.advance(context.Background(), fresh)
			_, err = s.Submit(context.Background(), key, videoYezonghuiLifecycleRequest())
			require.NoError(t, err)
			require.Equal(t, 1, billing.reserves)
			require.Equal(t, 1, billing.releases)
			require.Zero(t, billing.captures)
			require.Empty(t, logs.logs)
			require.Equal(t, 1, transport.posts)
			require.Equal(t, 1, transport.polls)
			_, err = s.OpenContent(context.Background(), key, task.ID, "openai_videos", "")
			require.Error(t, err)
			require.Zero(t, transport.contents)
		})
	}
}

// 查询错误和 unknown 都不能被误判为失败退款、成功扣款或需要重新创建；后续仅恢复原任务。
func TestVideoYezonghuiLifecycleUncertainPollKeepsSingleReservation(t *testing.T) {
	for _, scenario := range []string{"http_error", "unknown", "timeout", "foreign_task"} {
		t.Run(scenario, func(t *testing.T) {
			s, key, _, transport, billing, logs := newVideoYezonghuiLifecycleFixture(BillingModeVideo)
			created, err := s.Submit(context.Background(), key, videoYezonghuiLifecycleRequest())
			require.NoError(t, err)
			switch scenario {
			case "http_error":
				transport.pollStatus, transport.pollBody = http.StatusBadGateway, `{"id":"yezonghui-lifecycle-task","status":"failed"}`
			case "unknown":
				transport.pollBody = `{"id":"yezonghui-lifecycle-task","object":"video","status":"unknown"}`
			case "timeout":
				transport.pollError = context.DeadlineExceeded
			case "foreign_task":
				transport.pollBody = `{"id":"foreign-task","object":"video","status":"completed","seconds":"6","resolution":"1080p","size":"16:9"}`
			}
			for range 2 {
				task, err := s.repo.Get(context.Background(), created.LocalTaskID)
				require.NoError(t, err)
				s.advance(context.Background(), task)
				fresh, err := s.repo.Get(context.Background(), task.ID)
				require.NoError(t, err)
				require.Equal(t, "queued", fresh.Status)
				require.Equal(t, "reserved", fresh.BillingStatus)
				require.Equal(t, "yezonghui-lifecycle-task", fresh.UpstreamTaskID)
				_, err = s.Submit(context.Background(), key, videoYezonghuiLifecycleRequest())
				require.NoError(t, err)
			}
			require.Equal(t, 1, transport.posts)
			require.Equal(t, 1, billing.reserves)
			require.Zero(t, billing.captures+billing.releases)
			require.Empty(t, logs.logs)
			transport.pollError, transport.pollStatus = nil, http.StatusOK
			transport.pollBody = `{"id":"yezonghui-lifecycle-task","object":"video","status":"completed","seconds":"6","resolution":"1080p","size":"16:9"}`
			task, err := s.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			restarted := *s
			restarted.advance(context.Background(), task)
			fresh, err := s.repo.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.Equal(t, "settled", fresh.BillingStatus)
			require.Equal(t, 1, transport.posts)
			require.Equal(t, 1, billing.captures)
			require.Zero(t, billing.releases)
		})
	}
}

// 非法参数和像素尺寸必须在报价与付费创建前拒绝，不能通过同路径套用其它协议的计价。
func TestVideoYezonghuiLifecycleInvalidParametersDoNotReserve(t *testing.T) {
	for _, body := range []string{
		`{"model":"video-model","prompt":"x","seconds":"10","resolution":"1080p","size":"1280x720"}`,
		`{"model":"video-model","prompt":"x","seconds":"10","duration":5,"resolution":"1080p"}`,
		`{"model":"video-model","prompt":"x","seconds":"10","resolution":"auto"}`,
		`{"model":"video-model","prompt":"x","seconds":"10","resolution":"1080p","image_urls":["data:image/png;base64,AAAA"]}`,
	} {
		t.Run(body, func(t *testing.T) {
			s, key, _, transport, billing, logs := newVideoYezonghuiLifecycleFixture(BillingModeVideo)
			_, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{InboundProtocol: "openai_videos", Body: []byte(body)})
			require.Error(t, err)
			require.Empty(t, s.repo.(*videoLifecycleRepo).tasks)
			require.Zero(t, transport.posts+transport.polls+transport.contents)
			require.Zero(t, billing.reserves+billing.captures+billing.releases)
			require.Empty(t, logs.logs)
		})
	}
}
