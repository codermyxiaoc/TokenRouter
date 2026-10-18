package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/servertiming"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// 内存 HTTP 替身模拟生产共享 trace 的传输阶段，绝不发出真实生成请求。
type videoSubmitDiagnosticHTTP struct {
	calls int
	run   func(*http.Request) (*http.Response, error)
}

func (f *videoSubmitDiagnosticHTTP) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	f.calls++
	return f.run(req.WithContext(servertiming.BeginHTTPTrace(req.Context())))
}

func (f *videoSubmitDiagnosticHTTP) DoWithTLS(req *http.Request, proxy string, id int64, limit int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return f.Do(req, proxy, id, limit)
}

func TestVideoSubmitTransportDiagnosticPreservesResponseAndFailure(t *testing.T) {
	for _, scenario := range []string{"success", "http_error", "timeout", "canceled", "write_error"} {
		t.Run(scenario, func(t *testing.T) {
			var output bytes.Buffer
			oldLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(oldLogger) })
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			deadline, _ := ctx.Deadline()
			requestBody := []byte(`{"model":"m","prompt":"private-prompt-marker","input_reference":{"image_url":"data:image/png;base64,private-image-marker"}}`)
			responseBody := `{"id":"private-task-marker","status":"queued","extra":"private-response-marker"}`
			account := videoFixtureAccount(VideoEndpointOpenAIVideos)
			account.Credentials["api_key"] = "private-key-marker"
			target := VideoUpstreamTarget{Version: 1, Endpoint: VideoEndpointOpenAIVideos, AccountID: account.ID, BaseURL: "https://relay.example/private-path-marker", CreatePath: "/v1/videos", Model: "m"}
			var expectedErr error
			statusCode := http.StatusAccepted
			if scenario == "http_error" {
				statusCode = http.StatusBadRequest
			}
			fixture := &videoSubmitDiagnosticHTTP{run: func(req *http.Request) (*http.Response, error) {
				actualBody, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.Equal(t, requestBody, actualBody)
				require.Equal(t, "Bearer private-key-marker", req.Header.Get("Authorization"))
				require.Equal(t, "/private-path-marker/v1/videos", req.URL.Path)
				require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
				actualDeadline, ok := req.Context().Deadline()
				require.True(t, ok)
				require.Equal(t, deadline, actualDeadline)
				trace := httptrace.ContextClientTrace(req.Context())
				require.NotNil(t, trace)
				trace.GetConn("relay.example:443")
				trace.GotConn(httptrace.GotConnInfo{Reused: true})
				var writeErr error
				if scenario == "write_error" {
					writeErr = errors.New("private-write-error-marker")
				}
				trace.WroteRequest(httptrace.WroteRequestInfo{Err: writeErr})
				switch scenario {
				case "timeout":
					expectedErr = &url.Error{Op: "Post", URL: "https://private-error-url-marker.invalid/key", Err: context.DeadlineExceeded}
				case "canceled":
					cancel()
					require.ErrorIs(t, req.Context().Err(), context.Canceled)
					expectedErr = req.Context().Err()
				case "write_error":
					expectedErr = writeErr
				default:
					trace.GotFirstResponseByte()
					return &http.Response{StatusCode: statusCode, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody))}, nil
				}
				return nil, expectedErr
			}}
			service := NewVideoUpstreamService(&OpenAIGatewayService{httpUpstream: fixture})
			response, err := service.Submit(ctx, &VideoUpstreamSelection{Account: account, Target: target, Body: requestBody})
			require.Equal(t, 1, fixture.calls, "观测不能重放创建请求")
			require.Equal(t, expectedErr, err, "返回原始错误，不因日志改变错误分类")
			if expectedErr == nil {
				require.Equal(t, statusCode, response.StatusCode)
				require.Equal(t, responseBody, string(response.Body))
			} else {
				require.Nil(t, response)
			}
			var entry map[string]any
			require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry), "每次 Submit 只记录一条诊断")
			require.Equal(t, "video submission transport", entry["msg"])
			require.Equal(t, "relay.example", entry["hostname"])
			require.Equal(t, "openai_videos", entry["endpoint"])
			require.Equal(t, float64(len(requestBody)), entry["request_bytes"])
			require.Equal(t, true, entry["connection_observed"])
			require.Equal(t, true, entry["request_write_observed"])
			require.Equal(t, float64(1), entry["request_write_count"])
			require.Equal(t, scenario == "write_error", entry["request_write_error"])
			kind := scenario
			if scenario == "success" {
				kind = "none"
			} else if scenario == "write_error" {
				kind = "transport_error"
			}
			require.Equal(t, kind, entry["failure_kind"])
			require.Equal(t, expectedErr == nil, entry["first_byte_observed"])
			if expectedErr == nil {
				require.Equal(t, float64(statusCode), entry["http_status"])
				require.Equal(t, float64(len(responseBody)), entry["response_bytes"])
			} else {
				require.Equal(t, float64(0), entry["http_status"])
				require.Equal(t, float64(-1), entry["first_byte_ms"])
			}
			// 日志仅允许主机名和定长统计，不泄露路径、密钥、提示词、素材或错误原文。
			require.NotContains(t, output.String(), "private-")
			require.NotContains(t, output.String(), "Authorization")
			require.NotContains(t, output.String(), "base64")
		})
	}
}
