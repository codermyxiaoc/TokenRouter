//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 使用真实选账号、模型映射和协议适配，随后由持久化往返夹具验证完整资金生命周期。
func newSeedanceFramesLifecycle(t *testing.T, mode BillingMode, prepay bool, adapter string) (*VideoTaskService, *APIKey, *videoLifecycleUpstream, *videoLifecycleBilling, *videoLifecycleLogs, VideoTaskSubmitRequest) {
	t.Helper()
	svc, key, upstream, billing, logs := newVideoLifecycleFixture(mode)
	key.Group.Status, key.Group.RateMultiplier = StatusActive, 1
	if prepay {
		price := .3
		key.Group.ModelPricing[0].VideoTokenPrepay = &VideoTokenPrepayConfig{PricePerSecond: &price}
	}
	account := videoFixtureAccount(VideoEndpointSeedance)
	account.GroupIDs = []int64{key.Group.ID}
	account.Credentials["model_mapping"] = map[string]any{"video-model": "doubao-seedance-1-0-pro-250528"}
	account.Credentials["video_endpoint_adapters"] = map[string]string{"seedance": adapter}
	transport := NewVideoUpstreamService(&OpenAIGatewayService{accountRepo: &videoSelectionFixture{accounts: []Account{*account}}, concurrencyService: NewConcurrencyService(nil)})
	request := VideoTaskSubmitRequest{Native: true, InboundProtocol: "seedance", NativePath: "/api/v3/contents/generations/tasks", IdempotencyKey: "seedance-frames",
		Body: []byte(`{"model":"video-model","resolution":"720p","duration":2,"frames":289,"content":[{"type":"text","text":"隔离帧数计费测试"}]}`)}
	selection, err := transport.SelectAccount(context.Background(), key, request)
	require.NoError(t, err)
	require.Equal(t, "doubao-seedance-1-0-pro-250528", selection.Target.Model)
	require.Equal(t, int64(289), gjson.GetBytes(selection.Body, "frames").Int())
	require.Equal(t, int64(2), gjson.GetBytes(selection.Body, "duration").Int(), "仅修正计费事实，不改写供应商原始帧数合同")
	upstream.selection = selection
	svc.accounts = videoLifecycleAccounts{account: account}
	return svc, key, upstream, billing, logs, request
}

// 明确终态通过正式响应解析器进入恢复器，不能直接注入已经计算好的费用或时长。
func advanceSeedanceFrames(t *testing.T, svc *VideoTaskService, upstream *videoLifecycleUpstream, id, fields string) *VideoTaskRecord {
	t.Helper()
	var err error
	upstream.poll, err = ExtractVideoProtocolResponse(upstream.selection.Target, 200,
		[]byte(`{"id":"upstream-task","status":"succeeded","content":{"video_url":"https://cdn.example/video.mp4"},`+fields+`}`), "upstream-task")
	require.NoError(t, err)
	task, err := svc.repo.Get(context.Background(), id)
	require.NoError(t, err)
	restarted := *svc
	restarted.advance(context.Background(), task)
	task, err = svc.repo.Get(context.Background(), id)
	require.NoError(t, err)
	return task
}

// 帧数优先于较短 duration：按秒和 Token 预扣必须覆盖实际帧数，按次仍只收一次。
func TestVideoSeedanceFramesLifecycleReserveAndSettle(t *testing.T) {
	for _, tc := range []struct {
		mode      BillingMode
		prepay    bool
		hold, due float64
	}{
		{BillingModeVideo, false, 289.0 / 24, 289.0 / 24},
		{BillingModeVideoToken, true, 289.0 / 24 * .3, 1},
		{BillingModeVideoToken, false, 0, 1},
		{BillingModeVideoPerRequest, false, 1, 1},
	} {
		t.Run(fmt.Sprintf("%s/prepay_%t", tc.mode, tc.prepay), func(t *testing.T) {
			svc, key, upstream, billing, logs, request := newSeedanceFramesLifecycle(t, tc.mode, tc.prepay, VideoProtocolAdapterSeedance)
			require.InDelta(t, 289.0/24, upstream.selection.Metadata.DurationSeconds, 1e-10)
			created, err := svc.Submit(context.Background(), key, request)
			require.NoError(t, err)
			task, err := svc.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			require.InDelta(t, tc.hold, task.Hold.HoldAmount, 1e-9)
			if tc.prepay {
				require.InDelta(t, 289.0/24, task.Hold.VideoPrepayDurationSeconds, 1e-10)
			}
			for range 3 {
				task = advanceSeedanceFrames(t, svc, upstream, created.LocalTaskID, `"frames":289,"framespersecond":24,"resolution":"720p","usage":{"completion_tokens":1000000}`)
			}
			require.Equal(t, "settled", task.BillingStatus)
			require.InDelta(t, 289.0/24, task.Metadata.DurationSeconds, 1e-10)
			require.InDelta(t, tc.due, task.BillingResult.ActualAmountUSD, 1e-9)
			replayed, err := svc.Submit(context.Background(), key, request)
			require.NoError(t, err)
			require.Equal(t, created.LocalTaskID, replayed.LocalTaskID)
			require.Equal(t, 1, upstream.submitted)
			require.Equal(t, 1, billing.reserves)
			require.Equal(t, 1, billing.captures)
			require.Zero(t, billing.releases)
			require.Len(t, logs.logs, 1)
		})
	}
}

// 畸形或矛盾的帧数事实不得退回请求里的低时长扣费，必须等可靠维度恢复。
func TestVideoSeedanceFramesLifecycleMalformedResponseRecovery(t *testing.T) {
	for _, bad := range []string{
		`"frames":0,"framespersecond":24`, `"frames":-1,"framespersecond":24`,
		`"frames":"289","framespersecond":24`, `"frames":289.5,"framespersecond":24`,
		`"frames":null,"framespersecond":24`, `"frames":289,"framespersecond":0`,
		`"frames":289,"framespersecond":-24`, `"frames":289,"framespersecond":"24"`,
		`"frames":289,"framespersecond":null`, `"frames":289,"framespersecond":24,"duration":2`,
	} {
		t.Run(bad, func(t *testing.T) {
			svc, key, upstream, billing, logs, request := newSeedanceFramesLifecycle(t, BillingModeVideo, false, VideoProtocolAdapterSeedance)
			created, err := svc.Submit(context.Background(), key, request)
			require.NoError(t, err)
			task := advanceSeedanceFrames(t, svc, upstream, created.LocalTaskID, bad+`,"resolution":"720p"`)
			require.Equal(t, "reconciliation", task.BillingStatus)
			require.True(t, task.PricingMismatch)
			require.NotEmpty(t, task.ResponseMetadataError)
			for range 2 {
				task = advanceSeedanceFrames(t, svc, upstream, created.LocalTaskID, `"resolution":"720p"`)
				require.Equal(t, "reconciliation", task.BillingStatus)
			}
			require.Zero(t, billing.captures+billing.releases)
			require.Empty(t, logs.logs)
			task = advanceSeedanceFrames(t, svc, upstream, created.LocalTaskID, `"frames":289,"framespersecond":24,"resolution":"720p"`)
			require.Equal(t, "settled", task.BillingStatus)
			require.InDelta(t, 289.0/24, task.BillingResult.ActualAmountUSD, 1e-9)
			require.Equal(t, 1, billing.captures)
			require.Equal(t, 1, upstream.submitted)
		})
	}
}

// 非法 JSON 的伪终态不能提前捕获或释放；后续可靠完成仍只结算原任务一次。
func TestVideoSeedanceFramesLifecycleTrailingDataRecovery(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			svc, key, upstream, billing, logs, request := newSeedanceFramesLifecycle(t, BillingModeVideo, false, VideoProtocolAdapterSeedance)
			created, err := svc.Submit(context.Background(), key, request)
			require.NoError(t, err)
			upstream.poll, err = ExtractVideoProtocolResponse(upstream.selection.Target, 200,
				[]byte(fmt.Sprintf(`{"id":"upstream-task","status":%q,"frames":57,"framespersecond":24,"resolution":"720p"}{"frames":289}`, status)), "upstream-task")
			require.NoError(t, err)
			for range 2 {
				task, err := svc.repo.Get(context.Background(), created.LocalTaskID)
				require.NoError(t, err)
				svc.advance(context.Background(), task)
			}
			require.Zero(t, billing.captures+billing.releases)
			require.Empty(t, logs.logs)
			for range 2 {
				task := advanceSeedanceFrames(t, svc, upstream, created.LocalTaskID, `"frames":289,"framespersecond":24,"resolution":"720p"`)
				require.Equal(t, "settled", task.BillingStatus)
				require.InDelta(t, 289.0/24, task.BillingResult.ActualAmountUSD, 1e-9)
			}
			require.Equal(t, 1, upstream.submitted)
			require.Equal(t, 1, billing.captures)
			require.Zero(t, billing.releases)
			require.Len(t, logs.logs, 1)
		})
	}
}

// 明确失败仍释放原预扣，版本化帧数规则不能额外生成、扣费或改变退款幂等性。
func TestVideoSeedanceFramesLifecycleFailureRefund(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoToken, BillingModeVideoPerRequest} {
		t.Run(string(mode), func(t *testing.T) {
			svc, key, upstream, billing, logs, request := newSeedanceFramesLifecycle(t, mode, mode == BillingModeVideoToken, VideoProtocolAdapterSeedance)
			created, err := svc.Submit(context.Background(), key, request)
			require.NoError(t, err)
			upstream.poll, err = ExtractVideoProtocolResponse(upstream.selection.Target, 200, []byte(`{"id":"upstream-task","status":"failed","error":{"code":"GenerationFailed"}}`), "upstream-task")
			require.NoError(t, err)
			for range 3 {
				task, err := svc.repo.Get(context.Background(), created.LocalTaskID)
				require.NoError(t, err)
				svc.advance(context.Background(), task)
			}
			task, err := svc.repo.Get(context.Background(), created.LocalTaskID)
			require.NoError(t, err)
			require.Equal(t, "released", task.BillingStatus)
			require.Equal(t, 1, billing.releases)
			require.Zero(t, billing.captures)
			require.Equal(t, 1, upstream.submitted)
			require.Empty(t, logs.logs)
		})
	}
}

// 保留原有协议和可美不能被官方帧数新语义改变报价与最终费用。
func TestVideoSeedanceFramesLifecycleAdapterIsolation(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterLegacy, VideoProtocolAdapterKemei} {
		t.Run(adapter, func(t *testing.T) {
			svc, key, upstream, billing, _, request := newSeedanceFramesLifecycle(t, BillingModeVideo, false, adapter)
			require.Equal(t, float64(2), upstream.selection.Metadata.DurationSeconds)
			created, err := svc.Submit(context.Background(), key, request)
			require.NoError(t, err)
			task := advanceSeedanceFrames(t, svc, upstream, created.LocalTaskID, `"duration":2,"frames":289,"framespersecond":24,"resolution":"720p"`)
			require.Equal(t, "settled", task.BillingStatus)
			require.Equal(t, float64(2), task.BillingResult.ActualAmountUSD)
			require.Equal(t, 1, billing.captures)
		})
	}
}

// 升级前已按低 duration 预扣的旧任务不擅自补扣或退款；确认真实帧数后保留核对。
func TestVideoSeedanceFramesLifecycleHistoricalUnderReservation(t *testing.T) {
	svc, key, upstream, billing, logs, request := newSeedanceFramesLifecycle(t, BillingModeVideo, false, VideoProtocolAdapterSeedance)
	upstream.selection.Metadata.DurationSeconds = 2 // 模拟旧版本解析器已形成的预扣快照。
	created, err := svc.Submit(context.Background(), key, request)
	require.NoError(t, err)
	for range 3 {
		task := advanceSeedanceFrames(t, svc, upstream, created.LocalTaskID, `"frames":289,"framespersecond":24,"resolution":"720p"`)
		require.Equal(t, "reconciliation", task.BillingStatus)
		require.InDelta(t, 289.0/24, task.Metadata.DurationSeconds, 1e-10)
		require.Equal(t, float64(2), task.Hold.HoldAmount)
	}
	require.Zero(t, billing.captures+billing.releases)
	require.Equal(t, 1, billing.reserves)
	require.Equal(t, 1, upstream.submitted)
	require.Empty(t, logs.logs)
}
