package service

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// imageCooldownTrackerStub 在冷却桩之上记录连续失败次数，模拟 Redis 计数。
type imageCooldownTrackerStub struct {
	imageCooldownServiceStub
	strikes map[string]int
	resets  int
}

func (s *imageCooldownTrackerStub) IncrImageModelFailures(_ context.Context, accountID int64, model, operation string, _ time.Duration) (int, error) {
	if s.strikes == nil {
		s.strikes = map[string]int{}
	}
	key := model + "/" + operation
	s.strikes[key]++
	return s.strikes[key], nil
}

func (s *imageCooldownTrackerStub) ResetImageModelFailures(_ context.Context, _ int64, model, operation string) error {
	s.resets++
	delete(s.strikes, model+"/"+operation)
	return nil
}

// 模拟每次失败时上一个窗口已经过期，才能观察逐级加长。
func (s *imageCooldownTrackerStub) GetImageModelCooldown(context.Context, int64, string, string) (time.Duration, error) {
	return 0, nil
}

// 分级冷却：模型不可用/凭据失效最长，429 固定，未知故障按连续次数逐级延长并封顶。
func TestImageModelCooldownPolicyGrading(t *testing.T) {
	policy := DefaultImageModelCooldownPolicy()
	unavailable := ImageAttemptDecision{Outcome: ImageAttemptRejected, RetryDifferentAccount: true, AttemptFault: true, Reason: "image_account_model_unavailable", UpstreamStatus: 404}
	require.Equal(t, time.Hour, policy.cooldownFor(unavailable, 1))
	require.Equal(t, time.Minute, policy.cooldownFor(ClassifyImageHTTPFailure(429, []byte(`{"error":{"code":"rate_limit_exceeded"}}`)), 5))
	require.Equal(t, time.Hour, policy.cooldownFor(ClassifyImageHTTPFailure(401, []byte(`{"error":{"message":"bad key"}}`)), 1))
	unknown := ClassifyImageHTTPFailure(502, []byte(`{"error":{"message":"bad gateway"}}`))
	require.Equal(t, 502, unknown.UpstreamStatus)
	require.Equal(t, 2*time.Minute, policy.cooldownFor(unknown, 1))
	require.Equal(t, 10*time.Minute, policy.cooldownFor(unknown, 2))
	require.Equal(t, 30*time.Minute, policy.cooldownFor(unknown, 3))
	require.Equal(t, 30*time.Minute, policy.cooldownFor(unknown, 9), "超过档位数后保持最后一档")
	transport := ImageAttemptDecision{Outcome: ImageAttemptUnknown, AttemptFault: true, Reason: "image_transport_unknown"}
	require.Equal(t, 2*time.Minute, policy.cooldownFor(transport, 0))
}

// 连续失败逐级加长，成功后计数清零；没有计数能力时退回第一档。
func TestRecordImageModelFailureEscalatesAndResets(t *testing.T) {
	store := &imageCooldownTrackerStub{}
	unknown := ClassifyImageHTTPFailure(503, nil)
	require.NoError(t, RecordImageModelFailure(context.Background(), store, 7, "img", "generate", unknown))
	require.Equal(t, 2*time.Minute, store.duration)
	require.NoError(t, RecordImageModelFailure(context.Background(), store, 7, "img", "generate", unknown))
	require.Equal(t, 10*time.Minute, store.duration)
	RecordImageModelSuccess(context.Background(), store, 7, "img", "generate")
	require.Equal(t, 1, store.resets)
	require.NoError(t, RecordImageModelFailure(context.Background(), store, 7, "img", "generate", unknown))
	require.Equal(t, 2*time.Minute, store.duration)

	plain := &imageCooldownServiceStub{}
	require.NoError(t, RecordImageModelFailure(context.Background(), plain, 7, "img", "generate", unknown))
	require.Equal(t, 2*time.Minute, plain.duration)
	// 窗口仍生效时不再写入或累加。
	require.NoError(t, RecordImageModelFailure(context.Background(), plain, 7, "img", "generate", unknown))
	require.Equal(t, 1, plain.writes)
	// 非图片操作不写冷却也不计数。
	require.NoError(t, RecordImageModelFailure(context.Background(), store, 7, "img", "chat", unknown))
	require.Equal(t, 1, store.strikes["img/generate"])
}

// 配置覆盖只替换填写的项，其余保留默认值。
func TestImageModelCooldownPolicyFromConfig(t *testing.T) {
	t.Cleanup(func() { SetImageModelCooldownPolicy(DefaultImageModelCooldownPolicy()) })
	SetImageModelCooldownPolicy(ImageModelCooldownPolicyFromConfig(config.ImageModelCooldownConfig{RateLimitedSeconds: 30, UnknownSeconds: []int{60, 0, 300}}))
	policy := currentImageModelCooldownPolicy()
	require.Equal(t, 30*time.Second, policy.RateLimited)
	require.Equal(t, []time.Duration{time.Minute, 5 * time.Minute}, policy.Unknown)
	require.Equal(t, time.Hour, policy.Unavailable)
	require.Equal(t, DefaultImageModelCooldownPolicy().Rejected, policy.Rejected)
}

// 调度阶段只在显式标记了图片操作的请求中查询冷却。
func TestImageCooldownAccountBlockedRequiresOperation(t *testing.T) {
	store := &imageCooldownServiceStub{duration: time.Minute}
	account := &Account{ID: 9, Platform: PlatformOpenAI}
	require.False(t, imageCooldownAccountBlocked(context.Background(), store, account, "gpt-image-2"))
	require.Zero(t, store.reads)
	ctx := WithImageModelOperation(context.Background(), "Generate")
	require.Equal(t, "generate", ImageModelOperationFromContext(ctx))
	require.True(t, imageCooldownAccountBlocked(ctx, store, account, "gpt-image-2"))
	require.Equal(t, "generate", store.operation)
	require.Empty(t, ImageModelOperationFromContext(WithImageModelOperation(context.Background(), "tts")))
}

// 换组只接受“本组未发送”或“确定未生成且可换号”两种证据。
func TestImageAttemptAllowsGroupFallback(t *testing.T) {
	require.True(t, ImageAttemptAllowsGroupFallback(context.Background()), "无图片预算的请求不受限制")

	ctx, cancel := WithImageAttemptPolicy(context.Background(), time.Minute)
	defer cancel()
	require.False(t, ImageAttemptAllowsGroupFallback(ctx), "缺少证据时禁止换组")
	MarkImageAttemptGroupUnsent(ctx)
	require.True(t, ImageAttemptAllowsGroupFallback(ctx))

	rejected, cancelRejected := WithImageAttemptPolicy(context.Background(), time.Minute)
	defer cancelRejected()
	require.NoError(t, BeginImageAttempt(rejected, 1, "img", "generate"))
	RecordImageAttemptDecision(rejected, ClassifyImageHTTPFailure(429, nil))
	require.True(t, ImageAttemptAllowsGroupFallback(rejected))

	unknown, cancelUnknown := WithImageAttemptPolicy(context.Background(), time.Minute)
	defer cancelUnknown()
	require.NoError(t, BeginImageAttempt(unknown, 1, "img", "generate"))
	MarkImageAttemptGroupUnsent(unknown)
	RecordImageAttemptDecision(unknown, ClassifyImageHTTPFailure(502, nil))
	require.False(t, ImageAttemptAllowsGroupFallback(unknown), "结果未知不能换组重发")
}
