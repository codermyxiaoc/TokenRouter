package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 模拟真实 HTTP 边界并统计提交次数，避免回归测试仅验证地址字符串。
type directImagesHTTPUpstreamStub struct {
	HTTPUpstream
	do       func(*http.Request, string, int64, int) (*http.Response, error)
	profiles []*tlsfingerprint.Profile
}

func (s *directImagesHTTPUpstreamStub) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	return s.do(req, proxy, id, concurrency)
}
func (s *directImagesHTTPUpstreamStub) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	s.profiles = append(s.profiles, profile)
	return s.do(req, proxy, id, concurrency)
}

// 协议回退只改变 URL 与载荷，匹配的上游身份和 TLS 模板必须贯穿两次调用。
func TestCodexDirectImagesFallbackPreservesTLSRouting(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	const routedUA = "codex-tui/9.9.9 (Mac OS X 14.0; arm64) iTerm (codex-tui; 9.9.9)"
	calls := 0
	upstream := &directImagesHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls++
		require.Equal(t, routedUA, req.Header.Get("User-Agent"))
		require.Equal(t, "codex-tui", req.Header.Get("Originator"))
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"not found"}}`))}, nil
	}}
	svc := newOpenAIImagesTestService(upstream)
	svc.tlsFPProfileService = &TLSFingerprintProfileService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	account := directImagesTestAccount()
	account.Extra = map[string]any{"enable_tls_fingerprint": true}
	_, err = svc.ForwardImages(context.Background(), c, account, body, parsed, "", TLSFingerprintRouterMatchResult{
		Matched: true, UpstreamUserAgent: routedUA, UpstreamOriginator: "codex-tui", TLSFingerprintProfileID: 0,
	})
	require.Error(t, err)
	require.Equal(t, 2, calls)
	for _, profile := range upstream.profiles {
		require.NotNil(t, profile)
		require.Equal(t, "Built-in Default (Node.js 24.x)", profile.Name)
	}
}

// 网络结果不确定时，既不能回退 Responses，也不能返回账号重放信号再次生成。
func TestCodexDirectImagesNetworkFailureNeverReplays(t *testing.T) {
	for _, phase := range []string{"request", "json_body", "json_invalid", "json_empty", "stream_body", "stream_partial"} {
		t.Run(phase, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
			if strings.HasPrefix(phase, "stream") {
				body = []byte(`{"model":"gpt-image-2","prompt":"draw","stream":true}`)
			}
			c, recorder := newOpenAIImagesTestContext(t, body)
			calls := 0
			upstream := &directImagesHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				calls++
				require.Equal(t, "/backend-api/codex/images/generations", req.URL.Path)
				if phase == "request" {
					return nil, errors.New("connection reset")
				}
				var reader io.Reader = &openAIImagesReadErrorBody{err: io.ErrUnexpectedEOF}
				if phase == "json_invalid" {
					reader = strings.NewReader(`{"data":[`)
				} else if phase == "json_empty" {
					reader = strings.NewReader(`{"data":[]}`)
				}
				if phase == "stream_partial" {
					reader = io.MultiReader(strings.NewReader("data: {\"type\":\"image_generation.completed\",\"b64_json\":\"AA==\",\"usage\":{\"output_tokens\":20}}\n\n"), reader)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"req-direct-broken"}}, Body: io.NopCloser(reader)}, nil
			}}
			svc := newOpenAIImagesTestService(upstream)
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			require.Equal(t, 1, calls)
			if phase == "stream_partial" {
				require.NotNil(t, result)
				require.Equal(t, 1, result.ImageCount)
				require.Equal(t, 20, result.Usage.ImageOutputTokens)
				require.Contains(t, recorder.Body.String(), "event: error")
			} else {
				require.Nil(t, result)
			}
			_, recorded := c.Get(OpsUpstreamErrorsKey)
			require.True(t, recorded, "传输失败仍需记录后台错误")
		})
	}
}

// 原生端点同样从实际图片恢复尺寸，不采用请求尺寸或上游错误标注进行结算。
func TestCodexDirectImagesActualSizeAndClientURL(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			image := encodeOpenAIImageTestPNG(t, 2, 3)
			body := []byte(fmt.Sprintf(`{"model":"gpt-image-2","prompt":"draw","size":"1024x1024","response_format":"url","stream":%t}`, stream))
			c, rec := newOpenAIImagesTestContext(t, body)
			response := fmt.Sprintf(`{"size":"2048x2048","data":[{"b64_json":%q}]}`, image)
			if stream {
				response = fmt.Sprintf("data: {\"type\":\"image_generation.completed\",\"b64_json\":%q,\"size\":\"2048x2048\"}\n\n", image)
			}
			svc := newOpenAIImagesTestService(&httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}})
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
			require.NoError(t, err)
			require.Equal(t, []string{"2x3"}, result.ImageOutputSizes)
			require.Contains(t, rec.Body.String(), `"size":"2x3"`)
			require.Contains(t, rec.Body.String(), "data:image/png;base64,")
			if !stream {
				require.False(t, gjson.GetBytes(rec.Body.Bytes(), "data.0.b64_json").Exists())
			} else {
				require.Contains(t, rec.Body.String(), `"b64_json":`)
			}
		})
	}
}

// 原生和旧 Responses 都不支持时最多两次调用，不能在协议之间循环。
func TestCodexDirectImagesFallbackIsBounded(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	var paths []string
	svc := newOpenAIImagesTestService(&directImagesHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"endpoint not found"}}`))}, nil
	}})
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	_, err = svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
	require.Error(t, err)
	require.Equal(t, []string{"/backend-api/codex/images/generations", "/backend-api/codex/responses"}, paths)
}

// JSON 2xx 内的业务拒绝必须有客户端错误正文和后台诊断，不能被当成空 200 结束。
func TestCodexDirectImagesEmbeddedClientErrorWritesResponse(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
	c, rec := newOpenAIImagesTestContext(t, body)
	svc := newOpenAIImagesTestService(&httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","code":"content_policy_violation","message":"image request blocked"}}`))}})
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	_, err = svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "content_policy_violation", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	_, recorded := c.Get(OpsUpstreamErrorsKey)
	require.True(t, recorded)
}
