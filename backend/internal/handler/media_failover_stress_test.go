//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/TokenFlux/TokenRouter/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 压测缓存按真实归属键隔离，并对写入加锁，避免旧单请求夹具的数据竞争污染结果。
type mediaStressBindings struct {
	testutil.StubGatewayCache
	mu      sync.Mutex
	owners  map[string]int64
	pending map[string][]byte
	billed  map[string]bool
}

// 调度运行时仍使用真实冷却；持久写只记录调用，避免嵌入空接口导致 handler 吞掉夹具 panic。
type mediaStressAccountRepo struct {
	grokMediaSlotRepo
	mu     sync.Mutex
	writes int
}

func (r *mediaStressAccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.mu.Lock()
	r.writes++
	r.mu.Unlock()
	return nil
}

func (r *mediaStressAccountRepo) SetRateLimited(context.Context, int64, time.Time) error {
	r.mu.Lock()
	r.writes++
	r.mu.Unlock()
	return nil
}

func (r *mediaStressAccountRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	r.mu.Lock()
	r.writes++
	r.mu.Unlock()
	return nil
}

func (b *mediaStressBindings) GetSessionAccountID(_ context.Context, group int64, key string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	owner, ok := b.owners[fmt.Sprintf("%d:%s", group, key)]
	if !ok {
		return 0, errors.New("任务归属不存在")
	}
	return owner, nil
}

func (b *mediaStressBindings) SetSessionAccountID(_ context.Context, group int64, key string, owner int64, _ time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.owners[fmt.Sprintf("%d:%s", group, key)] = owner
	return nil
}

func (b *mediaStressBindings) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (b *mediaStressBindings) DeleteSessionAccountID(_ context.Context, group int64, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.owners, fmt.Sprintf("%d:%s", group, key))
	return nil
}

func (b *mediaStressBindings) SetGrokVideoPendingBilling(_ context.Context, key string, body []byte, _ time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending[key] = append([]byte(nil), body...)
	return nil
}

func (b *mediaStressBindings) GetGrokVideoPendingBilling(_ context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.pending[key]...), nil
}

func (b *mediaStressBindings) ClaimGrokVideoBilled(_ context.Context, key string, _ time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.billed[key] {
		return false, nil
	}
	b.billed[key] = true
	return true, nil
}

func (b *mediaStressBindings) ReleaseGrokVideoBilled(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.billed, key)
	return nil
}

type mediaStressUpstream struct {
	service.HTTPUpstream
	mu       sync.Mutex
	scenario string
	seedance bool
	attempts map[string][]int64
	gate     chan struct{}
	first    int
	active   int
	peak     int
}

func (u *mediaStressUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Prompt  string `json:"prompt"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	job := payload.Prompt
	if u.seedance && len(payload.Content) > 0 {
		job = payload.Content[0].Text
	}
	u.mu.Lock()
	u.attempts[job] = append(u.attempts[job], accountID)
	attempt := len(u.attempts[job])
	u.active++
	u.peak = max(u.peak, u.active)
	if attempt == 1 {
		u.first++
		if u.first == 64 {
			close(u.gate)
		}
	}
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.active--
		u.mu.Unlock()
	}()
	if attempt == 1 {
		select {
		case <-u.gate:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("压测首轮屏障未达到预期并发数")
		}
	}
	status := 200
	result := fmt.Sprintf(`{"request_id":%q,"status":"queued"}`, "task-"+job)
	if u.seedance {
		result = fmt.Sprintf(`{"id":%q,"status":"queued"}`, "task-"+job)
	}
	switch u.scenario {
	case "hard_quota_then_healthy":
		if attempt == 1 {
			status, result = 429, `{"error":{"code":"subscription:free-usage-exhausted","message":"spending limit reached"}}`
		}
	case "accepted_with_error":
		status = 503
		result = fmt.Sprintf(`{"id":%q,"status":"queued","error":{"code":"ServiceUnavailable"}}`, "task-"+job)
	case "transport_unknown":
		return nil, errors.New("connection lost after body write")
	case "generic_error":
		status, result = 500, `{"error":{"code":"internal_error","message":"internal server error"}}`
	case "conflicting_ids":
		status, result = 503, fmt.Sprintf(`{"id":%q,"task_id":%q,"request_id":%q,"error":{"code":"ServiceUnavailable"}}`, "task-"+job, "other-"+job, "other-"+job)
	case "invalid_task_id":
		status, result = 503, `{"id":42,"error":{"code":"ServiceUnavailable"}}`
	case "parameter_error":
		status, result = 400, `{"error":{"code":"invalid_request_error","message":"invalid duration"}}`
	case "usage_without_id":
		status, result = 503, `{"usage":{"completion_tokens":123},"error":{"code":"ServiceUnavailable"}}`
	case "media_without_id":
		status, result = 503, `{"data":[{"url":"https://cdn.example/video.mp4"}],"error":{"code":"ServiceUnavailable"}}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(result))}, nil
}

func newMediaStressHandler(t *testing.T, scenario string, seedance bool) (*OpenAIGatewayHandler, *grokMediaSlotsCache, *mediaStressBindings, *mediaStressUpstream) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	accounts := make([]service.Account, 3)
	for i := range accounts {
		accounts[i] = service.Account{ID: int64(i + 1), Platform: service.PlatformGrok, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 512, Priority: i, GroupIDs: []int64{24},
			Credentials: map[string]any{"api_key": "local-test-key", "access_token": "local-test-token"}}
		if seedance {
			accounts[i].Platform = service.PlatformOpenAI
			accounts[i].Credentials["base_url"] = "https://ark.cn-beijing.volces.com/api/v3"
			accounts[i].Credentials["openai_workload_capabilities"] = []string{"seedance"}
		}
	}
	slots := &grokMediaSlotsCache{accounts: map[string]int64{}, users: map[string]int64{}}
	concurrency := service.NewConcurrencyService(slots)
	bindings := &mediaStressBindings{owners: map[string]int64{}, pending: map[string][]byte{}, billed: map[string]bool{}}
	upstream := &mediaStressUpstream{scenario: scenario, seedance: seedance, attempts: map[string][]int64{}, gate: make(chan struct{})}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 20 * time.Millisecond
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.AdvancedScheduler.StickyEscapeEnabled = true
	repo := &mediaStressAccountRepo{grokMediaSlotRepo: grokMediaSlotRepo{openAIImagesFailoverAccountRepo: openAIImagesFailoverAccountRepo{accounts: accounts}}}
	provider := service.NewGrokTokenProvider(repo, nil)
	gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, bindings, cfg, nil, concurrency, nil, nil, nil, upstream, nil, nil, nil, provider, nil, nil, nil, nil, nil)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	return NewOpenAIGatewayHandler(gateway, concurrency, billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg), slots, bindings, upstream
}

// 共享 handler、调度器和缓存运行 64 并发，明确拒绝可换号，已有受理证据禁止再次生成。
func TestMediaFailoverStressConcurrentCreation(t *testing.T) {
	const count = 64
	for _, seedance := range []bool{false, true} {
		platform := "grok"
		if seedance {
			platform = "seedance"
		}
		for _, scenario := range []string{"hard_quota_then_healthy", "accepted_with_error", "transport_unknown", "generic_error", "conflicting_ids", "parameter_error", "usage_without_id", "media_without_id"} {
			// 方舟只以规范 id 作为任务身份，request_id 是追踪号，不能用跨协议字段制造伪冲突。
			if seedance && scenario == "conflicting_ids" {
				scenario = "invalid_task_id"
			}
			t.Run(platform+"/"+scenario, func(t *testing.T) {
				h, slots, bindings, upstream := newMediaStressHandler(t, scenario, seedance)
				contexts := make([]*gin.Context, count)
				recorders := make([]*httptest.ResponseRecorder, count)
				for i := 0; i < count; i++ {
					contexts[i], recorders[i] = grokMediaSlotContext(context.Background(), true)
					path, body := "/v1/videos/generations", fmt.Sprintf(`{"model":"grok-imagine-video","prompt":"job-%d","duration":6}`, i)
					if seedance {
						path, body = "/api/v3/contents/generations/tasks", fmt.Sprintf(`{"model":"doubao-seedance","content":[{"type":"text","text":"job-%d"}]}`, i)
						key, _ := middleware.GetAPIKeyFromContext(contexts[i])
						key.Group.Platform = service.PlatformOpenAI
					}
					contexts[i].Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
					contexts[i].Request.Header.Set("Content-Type", "application/json")
					contexts[i].Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 10, Concurrency: 512})
				}
				var workers sync.WaitGroup
				start := make(chan struct{})
				workers.Add(count)
				for i := 0; i < count; i++ {
					go func(i int) {
						defer workers.Done()
						<-start
						if seedance {
							h.SeedanceTasks(contexts[i])
						} else {
							h.GrokVideoGeneration(contexts[i])
						}
					}(i)
				}
				close(start)
				workers.Wait()
				wantAttempts := 1
				if scenario == "hard_quota_then_healthy" {
					wantAttempts = 2
				}
				require.Len(t, upstream.attempts, count)
				for i, recorder := range recorders {
					attempts := upstream.attempts[fmt.Sprintf("job-%d", i)]
					require.Len(t, attempts, wantAttempts, "logical request %d: %s", i, recorder.Body.String())
					if wantAttempts == 2 {
						require.NotEqual(t, attempts[0], attempts[1])
						require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
						require.Contains(t, recorder.Body.String(), fmt.Sprintf("task-job-%d", i))
					} else {
						require.GreaterOrEqual(t, recorder.Code, 400, recorder.Body.String())
					}
				}
				if scenario == "hard_quota_then_healthy" || scenario == "accepted_with_error" {
					require.Len(t, bindings.pending, count)
				} else {
					require.Empty(t, bindings.pending)
				}
				require.Empty(t, bindings.billed, "只提交排队任务不能提前结算")
				require.GreaterOrEqual(t, upstream.peak, count)
				slots.assertReleased(t)
				t.Logf("platform=%s logical=%d post=%d user_success=%d pending=%d billed=0 slots=%d/%d peak_upstream=%d", platform, count, count*wantAttempts, map[bool]int{true: count, false: 0}[wantAttempts == 2], len(bindings.pending), slots.released, slots.acquired, upstream.peak)
			})
		}
	}
}
