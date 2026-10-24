//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 从真实适配器入口验证同一安全判定，不访问网络或生成付费图片。
func TestCreativeImageProviderAttemptBoundaries(t *testing.T) {
	for _, provider := range []struct {
		platform, model string
		count           int
	}{
		{PlatformOpenAI, "gpt-image-2", 1},
		{PlatformGemini, "gemini-3.1-flash-image-preview", 1},
		{PlatformGrok, "grok-imagine-image-2.0", 1},
		{PlatformOpenAI, "mj-6.1", 4},
	} {
		for _, tc := range []struct {
			name, body           string
			status               int
			transport            error
			readError            bool
			outcome              ImageAttemptOutcome
			switchAccount, fault bool
		}{
			{name: "参数拒绝", status: 400, body: `{"error":{"code":"invalid_image"}}`, outcome: ImageAttemptRejected},
			{name: "审核拒绝", status: 403, body: `{"error":{"code":"content_policy_violation"}}`, outcome: ImageAttemptRejected},
			{name: "认证拒绝", status: 401, body: `{"error":{"code":"invalid_api_key"}}`, outcome: ImageAttemptRejected, switchAccount: true, fault: true},
			{name: "限流拒绝", status: 429, body: `{"error":{"code":"rate_limit"}}`, outcome: ImageAttemptRejected, switchAccount: true, fault: true},
			{name: "泛服务异常", status: 503, body: `{"error":{"message":"busy"}}`, outcome: ImageAttemptUnknown, fault: true},
			{name: "已受理任务", status: 503, body: `{"task_id":"existing-task"}`, outcome: ImageAttemptAccepted},
			{name: "200已有用量", status: 200, body: `{"usage":{"total_tokens":10}}`, outcome: ImageAttemptAccepted},
			{name: "200无产物", status: 200, body: `{}`, outcome: ImageAttemptUnknown, fault: true},
			{name: "200读取失败", status: 200, readError: true, outcome: ImageAttemptUnknown, fault: true},
			{name: "连接前失败", transport: &net.OpError{Op: "dial", Err: errors.New("connection refused")}, outcome: ImageAttemptNotSent, switchAccount: true, fault: true},
			{name: "写入失败", transport: &net.OpError{Op: "write", Err: errors.New("reset")}, outcome: ImageAttemptUnknown, fault: true},
			{name: "客户端取消", transport: context.Canceled, outcome: ImageAttemptUnknown},
		} {
			t.Run(provider.model+"/"+tc.name, func(t *testing.T) {
				var body io.ReadCloser = io.NopCloser(strings.NewReader(tc.body))
				if tc.readError {
					body = passthroughErrReadCloser{err: io.ErrUnexpectedEOF}
				}
				upstream := &httpUpstreamRecorder{err: tc.transport, resp: &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: body}}
				cfg := &config.Config{}
				executor := &CreativeExecutor{cfg: cfg, gateway: &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}}
				_, err := executor.Execute(context.Background(), CreativeRun{Operation: CreativeOperationGenerate, RequestedOutputCount: provider.count, ImageSize: "1K", AspectRatio: "1:1"}, CreativeRunPayload{Prompt: "test image"}, &CreativeExecution{Account: &Account{ID: 1, Platform: provider.platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "local-key"}}, UpstreamModel: provider.model})
				require.Error(t, err)
				require.Len(t, upstream.requests, 1)
				decision, ok := ImageAttemptDecisionForError(err)
				require.True(t, ok)
				require.Equal(t, tc.outcome, decision.Outcome)
				require.Equal(t, tc.switchAccount, ImageAttemptCanSwitch(decision))
				require.Equal(t, tc.fault, decision.AttemptFault)
			})
		}
	}
}

func TestCreativeImageCancelledPreparationAndReadDoNotPenalizeAccount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range []error{creativeImageReadFailure(ctx), creativeImageAccountPreparationFailure(ctx, context.Canceled)} {
		decision, ok := ImageAttemptDecisionForError(err)
		require.True(t, ok)
		require.False(t, decision.AttemptFault)
		require.False(t, ImageAttemptCanSwitch(decision))
	}
}

func TestCreativeImageLocalContractFailureDoesNotSend(t *testing.T) {
	upstream := &httpUpstreamRecorder{}
	cfg := &config.Config{}
	executor := &CreativeExecutor{cfg: cfg, gateway: &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}}
	_, err := executor.Execute(context.Background(), CreativeRun{Operation: CreativeOperationGenerate, RequestedOutputCount: 1}, CreativeRunPayload{Prompt: "test"}, &CreativeExecution{Account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, UpstreamModel: "mj-6.1"})
	require.Error(t, err)
	require.Empty(t, upstream.requests)
	decision, ok := ImageAttemptDecisionForError(err)
	require.True(t, ok)
	require.Equal(t, ImageAttemptNotSent, decision.Outcome)
	require.False(t, decision.AttemptFault)
	require.False(t, ImageAttemptCanSwitch(decision))
}

func TestCreativeImageBudgetStartsAtExecutionNotQueueCreation(t *testing.T) {
	now := time.Now()
	run := &CreativeRun{Status: CreativeRunStatusRunning, Operation: CreativeOperationGenerate, CreatedAt: now.Add(-time.Hour)}
	ids, deadline, err := CreativeImageAttemptBudget(run, 55, 5*time.Minute, now)
	require.NoError(t, err)
	require.Equal(t, []int64{55}, ids)
	require.Equal(t, now.Add(5*time.Minute), deadline)
}
