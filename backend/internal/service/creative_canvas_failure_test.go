//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 真实失败分类沿执行器、落库错误码及画布投影传递，未知原文绝不直接显示。
func TestCreativeProviderFailureReasonSurvivesProjection(t *testing.T) {
	for _, tc := range []struct {
		name, message, code, contains string
		status                        int
	}{
		{"审核", "content_policy_violation: private-prompt https://private.test sk-secret", "PROVIDER_CONTENT_BLOCKED", "内容审核", 400},
		{"账号认证", "private credential", "PROVIDER_AUTH_FAILED", "账号认证失败", 401},
		{"上游余额", "insufficient_quota", "PROVIDER_QUOTA_EXHAUSTED", "上游账号余额", 429},
		{"模型", "model_not_found", "PROVIDER_MODEL_UNAVAILABLE", "模型映射", 404},
		{"参数", "invalid parameters", "PROVIDER_INVALID_PARAMS", "生成参数", 422},
		{"限流", "unknown", "PROVIDER_RATE_LIMITED", "频率或并发", 429},
		{"超时", "unknown", "PROVIDER_TIMEOUT", "超时", 504},
		{"上游故障", "private-prompt", "PROVIDER_UNAVAILABLE", "暂不可用", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := creativeHTTPStatusError(tc.status, tc.message)
			code, message := creativeExecuteErrorParts(err)
			require.Equal(t, tc.code, code)
			require.Contains(t, message, tc.contains)
			require.Contains(t, creativeCanvasExecutionFailureMessage("image", code), tc.contains)
			for _, secret := range []string{"private", "sk-secret", "https://"} {
				require.NotContains(t, message, secret)
				require.NotContains(t, err.Error(), secret)
			}
			require.Equal(t, tc.status == 429 || tc.status >= 500, err.Retryable)
		})
	}
	require.Contains(t, creativeCanvasExecutionFailureMessage("image", "UPSTREAM_STATUS_401"), "账号认证")
	require.Contains(t, creativeCanvasExecutionFailureMessage("image", "UPSTREAM_STATUS_504"), "超时")
	require.Contains(t, creativeCanvasExecutionFailureMessage("audio", "UPSTREAM_STATUS_404"), "语音服务端点")
	require.Empty(t, creativeCanvasExecutionFailureMessage("image", "private-secret"))
	require.Equal(t, "", creativeProviderFailureCode(200, strings.Repeat("private-secret", 1000)))
}

// 只有原账本已释放的失败结果才带退款说明，重复查询不重新预扣、捕获或释放。
func TestCreativeVideoFailureReasonUsesOriginalReleasedTask(t *testing.T) {
	for _, tc := range []struct{ name, raw, code, message string }{
		{"审核", `{"error":{"code":"content_policy_violation","message":"private-prompt"}}`, "CREATIVE_VIDEO_PROVIDER_CONTENT_BLOCKED", "内容审核"},
		{"参数", `{"output":{"code":"invalid_parameter","message":"private-url"}}`, "CREATIVE_VIDEO_PROVIDER_INVALID_PARAMS", "生成参数"},
		{"限额", `{"base_resp":{"status_msg":"insufficient balance"}}`, "CREATIVE_VIDEO_PROVIDER_QUOTA_EXHAUSTED", "上游账号余额"},
		{"未知", `{"error":{"message":"sk-secret private-prompt https://private.test"}}`, "CREATIVE_VIDEO_FAILED", "未返回可识别的错误原因"},
		{"排除提示词", `{"prompt":"content_policy_violation","video_url":"https://private.test"}`, "CREATIVE_VIDEO_FAILED", "未返回可识别"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e, upstream, billing, _ := creativeVideoTestFixture(t)
			e.TaskID = "vid_saved"
			task := &VideoTaskRecord{ID: e.TaskID, APIKeyID: 2, UserID: e.UserID, GroupID: e.Params.GroupID, IdempotencyKey: "creative_video:" + e.ID, Status: "failed", BillingStatus: "released", NextPollAt: time.Now().Add(time.Hour), RawResponse: []byte(tc.raw), ResponseStatus: 200}
			task.Target = VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointCompat, Model: e.Params.Model, CreatePath: "/v1/video/generations", AccountID: 4}
			s.repo.(*videoLifecycleRepo).tasks[task.ID] = task
			s.content = &creativeVideoContentStub{}
			for range 2 {
				result, err := s.Poll(context.Background(), e)
				require.NoError(t, err)
				require.Equal(t, "failed", result.Status)
				require.Equal(t, tc.code, result.ErrorCode)
				require.Contains(t, result.ErrorMessage, tc.message)
				require.Contains(t, result.ErrorMessage, "预扣已释放")
				require.NotContains(t, result.ErrorMessage, "private")
				require.NotContains(t, result.ErrorMessage, "sk-secret")
				require.Equal(t, result.ErrorMessage, creativeCanvasExecutionFailureMessage("video", result.ErrorCode))
			}
			require.Zero(t, upstream.submitted)
			require.Zero(t, billing.reserves)
			require.Zero(t, billing.captures)
			require.Zero(t, billing.releases)
		})
	}
}

// 短暂轮询失败不能覆盖已经明确的图片人工核对说明，也不能触发重新生成。
func TestCreativeImageReconciliationSurvivesPollFailure(t *testing.T) {
	s, repo, params := canvasBatchFixture(t)
	x := &canvasProgressExecutor{err: errors.New("private transport failure")}
	s.SetExecutor("image", x)
	e := &CreativeCanvasExecution{ID: "existing", Kind: "image", Params: params, Status: "running", TaskID: "crun_existing", ErrorCode: "CREATIVE_IMAGE_SUBMISSION_UNCONFIRMED", ErrorMessage: "请联系管理员核对，预扣未结算"}
	s.reconcileOne(context.Background(), e)
	require.Equal(t, "running", repo.saved.Status)
	require.Equal(t, e.ErrorCode, repo.saved.ErrorCode)
	require.Equal(t, e.ErrorMessage, repo.saved.ErrorMessage)
	require.Zero(t, x.submits)
}
