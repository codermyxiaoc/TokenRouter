//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 普通 Gemini Key 固定分组；测试明确覆盖组内恢复，不以智能换组替代原生 handler 的重试。
type failoverGeminiStressGroups struct {
	service.GroupRepository
	group service.Group
}

func (r *failoverGeminiStressGroups) GetByID(context.Context, int64) (*service.Group, error) {
	copy := r.group
	return &copy, nil
}

func (r *failoverGeminiStressGroups) GetByIDLite(ctx context.Context, id int64) (*service.Group, error) {
	return r.GetByID(ctx, id)
}

// 四种真实 handler 共用 Gemini 服务、本机 HTTP 上游和真实 RecordUsage，验证并发转换不串用量。
func TestFailoverStressGeminiProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	usage := &routingConcurrentUsage{}
	repo := &failoverStressAccounts{accounts: make(map[int64]service.Account), groups: map[int64][]int64{1: {7101, 7102}}}
	upstream := &encryptedRoutingLocalUpstream{allowed: make(map[string]bool)}
	var mu sync.Mutex
	visits := make(map[string][]int64)
	for _, accountID := range []int64{7101, 7102} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil || r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/v1beta/models/gemini-2.5-flash:") {
				t.Errorf("Gemini 上游路径或读取错误: %s %s %v", r.Method, r.URL.Path, err)
				w.WriteHeader(400)
				return
			}
			id := gjson.GetBytes(body, "contents.0.parts.0.text").String()
			mu.Lock()
			visits[id] = append(visits[id], accountID)
			mu.Unlock()
			stream := strings.HasSuffix(r.URL.Path, ":streamGenerateContent")
			w.Header().Set("X-Request-Id", fmt.Sprintf("%s-a%d", id, accountID))
			w.Header().Set("Content-Type", "application/json")
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
			}
			write := func(payload string) {
				if stream {
					payload = "data: " + payload + "\n\n"
				}
				_, _ = io.WriteString(w, payload)
			}
			if accountID == 7101 {
				if strings.HasPrefix(id, "recover-") {
					write(`{"error":{"code":503,"status":"UNAVAILABLE","message":"upstream unavailable"}}`)
					return
				}
				if strings.HasPrefix(id, "empty-") {
					return
				}
			}
			if strings.HasPrefix(id, "usage-error-") {
				write(`{"error":{"code":503,"status":"UNAVAILABLE","message":"generation stopped after metering"},"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"totalTokenCount":13}}`)
				return
			}
			write(`{"candidates":[{"content":{"role":"model","parts":[{"text":"visible-gemini"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"totalTokenCount":13},"responseId":"` + id + `"}`)
		}))
		t.Cleanup(server.Close)
		upstream.allowed[server.URL] = true
		repo.accounts[accountID] = service.Account{ID: accountID, Name: fmt.Sprint(accountID), Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: int(accountID - 7100), GroupIDs: []int64{1}, Credentials: map[string]any{"api_key": "local-only", "base_url": server.URL, "provider_type": "third_party", "model_whitelist": []any{"gemini-2.5-flash"}}}
	}
	groupRepo := &failoverGeminiStressGroups{group: service.Group{ID: 1, Platform: service.PlatformGemini, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1}}
	groupID := int64(1)
	key := service.APIKey{ID: 7401, Key: "sk-local-gemini-stress", UserID: 7402, User: &service.User{ID: 7402, Status: service.StatusActive, Balance: 100}, Status: service.StatusActive, GroupID: &groupID, Group: &groupRepo.group, BillingMode: service.APIKeyBillingModeBalance}
	keyJSON, err := json.Marshal(key)
	require.NoError(t, err)
	keyRepo := &routingConcurrentKeys{snapshots: map[string][]byte{key.Key: keyJSON}}
	cfg := &config.Config{RunMode: config.RunModeSimple, Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}
	cfg.Default.RateMultiplier = 1
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	concurrency := service.NewConcurrencyService(nil)
	gateway := service.NewGatewayService(repo, groupRepo, usage, nil, nil, nil, nil, nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil), nil, billing, nil, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	gemini := service.NewGeminiMessagesCompatService(repo, groupRepo, nil, nil, nil, nil, upstream, nil, cfg)
	keys := service.NewAPIKeyService(keyRepo, nil, nil, nil, nil, nil, cfg)
	handler := NewGatewayHandler(gateway, nil, gemini, nil, nil, concurrency, billing, nil, keys, nil, nil, nil, nil, cfg, nil)
	router := gin.New()
	router.Use(middleware.Recovery())
	router.Use(gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)))
	router.POST("/v1beta/models/*modelAction", handler.GeminiV1BetaModels)
	router.POST("/v1/messages", handler.Messages)
	router.POST("/v1/chat/completions", handler.ChatCompletions)
	router.POST("/v1/responses", handler.Responses)
	type testcase struct {
		id, protocol, mode string
		stream             bool
		response           *httptest.ResponseRecorder
	}
	var cases []testcase
	for _, protocol := range []string{"native", "messages", "chat", "responses"} {
		for _, stream := range []bool{false, true} {
			for _, mode := range []string{"healthy", "recover", "empty", "usage-error"} {
				for n := range 8 {
					cases = append(cases, testcase{id: fmt.Sprintf("%s-%s-%v-%02d", mode, protocol, stream, n), protocol: protocol, mode: mode, stream: stream})
				}
			}
		}
	}
	runFailoverStress(64, len(cases), func(i int) {
		tc := &cases[i]
		body := map[string]any{"model": "gemini-2.5-flash", "stream": tc.stream}
		path := "/v1/responses"
		switch tc.protocol {
		case "native":
			body = map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": tc.id}}}}}
			path = "/v1beta/models/gemini-2.5-flash:generateContent"
			if tc.stream {
				path = "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse"
			}
		case "messages", "chat":
			path = "/v1/messages"
			if tc.protocol == "chat" {
				path = "/v1/chat/completions"
			}
			body["max_tokens"] = 100
			body["messages"] = []any{map[string]string{"role": "user", "content": tc.id}}
		default:
			body["input"] = tc.id
		}
		encoded, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(encoded)))
		req.Header.Set("Authorization", "Bearer "+key.Key)
		req.Header.Set("Content-Type", "application/json")
		tc.response = httptest.NewRecorder()
		router.ServeHTTP(tc.response, req)
	})
	logs := usage.snapshot()
	logsByID := make(map[string][]*service.UsageLog)
	for _, log := range logs {
		logsByID[log.RequestID] = append(logsByID[log.RequestID], log)
	}
	var recoverErrors, posts, duplicateAccount, usageMissing int
	for _, tc := range cases {
		posts += len(visits[tc.id])
		seen := map[int64]bool{}
		for _, account := range visits[tc.id] {
			if seen[account] {
				duplicateAccount++
			}
			seen[account] = true
		}
		if tc.mode != "usage-error" && (tc.response.Code != 200 || !strings.Contains(tc.response.Body.String(), "visible-gemini")) {
			recoverErrors++
		}
		wantID := int64(7101)
		if tc.mode == "recover" || tc.mode == "empty" {
			wantID = 7102
		}
		if len(logsByID[fmt.Sprintf("%s-a%d", tc.id, wantID)]) != 1 {
			usageMissing++
		}
	}
	t.Logf("FAILOVER_STRESS_GEMINI logical=%d concurrency=64 recoverable_user_errors=%d upstream_posts=%d same_account_repeats=%d usage_records=%d usage_missing_or_duplicate=%d", len(cases), recoverErrors, posts, duplicateAccount, len(logs), usageMissing)
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			want := []int64{7101}
			if tc.mode == "recover" || tc.mode == "empty" {
				want = append(want, 7102)
			}
			require.Equal(t, want, visits[tc.id], tc.response.Body.String())
			if tc.mode != "usage-error" {
				require.Equal(t, 200, tc.response.Code, tc.response.Body.String())
				require.Contains(t, tc.response.Body.String(), "visible-gemini")
				require.NotContains(t, tc.response.Body.String(), "upstream unavailable")
			}
			logID := fmt.Sprintf("%s-a%d", tc.id, want[len(want)-1])
			require.Len(t, logsByID[logID], 1, "每个请求只结算实际已观测用量: "+tc.response.Body.String())
			log := logsByID[logID][0]
			require.Equal(t, want[len(want)-1], log.AccountID)
			require.Equal(t, 10, log.InputTokens)
			require.Equal(t, 3, log.OutputTokens)
		})
	}
	require.Len(t, logs, len(cases))
}
