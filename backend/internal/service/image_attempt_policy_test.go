package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 同一请求被并发恢复也只能领取一次发送权，明确拒绝后只允许一个不同账号取得备用机会。
func TestImageAttemptPolicy256ConcurrentClaimsCannotMultiplySends(t *testing.T) {
	ctx, cancel := WithImageAttemptPolicy(context.Background(), time.Minute)
	defer cancel()
	claim := func(offset int64) int {
		start := make(chan struct{})
		successes := make(chan int64, 128)
		var wg sync.WaitGroup
		for id := int64(1); id <= 128; id++ {
			wg.Go(func() {
				<-start
				if BeginImageAttempt(ctx, offset+id, "actual-image-model", "generate") == nil {
					successes <- offset + id
				}
			})
		}
		close(start)
		wg.Wait()
		close(successes)
		return len(successes)
	}
	require.Equal(t, 1, claim(0))
	RecordImageAttemptDecision(ctx, ImageAttemptDecision{Outcome: ImageAttemptRejected, RetryDifferentAccount: true})
	require.Equal(t, 1, claim(1000))
	require.Equal(t, 2, ImageAttemptSendCount(ctx))
	RecordImageAttemptDecision(ctx, ImageAttemptDecision{Outcome: ImageAttemptRejected, RetryDifferentAccount: true})
	require.Error(t, BeginImageAttempt(ctx, 3000, "actual-image-model", "generate"))
}

// 同一个逻辑请求最多两个不同账号；内部同号重试不能绕过预算。
func TestImageAttemptPolicyBudgetAndUnknownGuard(t *testing.T) {
	ctx, cancel := WithImageAttemptPolicy(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, BeginImageAttempt(ctx, 1, "image-model", "generate"))
	require.Error(t, BeginImageAttempt(ctx, 2, "image-model", "generate"), "未知结果不能自动开始第二次生成")
	RecordImageAttemptDecision(ctx, ImageAttemptDecision{Outcome: ImageAttemptRejected, RetryDifferentAccount: true})
	require.Error(t, BeginImageAttempt(ctx, 1, "image-model", "generate"), "禁止同号内部重发")
	require.NoError(t, BeginImageAttempt(ctx, 2, "image-model", "generate"))
	RecordImageAttemptDecision(ctx, ImageAttemptDecision{Outcome: ImageAttemptRejected, RetryDifferentAccount: true})
	require.Error(t, BeginImageAttempt(ctx, 3, "image-model", "generate"))
	require.Equal(t, 2, ImageAttemptSendCount(ctx))
	// 管理员指定账号测试未挂策略，保留既有调用边界。
	require.NoError(t, BeginImageAttempt(context.Background(), 1, "image-model", "generate"))
}

func TestImageAttemptPolicyDeadlineAndEvidenceAreMonotonic(t *testing.T) {
	ctx, cancel := WithImageAttemptPolicy(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	other, stop := WithImageAttemptPolicy(ctx, time.Hour)
	defer stop()
	second, _ := other.Deadline()
	require.Equal(t, deadline, second)
	detached, release := DetachImageAttemptContext(other)
	defer release()
	third, _ := detached.Deadline()
	require.Equal(t, deadline, third)
	RecordImageAttemptDecision(ctx, ImageAttemptDecision{Outcome: ImageAttemptAccepted, Reason: "usage"})
	err := RecordImageAttemptFailure(ctx, io.EOF, ImageAttemptDecision{Outcome: ImageAttemptRejected, RetryDifferentAccount: true})
	decision, ok := ImageAttemptDecisionForError(err)
	require.True(t, ok)
	require.Equal(t, ImageAttemptAccepted, decision.Outcome)
	require.False(t, ImageAttemptCanSwitch(decision))
	CancelImageAttemptPolicy(ctx)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestImageAttemptHTTPFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body         string
		outcome      ImageAttemptOutcome
		retry, fault bool
	}{
		{"rate_reject", 429, `{"error":{"code":"rate_limit_exceeded"}}`, ImageAttemptRejected, true, true},
		{"rate_reject_generic_shell", 429, `{"error":{"type":"invalid_request_error"}}`, ImageAttemptRejected, true, true},
		{"model_missing_generic_shell", 404, `{"error":{"type":"invalid_request_error","message":"The model image-model does not exist"}}`, ImageAttemptRejected, true, true},
		{"bad_image", 400, `{"error":{"code":"invalid_image"}}`, ImageAttemptRejected, false, false},
		{"safety", 403, `{"error":{"code":"content_policy_violation"}}`, ImageAttemptRejected, false, false},
		{"gemini_safety_200", 200, `{"promptFeedback":{"blockReason":"SAFETY"}}`, ImageAttemptRejected, false, false},
		{"generic_server_failure", 502, `{"error":{"type":"server_error"}}`, ImageAttemptUnknown, false, true},
		{"server_failure_invalid_request_shell", 503, `{"error":{"type":"invalid_request_error"}}`, ImageAttemptUnknown, false, true},
		{"upstream_timeout", 408, `{}`, ImageAttemptUnknown, false, true},
		{"accepted_task_even_429", 429, `{"task_id":"accepted-1"}`, ImageAttemptAccepted, false, false},
		{"response_id_even_429", 429, `{"response":{"id":"resp_accepted"},"error":{"code":"rate_limit_exceeded"}}`, ImageAttemptAccepted, false, false},
		{"job_id_even_429", 429, `{"id":"job_accepted","error":{"code":"rate_limit_exceeded"}}`, ImageAttemptAccepted, false, false},
		{"usage_even_reject", 429, `{"usage":{"output_tokens":1}}`, ImageAttemptAccepted, false, false},
		{"vertex_usage", 500, `{"usageMetadata":{"promptTokenCount":1}}`, ImageAttemptAccepted, false, false},
		{"vertex_output", 500, `{"candidates":[{"content":{"parts":[{"inlineData":{"data":"base64","mimeType":"image/png"}}]}}]}`, ImageAttemptResultReady, false, false},
		{"image_even_500", 500, `{"data":[{"b64_json":"base64"}]}`, ImageAttemptResultReady, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := ClassifyImageHTTPFailure(tc.status, []byte(tc.body))
			require.Equal(t, tc.outcome, d.Outcome)
			require.Equal(t, tc.retry, ImageAttemptCanSwitch(d))
			require.Equal(t, tc.fault, d.AttemptFault)
		})
	}
}

func TestImageAttemptTransportDoesNotGuessFromErrorMessage(t *testing.T) {
	for _, err := range []error{io.EOF, context.DeadlineExceeded, errors.New("dial failed after body upload"), &net.OpError{Op: "read", Err: io.EOF}} {
		require.Equal(t, ImageAttemptUnknown, ClassifyImageTransportFailure(err).Outcome)
		require.False(t, ImageAttemptCanSwitch(ClassifyImageTransportFailure(err)))
	}
	require.True(t, ImageAttemptCanSwitch(ClassifyImageTransportFailure(&net.OpError{Op: "dial", Err: errors.New("connection refused")})))
	require.False(t, ClassifyImageTransportFailure(context.Canceled).AttemptFault)
	require.False(t, ImageAttemptCanSwitch(ClassifyImageHTTPFailure(http.StatusOK, []byte(`{}`))))
}
