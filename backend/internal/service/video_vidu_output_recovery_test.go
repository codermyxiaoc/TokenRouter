//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 生命周期夹具持久化往返模拟恢复；每个场景只允许一次生成和一次预留。
func newViduOutputRecoveryFixture(t *testing.T, adapter string, mode BillingMode, unknownDuration bool) (*VideoTaskService, *videoLifecycleUpstream, *videoLifecycleBilling, *videoLifecycleLogs, string) {
	t.Helper()
	svc, key, upstream, billing, logs := newVideoLifecycleFixture(mode)
	target := officialViduFixture("/ent/v2/img2video")
	target.Model, target.AccountID, target.ProtocolAdapter = "video-model", upstream.selection.Account.ID, adapter
	upstream.selection.Target = target
	if unknownDuration {
		upstream.selection.Metadata.DurationSeconds = 0
		upstream.selection.Account.Credentials["video_max_duration_seconds"] = 8
	}
	var err error
	upstream.submit, err = ExtractVideoProtocolResponse(target, 200, []byte(`{"task_id":"upstream-task","state":"created"}`), "")
	require.NoError(t, err)
	created, err := svc.Submit(context.Background(), key, VideoTaskSubmitRequest{Native: true, NativePath: target.CreatePath, InboundProtocol: "vidu", IdempotencyKey: "recovery",
		Body: []byte(`{"model":"video-model","images":["reference"],"resolution":"720p"}`)})
	require.NoError(t, err)
	require.Equal(t, 1, upstream.submitted)
	require.Equal(t, 1, billing.reserves)
	return svc, upstream, billing, logs, created.LocalTaskID
}

func advanceViduOutputRecovery(t *testing.T, svc *VideoTaskService, upstream *videoLifecycleUpstream, id, fragment string) *VideoTaskRecord {
	t.Helper()
	var err error
	upstream.poll, err = ExtractVideoProtocolResponse(upstream.selection.Target, 200, []byte(`{"id":"upstream-task","state":"success",`+fragment+`}`), "upstream-task")
	require.NoError(t, err)
	task, err := svc.repo.Get(context.Background(), id)
	require.NoError(t, err)
	restarted := *svc
	restarted.advance(context.Background(), task)
	task, err = svc.repo.Get(context.Background(), id)
	require.NoError(t, err)
	return task
}

func TestVideoViduOutputRecoverySettlesMissingArtifactExactlyOnce(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterLegacy, VideoProtocolAdapterVidu, VideoProtocolAdapterKemei} {
		for _, mode := range []BillingMode{BillingModeVideo, BillingModeVideoPerRequest} {
			for _, missing := range []string{`"creations":[]`, `"credits":4`, `"creations":[{"id":"asset"}]`} {
				t.Run(adapter+"/"+string(mode)+"/"+missing, func(t *testing.T) {
					// 按次模式特意不设置时长，防止恢复错误地依赖与收费无关的字段。
					svc, upstream, billing, logs, id := newViduOutputRecoveryFixture(t, adapter, mode, mode == BillingModeVideoPerRequest)
					task := advanceViduOutputRecovery(t, svc, upstream, id, missing)
					require.Equal(t, "reconciliation", task.BillingStatus)
					require.True(t, task.PricingMismatch)
					require.True(t, videoViduOutputErrorRecoverable(task.ResponseMetadataError))
					require.Zero(t, billing.captures+billing.releases)
					for range 3 {
						task = advanceViduOutputRecovery(t, svc, upstream, id, `"creations":[{"id":"asset","url":"https://cdn.example/video.mp4"}]`)
					}
					require.Equal(t, "settled", task.BillingStatus)
					require.False(t, task.PricingMismatch)
					require.Empty(t, task.ResponseMetadataError)
					require.Equal(t, 1, billing.captures)
					require.Zero(t, billing.releases)
					require.Equal(t, 1, billing.reserves)
					require.Equal(t, 1, upstream.submitted)
					require.Len(t, logs.logs, 1)
					want := 16.0
					if mode == BillingModeVideoPerRequest {
						want = 2
					}
					require.InDelta(t, want, task.BillingResult.ActualAmountUSD, 1e-9)
				})
			}
		}
	}
}

func TestVideoViduOutputRecoveryKeepsRealDimensionErrors(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterLegacy, VideoProtocolAdapterVidu, VideoProtocolAdapterKemei} {
		for _, tc := range []struct {
			name  string
			steps []string
		}{
			{"invalid_duration_with_missing_creation", []string{`"duration":-1,"creations":[]`}},
			{"invalid_resolution_with_missing_creation", []string{`"resolution":null,"creations":[]`}},
			{"resolution_conflict_with_missing_creation", []string{`"resolution":"1080p","creations":[]`}},
			{"changed_duration_with_missing_creation", []string{`"duration":12,"creations":[]`}},
			{"historical_resolution_conflict", []string{`"resolution":"1080p","creations":[{"url":"https://cdn.example/video.mp4"}]`, `"creations":[]`}},
			{"historical_invalid_duration", []string{`"duration":-1,"creations":[{"url":"https://cdn.example/video.mp4"}]`, `"creations":[]`}},
			{"resolution_conflict_during_recovery", []string{`"creations":[]`, `"resolution":"1080p","creations":[{"url":"https://cdn.example/video.mp4"}]`}},
			{"duration_change_during_recovery", []string{`"creations":[]`, `"duration":12,"creations":[{"url":"https://cdn.example/video.mp4"}]`}},
			{"invalid_duration_overwritten_by_missing_url", []string{`"duration":-1,"creations":[{"id":"asset"}]`}},
			{"multiple_artifacts_are_not_missing", []string{`"creations":[{"url":"https://cdn.example/a.mp4"},{"url":"https://cdn.example/b.mp4"}]`}},
			{"malformed_creation_is_not_missing", []string{`"creations":[123]`}},
			{"malformed_url_is_not_missing", []string{`"creations":[{"url":123}]`}},
		} {
			t.Run(adapter+"/"+tc.name, func(t *testing.T) {
				svc, upstream, billing, logs, id := newViduOutputRecoveryFixture(t, adapter, BillingModeVideo, false)
				for _, fragment := range tc.steps {
					task := advanceViduOutputRecovery(t, svc, upstream, id, fragment)
					require.True(t, task.PricingMismatch)
				}
				for range 2 {
					task := advanceViduOutputRecovery(t, svc, upstream, id, `"creations":[{"url":"https://cdn.example/video.mp4"}]`)
					require.True(t, task.PricingMismatch)
					require.Equal(t, "reconciliation", task.BillingStatus)
				}
				require.Zero(t, billing.captures+billing.releases)
				require.Empty(t, logs.logs)
				// 真正提供明确计费维度后仍可恢复，不能把安全保护变成永久无法处理。
				task := advanceViduOutputRecovery(t, svc, upstream, id, `"duration":8,"resolution":"720p","creations":[{"url":"https://cdn.example/video.mp4"}]`)
				require.Equal(t, "settled", task.BillingStatus)
				require.Equal(t, 1, billing.captures)
			})
		}
	}
}

func TestVideoViduOutputRecoveryPerRequestIgnoresValidDurationChanges(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterLegacy, VideoProtocolAdapterVidu, VideoProtocolAdapterKemei} {
		t.Run(adapter, func(t *testing.T) {
			svc, upstream, billing, logs, id := newViduOutputRecoveryFixture(t, adapter, BillingModeVideoPerRequest, false)
			task := advanceViduOutputRecovery(t, svc, upstream, id, `"duration":12,"creations":[]`)
			require.True(t, videoViduOutputErrorRecoverable(task.ResponseMetadataError))
			task = advanceViduOutputRecovery(t, svc, upstream, id, `"creations":[{"url":"https://cdn.example/video.mp4"}]`)
			require.Equal(t, "settled", task.BillingStatus)
			require.Equal(t, 1, billing.captures)
			require.InDelta(t, 2, task.BillingResult.ActualAmountUSD, 1e-9)
			require.Len(t, logs.logs, 1)
		})
	}
}

func TestVideoViduOutputRecoveryNeverInventsUsageOrAcceptsUnsafeMedia(t *testing.T) {
	for _, adapter := range []string{VideoProtocolAdapterLegacy, VideoProtocolAdapterVidu, VideoProtocolAdapterKemei} {
		for _, tc := range []struct {
			name            string
			mode            BillingMode
			unknownDuration bool
			completion      string
		}{
			{"missing_tokens", BillingModeVideoToken, false, `"duration":8,"resolution":"720p","usage":{"output_tokens":999},"credits":4,"creations":[{"url":"https://cdn.example/video.mp4"}]`},
			{"missing_duration", BillingModeVideo, true, `"creations":[{"url":"https://cdn.example/video.mp4"}]`},
			{"unsafe_url", BillingModeVideo, false, `"creations":[{"url":"javascript:alert(1)"}]`},
		} {
			t.Run(adapter+"/"+tc.name, func(t *testing.T) {
				svc, upstream, billing, logs, id := newViduOutputRecoveryFixture(t, adapter, tc.mode, tc.unknownDuration)
				advanceViduOutputRecovery(t, svc, upstream, id, `"creations":[]`)
				for range 3 {
					task := advanceViduOutputRecovery(t, svc, upstream, id, tc.completion)
					require.Equal(t, "reconciliation", task.BillingStatus)
					require.True(t, task.PricingMismatch)
				}
				require.Zero(t, billing.captures+billing.releases)
				require.Empty(t, logs.logs)
				require.Equal(t, 1, upstream.submitted)
			})
		}
	}
}
