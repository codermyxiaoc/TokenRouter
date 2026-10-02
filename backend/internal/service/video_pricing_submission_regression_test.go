package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 记录真实选择器取得的短请求槽，定价失败也必须归还该槽。
type videoPricingAuditTransport struct {
	*VideoUpstreamService
	selected, released int
}

func (s *videoPricingAuditTransport) SelectAccount(ctx context.Context, key *APIKey, request VideoTaskSubmitRequest) (*VideoUpstreamSelection, error) {
	selection, err := s.VideoUpstreamService.SelectAccount(ctx, key, request)
	if err != nil {
		return nil, err
	}
	s.selected++
	release := selection.Release
	selection.Release = func() {
		s.released++
		if release != nil {
			release()
		}
	}
	return selection, nil
}

// HTTP 正文经真实选号、协议元数据和报价后，缺价必须在建任务、冻结预算、上游 POST 之前失败。
func TestVideoPricingSubmissionRegressionMissingPriceNeverChargesOrSubmits(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken} {
		t.Run(string(mode), func(t *testing.T) {
			for _, tc := range []struct {
				name, body   string
				configure    func(*APIKey)
				wantSelected bool
			}{
				{name: "missing_model", body: `{"resolution":"720p","duration":5}`},
				{name: "unconfigured_model", body: `{"model":"unknown-model","resolution":"720p","duration":5}`, wantSelected: true},
				{name: "no_model_pricing", body: `{"model":"video-model","resolution":"720p","duration":5}`, configure: func(k *APIKey) { k.Group.ModelPricing = nil }, wantSelected: true},
				{name: "missing_resolution", body: `{"model":"video-model","duration":5}`, wantSelected: true},
				{name: "unconfigured_resolution", body: `{"model":"video-model","resolution":"540p","duration":5}`, wantSelected: true},
				{name: "reference_video_cannot_fill_missing_resolution", body: `{"model":"video-model","resolution":"540p","duration":5,"videos":["https://assets.example/video.mp4"]}`, wantSelected: true},
				{name: "fallback_cannot_hide_null_configured_tier", body: `{"model":"video-model","resolution":"720p","duration":5,"videos":["https://assets.example/video.mp4"]}`, configure: func(k *APIKey) {
					// 参考视频不参与选价，已配置档位的空价也不能借免费兜底放行。
					zero := 0.
					k.Group.ModelPricing[0].VideoFallbackPrice = &zero
					k.Group.ModelPricing[0].VideoPrices[0].Price = nil
				}, wantSelected: true},
				{name: "fallback_cannot_price_missing_resolution", body: `{"model":"video-model","duration":5}`, configure: func(k *APIKey) { zero := 0.; k.Group.ModelPricing[0].VideoFallbackPrice = &zero }, wantSelected: true},
				{name: "null_tier_not_free", body: `{"model":"video-model","resolution":"720p","duration":5}`, configure: func(k *APIKey) { k.Group.ModelPricing[0].VideoPrices[0].Price = nil }, wantSelected: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					s, key, _, billing, logs := newVideoLifecycleFixture(mode)
					key.Group.Status = StatusActive
					if tc.configure != nil {
						tc.configure(key)
					}
					account := videoFixtureAccount(VideoEndpointCompat)
					account.GroupIDs = []int64{key.Group.ID}
					httpFixture := &videoHTTPFixture{status: 202, response: `{"id":"unexpected","status":"queued"}`}
					transport := &videoPricingAuditTransport{VideoUpstreamService: NewVideoUpstreamService(&OpenAIGatewayService{
						accountRepo: &videoSelectionFixture{accounts: []Account{*account}}, concurrencyService: NewConcurrencyService(nil), httpUpstream: httpFixture})}
					s.upstream = transport
					r := httptest.NewRequest(http.MethodPost, "/v1/video/generations", strings.NewReader(tc.body))
					r.Header.Set("Content-Type", "application/json")
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					result, err := s.Submit(r.Context(), key, VideoTaskSubmitRequest{Body: body, ContentType: r.Header.Get("Content-Type"), InboundProtocol: "unified"})
					require.Error(t, err)
					require.Nil(t, result)
					if tc.wantSelected {
						require.ErrorIs(t, err, ErrVideoTaskPricing)
						require.Equal(t, 1, transport.selected)
					}
					require.Equal(t, transport.selected, transport.released)
					require.Empty(t, billing.repo.tasks)
					require.Zero(t, billing.reserves)
					require.Zero(t, billing.captures)
					require.Zero(t, billing.releases)
					require.Empty(t, logs.logs)
					require.Zero(t, httpFixture.calls)
				})
			}
		})
	}
}

// 明确零价是有效合同，与缺价拒绝不同；正向对照证明测试确实可走到持久建账和一次 POST。
func TestVideoPricingSubmissionRegressionExplicitZeroIsAccepted(t *testing.T) {
	s, key, _, billing, _ := newVideoLifecycleFixture(BillingModeVideoToken)
	key.Group.Status = StatusActive
	zero := 0.
	key.Group.ModelPricing[0].VideoPrices = nil
	key.Group.ModelPricing[0].VideoFallbackPrice = &zero
	account := videoFixtureAccount(VideoEndpointCompat)
	account.GroupIDs = []int64{key.Group.ID}
	httpFixture := &videoHTTPFixture{status: 202, response: `{"id":"zero-price-task","status":"queued"}`}
	transport := &videoPricingAuditTransport{VideoUpstreamService: NewVideoUpstreamService(&OpenAIGatewayService{
		accountRepo: &videoSelectionFixture{accounts: []Account{*account}}, concurrencyService: NewConcurrencyService(nil), httpUpstream: httpFixture})}
	s.upstream = transport
	result, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model","resolution":"540p","duration":5}`), ContentType: "application/json", InboundProtocol: "unified"})
	require.NoError(t, err)
	require.NotEmpty(t, result.LocalTaskID)
	require.Len(t, billing.repo.tasks, 1)
	require.Equal(t, 1, billing.reserves)
	require.Equal(t, 1, httpFixture.calls)
	require.Equal(t, 1, transport.released)
	task, err := s.repo.Get(context.Background(), result.LocalTaskID)
	require.NoError(t, err)
	require.True(t, task.Quote.FallbackUsed)
	require.Zero(t, task.Quote.UnitPrice)
	require.Zero(t, task.Hold.HoldAmount)
}
