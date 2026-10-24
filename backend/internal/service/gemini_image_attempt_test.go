//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 四种用户协议必须共享同一策略；测试按实际 HTTP 发送次数验证，不能只检查返回码。
func TestGeminiImageAttemptFourEntrypointsNeverRetryUnknown(t *testing.T) {
	for _, endpoint := range geminiStabilityEndpoints() {
		for _, tc := range []struct {
			name       string
			status     int
			body       string
			wantSwitch bool
		}{
			{"rate_rejection", 429, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"busy"}}`, true},
			{"unknown_gateway", 502, `{"error":{"message":"upstream unavailable"}}`, false},
			{"invalid_input", 400, `{"error":{"status":"INVALID_ARGUMENT","message":"invalid image"}}`, false},
		} {
			t.Run(endpoint.name+"/"+tc.name, func(t *testing.T) {
				svc, stub := newGeminiVertexAPIKeyTestService()
				// 错误分支会记录账号限流；使用完整仓储桩而非仅成功路径的空依赖。
				svc.accountRepo = &geminiErrorPolicyRepo{}
				stub.response = &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}
				c, _ := newGeminiNativeTestContext(t)
				withGeminiImageGateAPIKey(c, &Group{ID: 1, Platform: PlatformGemini, AllowImageGeneration: true})
				account := geminiVertexAPIKeyTestAccount("")
				account.Credentials["model_mapping"] = map[string]any{"studio-alias": "gemini-3-pro-image"}
				_, _ = endpoint.call(svc, c, account, []byte(endpoint.body))
				t.Cleanup(func() { CancelImageAttemptPolicy(c.Request.Context()) })
				require.Equal(t, 1, stub.calls, "图片不能使用文本的五次同号重试")
				require.Equal(t, 1, ImageAttemptSendCount(c.Request.Context()))
				decision, ok := ImageAttemptLastDecision(c.Request.Context())
				require.True(t, ok)
				require.Equal(t, tc.wantSwitch, ImageAttemptCanSwitch(decision))
			})
		}
	}
}

func TestGeminiImageAttemptTransportAndResponseEvidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantSwitch bool
	}{
		{"dns_before_send", &net.DNSError{Err: "no such host", Name: "example.invalid"}, true},
		{"read_interrupted", io.ErrUnexpectedEOF, false},
		{"cancelled", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := WithImageAttemptPolicy(context.Background(), time.Second)
			defer cancel()
			stub := &geminiCompatHTTPUpstreamStub{err: tc.err}
			req := httptest.NewRequest(http.MethodPost, "https://example.invalid/generate", nil).WithContext(ctx)
			_, err := doGeminiImageAwareRequest(ctx, stub, req, "", &Account{ID: 1}, nil, "gemini-3-pro-image", "generate")
			require.Error(t, err)
			d, ok := ImageAttemptDecisionForError(err)
			require.True(t, ok)
			require.Equal(t, tc.wantSwitch, ImageAttemptCanSwitch(d))
			require.Equal(t, 1, stub.calls)
		})
	}
	ctx, cancel := WithImageAttemptPolicy(context.Background(), time.Second)
	defer cancel()
	stub := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200, Header: http.Header{}, Body: &geminiAbortingStreamBody{data: bytes.NewReader([]byte("partial")), abortErr: io.ErrUnexpectedEOF}}}
	req := httptest.NewRequest(http.MethodPost, "https://example.invalid/generate", nil).WithContext(ctx)
	resp, err := doGeminiImageAwareRequest(ctx, stub, req, "", &Account{ID: 1}, nil, "gemini-3-pro-image", "generate")
	require.NoError(t, err)
	_, err = io.ReadAll(resp.Body)
	require.Error(t, err)
	_ = resp.Body.Close()
	d, _ := ImageAttemptLastDecision(ctx)
	require.False(t, ImageAttemptCanSwitch(d))
	require.Error(t, BeginImageAttempt(ctx, 2, "gemini-3-pro-image", "generate"))
}

// 未命中的账号自定义错误码既不能换号，也不能被共享图片层提前写入健康冷却。
func TestGeminiImageAttemptCustomErrorPolicyPrecedesCooldown(t *testing.T) {
	ctx, cancel := WithImageAttemptPolicy(context.Background(), time.Second)
	defer cancel()
	store := &imageCooldownServiceStub{}
	account := &Account{ID: 1, Type: AccountTypeAPIKey, Credentials: map[string]any{"custom_error_codes_enabled": true, "custom_error_codes": []any{float64(401)}}}
	stub := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"rate_limit_exceeded"}}`))}}
	req := httptest.NewRequest(http.MethodPost, "https://example.invalid/generate", nil).WithContext(ctx)
	resp, err := doGeminiImageAwareRequest(ctx, stub, req, "", account, store, "gemini-3-pro-image", "generate")
	require.NoError(t, err)
	_ = resp.Body.Close()
	decision, ok := ImageAttemptLastDecision(ctx)
	require.True(t, ok)
	require.False(t, ImageAttemptCanSwitch(decision))
	require.False(t, decision.AttemptFault)
	require.Zero(t, store.writes)
}

// 安全策略下没有实际产物不能只凭图片模型名按一张扣费；文件引用必须是明确的图片类型。
func TestGeminiImageAttemptCountsOnlyObservedImages(t *testing.T) {
	ctx, cancel := WithImageAttemptPolicy(context.Background(), time.Second)
	defer cancel()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/image", nil).WithContext(ctx)
	beginGeminiImageOutputObservation(c)
	observeGeminiImageOutputs(c, []byte(`{"promptFeedback":{"blockReason":"SAFETY"}}`))
	require.Zero(t, resolveGeminiImageCount(c, "gemini-3-pro-image", "gemini-3-pro-image"))
	observeGeminiImageOutputs(c, []byte(`{"candidates":[{"content":{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"https://example.test/generated.png"}}]}}]}`))
	require.Equal(t, 1, resolveGeminiImageCount(c, "alias", "alias"))
}

// 总预算到期后的读取失败仍冷却账号模型；纯取消不处罚，重复读取也只写一次。
func TestGeminiImageAttemptReadDeadlineRecordsHealthButCancellationDoesNot(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "cancelled"}[cancelled], func(t *testing.T) {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			readErr := error(context.DeadlineExceeded)
			if cancelled {
				cancel()
				ctx, cancel = context.WithCancel(context.Background())
				cancel()
				readErr = context.Canceled
			}
			defer cancel()
			store := &imageCooldownServiceStub{}
			body := &imageAttemptResponseBody{ReadCloser: &openAIImagesReadErrorBody{err: readErr}, ctx: ctx, cache: store, accountID: 7, model: "gemini-3-pro-image", operation: "edit"}
			for range 2 {
				_, err := body.Read(make([]byte, 1))
				require.ErrorIs(t, err, readErr)
			}
			if cancelled {
				require.Zero(t, store.writes)
			} else {
				require.Equal(t, 1, store.writes)
				require.False(t, store.cancelled)
			}
		})
	}
}

// 原生 Antigravity 在共享策略下不能执行60次容量重试、签名改写重发或URL回退。
func TestAntigravityImageAttemptSingleSendAndNoSameAccountRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ctx, cancel := WithImageAttemptPolicy(context.Background(), time.Second)
			defer cancel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image:generateContent", nil).WithContext(ctx)
			stub := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"MODEL_CAPACITY_EXHAUSTED"}}`))}}
			svc := &AntigravityGatewayService{}
			_, err := svc.antigravityRetryLoop(antigravityRetryLoopParams{ctx: ctx, c: c, account: &Account{ID: 1, Platform: PlatformAntigravity}, body: []byte(`{"model":"gemini-3-pro-image","request":{"contents":[]}}`), httpUpstream: stub, action: "generateContent", requestedModel: "gemini-3-pro-image"})
			require.Error(t, err)
			require.Equal(t, 1, stub.calls)
			var failure *UpstreamFailoverError
			require.True(t, errors.As(err, &failure))
			d, _ := ImageAttemptLastDecision(ctx)
			require.Equal(t, status == 429, ImageAttemptCanSwitch(d))
		})
	}
}
