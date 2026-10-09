//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 对抗性回归：非空数组不代表存在可转换的回答；损坏封装不能产生成功计量。
func TestAdversarialClineMalformedChoicesMustFail(t *testing.T) {
	for _, choices := range []string{`[null]`, `[{}]`, `[{"index":0}]`, `[{"message":null}]`, `[false]`, `["answer"]`} {
		for _, ingress := range cnProtocolIngressCases() {
			t.Run(choices+"/"+ingress.name, func(t *testing.T) {
				response := fmt.Sprintf(`{"success":true,"data":{"id":"invalid-choice","model":"deepseek-chat","choices":%s,"usage":{"prompt_tokens":8,"completion_tokens":27}}}`, choices)
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}}
				account := adaptiveProtocolTestAccount(PlatformCline, nil)
				account.Credentials["api_protocol"] = APIProtocolChatCompletions
				account.Credentials["base_url"] = "https://relay.example/v1"
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, ingress.path, strings.NewReader(string(ingress.body)))
				var result *OpenAIForwardResult
				var err error
				switch ingress.name {
				case "chat completions":
					result, err = svc.ForwardAsChatCompletions(t.Context(), c, account, ingress.body, "", "")
				case "messages":
					result, err = svc.ForwardAsAnthropic(t.Context(), c, account, ingress.body, "", "")
				default:
					result, err = svc.Forward(t.Context(), c, account, ingress.body)
				}
				require.Error(t, err, "损坏 choices 被接受，result=%+v body=%s", result, w.Body.String())
				require.Nil(t, result)
			})
		}
	}
}

// 对抗性回归：重复 JSON 计量字段有解析分歧，不能把首个零值当作最终费用。
func TestAdversarialSystemOneAmbiguousUsageMustFail(t *testing.T) {
	for _, body := range []string{
		`{"answers":{},"usage":{"input_tokens":0,"input_tokens":100,"output_tokens":1}}`,
		`{"answers":{},"usage":{"input_tokens":0,"output_tokens":1},"usage":{"input_tokens":100,"output_tokens":1}}`,
		`{"answers":{},"usage":{"input_tokens":1099511627775.99999,"output_tokens":1}}`,
	} {
		t.Run(body, func(t *testing.T) {
			usage, ok := extractSystemOneUsage([]byte(body))
			require.False(t, ok, "歧义计量不应被接受：%+v", usage)
		})
	}
}

// 取消请求须快速退出共享冷启动等待，后台结果仍可供下一请求读取。
func TestAdversarialModelCatalogCancellationAndBackoff(t *testing.T) {
	var catalog modelProtocolCatalog
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	finished := make(chan []string, 1)
	go func() {
		finished <- catalog.lookup(ctx, "cancel-key", "m", time.Now(), func() { close(started) })
	}()
	<-started
	cancel()
	select {
	case got := <-finished:
		require.Nil(t, got)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("取消请求未及时退出目录冷启动等待")
	}
	catalog.store("cancel-key", map[string][]string{"m": {APIProtocolResponses}}, nil, time.Now())
	require.Equal(t, []string{APIProtocolResponses}, catalog.lookup(t.Context(), "cancel-key", "M", time.Now(), nil))
	catalog.store("cancel-key", nil, fmt.Errorf("local test failure"), time.Now())
	require.Equal(t, []string{APIProtocolResponses}, catalog.lookup(t.Context(), "cancel-key", "m", time.Now().Add(modelProtocolCatalogTTL), nil))
}

// 官方主机识别不能让相似域名、明文协议、非默认端口借用官方钱包查询。
func TestAdversarialOfficialProviderOriginBoundary(t *testing.T) {
	for _, raw := range []string{"http://api.cline.bot", "https://api.cline.bot.evil.invalid", "https://api.cline.bot@evil.invalid", "https://evil.invalid/?target=api.cline.bot", "https://api.cline.bot:444", "https://api.cline.bot./"} {
		require.False(t, isOfficialProviderHost(raw, "api.cline.bot"), raw)
	}
	for _, raw := range []string{"https://api.cline.bot/api/v1", "https://API.CLINE.BOT:443/api/v1"} {
		require.True(t, isOfficialProviderHost(raw, "api.cline.bot"), raw)
	}
}

// 目录成功与失败日志都不能保存地址中的 Basic Auth 密码或查询密钥。
func TestAdversarialModelCatalogLogsRedactURLCredentials(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	account := adaptiveProtocolTestAccount(PlatformCommandCode, nil)
	account.ID = time.Now().UnixNano()
	account.Credentials["api_protocol"] = APIProtocolAdaptive
	account.Credentials["base_url"] = "https://catalog-user:catalog-password-secret@relay.example/v1?api_key=catalog-query-secret"
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"m","supported_endpoints":["/responses"]}]}`))}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	require.Equal(t, []string{APIProtocolResponses}, svc.modelCatalogProtocols(t.Context(), account, "m"))
	require.NotContains(t, logs.String(), "catalog-password-secret")
	require.NotContains(t, logs.String(), "catalog-query-secret")
}

// 对抗性回归：路由读取的第一个 model 必须与标准 JSON 解码后的实际出站 model 一致。
func TestAdversarialDuplicateModelCannotChangeForwardedModel(t *testing.T) {
	for _, platform := range []string{PlatformCline, PlatformCommandCode, PlatformOpenAI} {
		t.Run(platform, func(t *testing.T) {
			body := []byte(`{"model":"allowed-model","model":"forbidden-model","messages":[{"role":"user","content":"local test"}]}`)
			account := adaptiveProtocolTestAccount(platform, nil)
			account.Credentials["api_protocol"] = APIProtocolChatCompletions
			account.Credentials["base_url"] = "https://relay.example/v1"
			account.Credentials["model_mapping"] = map[string]any{"allowed-model": "allowed-model"}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"local stop"}}`))}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			c := adaptiveProtocolTestContext("/v1/chat/completions", body)
			_, _ = svc.ForwardAsChatCompletions(t.Context(), c, account, body, "", "")
			if upstream.lastReq == nil {
				return
			}
			var forwarded struct{ Model string `json:"model"` }
			require.NoError(t, json.Unmarshal(upstream.lastBody, &forwarded))
			require.Equal(t, "allowed-model", forwarded.Model, "最后一个 model 不能越过先前按 allowed-model 做出的路由与账号筛选")
		})
	}
}
