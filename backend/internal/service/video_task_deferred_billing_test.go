package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 空上限冻结后结算模式，重启或修改账号配置都不能把任务重新当成有预算上限的任务。
func TestVideoTaskDeferredBillingSurvivesRestartAndSettlesActualUsageOnce(t *testing.T) {
	for _, absent := range []bool{true, false} {
		name := "null"
		if absent {
			name = "absent"
		}
		t.Run(name, func(t *testing.T) {
			s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
			if absent {
				delete(upstream.selection.Account.Credentials, "video_max_output_tokens")
			} else {
				upstream.selection.Account.Credentials["video_max_output_tokens"] = nil
			}
			req := VideoTaskSubmitRequest{Body: []byte(`{"model":"video-model"}`), IdempotencyKey: "unbounded"}
			response, err := s.Submit(context.Background(), key, req)
			require.NoError(t, err)
			task, err := s.repo.Get(context.Background(), response.LocalTaskID)
			require.NoError(t, err)
			require.True(t, task.Hold.VideoDeferredBilling)
			require.Equal(t, "reserved", task.BillingStatus)
			require.Zero(t, task.Hold.BaseAmountUSD)
			require.Zero(t, task.Hold.HoldAmount)
			require.Equal(t, 1.0, task.Quote.UnitPrice)
			require.Nil(t, task.Metadata.Tokens, "预算估算的零不能成为真实用量")
			require.Nil(t, task.BillingResult)
			upstream.selection.Account.Credentials["video_max_output_tokens"] = 1
			restarted := *s
			count := int64(2_000_000)
			upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Metadata: VideoRequestMetadata{Tokens: &count}, Body: []byte(`{"status":"completed"}`)}
			restarted.advance(context.Background(), task)
			fresh, err := s.repo.Get(context.Background(), task.ID)
			require.NoError(t, err)
			require.True(t, fresh.Hold.VideoDeferredBilling)
			require.Equal(t, "settled", fresh.BillingStatus)
			require.Equal(t, 4.0, fresh.BillingResult.ActualAmountUSD)
			require.True(t, fresh.EffectsDone)
			require.Len(t, billing.captureCommands, 1)
			require.True(t, billing.captureCommands[0].VideoDeferredBilling)
			require.Equal(t, 2.0, billing.captureCommands[0].ActualBaseAmountUSD)
			require.Zero(t, billing.captureCommands[0].BaseAmountUSD)
			require.Equal(t, 4.0, logs.logs["video_capture:"+task.ID].ActualCost)
			require.Equal(t, int(count), logs.logs["video_capture:"+task.ID].OutputTokens)
			restarted.advance(context.Background(), fresh)
			_, err = restarted.Submit(context.Background(), key, req)
			require.NoError(t, err)
			require.Equal(t, 1, billing.reserves)
			require.Equal(t, 1, billing.captures)
			require.Equal(t, 1, upstream.submitted)
			require.Len(t, logs.logs, 1)
		})
	}
}

// 无上限只取消金额预留，不取消可信用量及最终资金检查。
func TestVideoTaskDeferredBillingMissingUsageAndRejectedCaptureRemainUnsettled(t *testing.T) {
	s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
	mediaRepo := &videoDeferredMediaRepo{}
	s.media = NewMediaTaskService(mediaRepo)
	upstream.selection.Account.Credentials["video_max_output_tokens"] = nil
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Body: []byte(`{"status":"completed"}`)}
	s.advance(context.Background(), task)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.Empty(t, billing.captureCommands)
	require.Empty(t, logs.logs)
	count := int64(300_000)
	upstream.poll.Metadata.Tokens = &count
	billing.captureErr = errors.New("insufficient funds or quota")
	s.advance(context.Background(), task)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.Len(t, billing.captureCommands, 1)
	require.NotEmpty(t, task.ErrorMessage)
	require.InDelta(t, .3, billing.captureCommands[0].ActualBaseAmountUSD, 1e-9)
	require.Zero(t, billing.captures)
	require.Empty(t, logs.logs)
	// 补足余额或额度后只补结算，不重发上游生成。
	billing.captureErr = nil
	s.advance(context.Background(), task)
	require.Equal(t, "settled", task.BillingStatus)
	require.Empty(t, task.ErrorMessage)
	require.Empty(t, mediaRepo.observations[len(mediaRepo.observations)-1].ErrorMessage)
	fresh, err := s.repo.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Empty(t, fresh.ErrorMessage)
	require.NotContains(t, string(videoTaskResponse(fresh, false).Body), "待对账")
	mediaRepo.record = fresh
	projected := &MediaTask{Source: "video", ErrorMessage: "视频任务费用待对账"}
	s.media.enrichVideoBilling(context.Background(), projected)
	require.Empty(t, projected.ErrorMessage, "列表不能保留首次完成时被冻结的对账错误")
	require.Equal(t, "settled", projected.VideoBilling.Status)
	require.Equal(t, 1, upstream.submitted)
	require.Equal(t, 1, billing.captures)
	require.Len(t, logs.logs, 1)
}

func TestVideoTaskDeferredBillingDoesNotOverrideLegacyHoldOrFreePrice(t *testing.T) {
	t.Run("legacy_hold", func(t *testing.T) {
		s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideoToken)
		response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
		require.NoError(t, err)
		task, err := s.repo.Get(context.Background(), response.LocalTaskID)
		require.NoError(t, err)
		restoreLegacyVideoLifecycleHold(s, task)
		require.False(t, task.Hold.VideoDeferredBilling)
		require.Equal(t, 1.0, task.Hold.BaseAmountUSD)
		delete(upstream.selection.Account.Credentials, "video_max_output_tokens")
		count := int64(2_000_000)
		upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Metadata: VideoRequestMetadata{Tokens: &count}, Body: []byte(`{"status":"completed"}`)}
		s.advance(context.Background(), task)
		require.Equal(t, "reconciliation", task.BillingStatus)
		require.Zero(t, billing.captures)
		require.False(t, billing.captureCommands[0].VideoDeferredBilling)
	})
	t.Run("explicit_free", func(t *testing.T) {
		s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideoToken)
		price := 0.0
		key.Group.ModelPricing[0].VideoPrices[0].Price = &price
		delete(upstream.selection.Account.Credentials, "video_max_output_tokens")
		response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
		require.NoError(t, err)
		task, err := s.repo.Get(context.Background(), response.LocalTaskID)
		require.NoError(t, err)
		require.True(t, task.Hold.VideoDeferredBilling)
		upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Body: []byte(`{"status":"completed"}`)}
		s.advance(context.Background(), task)
		require.Equal(t, "settled", task.BillingStatus)
		require.Zero(t, task.BillingResult.ActualAmountUSD)
		require.Equal(t, 1, billing.captures)
	})
}

func TestVideoTaskDeferredBillingIgnoresObsoleteAccountTokenBudget(t *testing.T) {
	for _, value := range []any{0, -1, 1.5, 1e12 + 1, "100", "", false, []any{}, map[string]any{}, math.NaN(), math.Inf(1)} {
		s, key, upstream, billing, _ := newVideoLifecycleFixture(BillingModeVideoToken)
		upstream.selection.Account.Credentials["video_max_output_tokens"] = value
		_, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
		require.NoError(t, err, "旧 Token 预算字段不再影响新任务: %v", value)
		require.Equal(t, 1, billing.reserves)
		require.Equal(t, 1, upstream.submitted)
	}
}

func TestVideoTaskDeferredBillingReleasesConfirmedFailureWithoutCharge(t *testing.T) {
	s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
	delete(upstream.selection.Account.Credentials, "video_max_output_tokens")
	upstream.submit = &VideoUpstreamResponse{StatusCode: 400, Body: []byte(`{"error":"rejected"}`)}
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	require.True(t, task.Hold.VideoDeferredBilling)
	require.Equal(t, "released", task.BillingStatus)
	require.Equal(t, 1, billing.releases)
	require.Zero(t, billing.captures)
	require.Empty(t, logs.logs)
}

func TestVideoTaskDeferredBillingAcceptsConfirmedZeroUsage(t *testing.T) {
	s, key, upstream, billing, logs := newVideoLifecycleFixture(BillingModeVideoToken)
	upstream.selection.Account.Credentials["video_max_output_tokens"] = nil
	response, err := s.Submit(context.Background(), key, VideoTaskSubmitRequest{Body: []byte(`{}`)})
	require.NoError(t, err)
	task, err := s.repo.Get(context.Background(), response.LocalTaskID)
	require.NoError(t, err)
	zero := int64(0)
	upstream.poll = &VideoUpstreamResponse{StatusCode: 200, Status: "completed", Metadata: VideoRequestMetadata{Tokens: &zero}, Body: []byte(`{"status":"completed"}`)}
	s.advance(context.Background(), task)
	require.Equal(t, "settled", task.BillingStatus)
	require.Zero(t, task.BillingResult.ActualAmountUSD)
	require.Equal(t, 1, billing.captures)
	require.Len(t, logs.logs, 1)
	require.Zero(t, logs.logs["video_capture:"+task.ID].OutputTokens)
}

type videoDeferredMediaRepo struct {
	MediaTaskRepository
	record       *VideoTaskRecord
	observations []MediaTaskObservation
}

func (r videoDeferredMediaRepo) GetVideoTask(context.Context, *MediaTask) (*VideoTaskRecord, error) {
	return r.record, nil
}

func (r *videoDeferredMediaRepo) Observe(_ context.Context, observation MediaTaskObservation) error {
	r.observations = append(r.observations, observation)
	return nil
}

// 待结算的零预留只展示预算事实，不能提前产生零元实际费用。
func TestVideoDeferredBillingProjectionWaitsForActualSettlement(t *testing.T) {
	record := &VideoTaskRecord{BillingStatus: "reserved", Quote: &VideoPriceQuote{Mode: BillingModeVideoToken, UnitPrice: 1}, Hold: BatchImageBalanceHoldCommand{VideoDeferredBilling: true}}
	s := NewMediaTaskService(&videoDeferredMediaRepo{record: record})
	task := &MediaTask{Source: "video"}
	s.enrichVideoBilling(context.Background(), task)
	require.True(t, task.VideoBilling.DeferredBilling)
	require.Zero(t, task.VideoBilling.ReservedAmount)
	require.Equal(t, 1.0, task.VideoBilling.UnitPrice)
	require.Nil(t, task.VideoBilling.ActualAmount)
	require.Nil(t, task.ActualCost)
	raw, err := json.Marshal(task.VideoBilling)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"deferred_billing":true`)
	require.NotContains(t, string(raw), `"actual_amount"`)
	record.BillingStatus = "settled"
	record.BillingResult = &BatchImageBalanceHoldResult{ActualAmountUSD: .6}
	s.enrichVideoBilling(context.Background(), task)
	require.NotNil(t, task.VideoBilling.ActualAmount)
	require.Equal(t, .6, *task.VideoBilling.ActualAmount)
	require.Equal(t, .6, *task.ActualCost)
}

// 固定旧指纹输入，验证增加标记不会改变已受理的普通视频及图片任务的幂等身份。
func TestVideoDeferredBillingFingerprintPreservesLegacyAndPersistsTrue(t *testing.T) {
	command := BatchImageBalanceHoldCommand{UserID: 1, ActorUserID: 2, APIKeyID: 3, BatchID: "vid_fingerprint", PricingSnapshotVersion: 3,
		ActualBaseAmountUSD: .5, SubscriptionRateMultiplier: 1, SubscriptionRateMultiplierScale: 1, BalanceRateMultiplier: 2, SettlementRateScale: 1,
		APIKeyBillingMode: APIKeyBillingModeBalance, ReservedAt: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), RequestPayloadHash: "payload"}
	legacyPayload := "1|2|0|3|vid_fingerprint|3|0.0000000000|0.5000000000|1.0000000000|1.0000000000|2.0000000000|1.0000000000|false|2026-09-30T00:00:00Z|balance|0|payload"
	legacyHash := sha256.Sum256([]byte(legacyPayload))
	command.Normalize()
	require.Equal(t, hex.EncodeToString(legacyHash[:]), command.RequestFingerprint)
	legacyFingerprint := command.RequestFingerprint
	command.VideoEntity = true
	command.RequestFingerprint = ""
	command.Normalize()
	require.Equal(t, legacyFingerprint, command.RequestFingerprint)
	command.VideoDeferredBilling = true
	command.RequestFingerprint = ""
	command.Normalize()
	require.NotEqual(t, legacyFingerprint, command.RequestFingerprint)
	raw, err := json.Marshal(&VideoTaskRecord{Hold: command})
	require.NoError(t, err)
	var restored VideoTaskRecord
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.True(t, restored.Hold.VideoDeferredBilling)
	restored.Hold.RequestFingerprint = ""
	restored.Hold.Normalize()
	require.Equal(t, command.RequestFingerprint, restored.Hold.RequestFingerprint)
}
