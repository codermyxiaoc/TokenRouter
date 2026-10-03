package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/manxue"
	"github.com/stretchr/testify/require"
)

// 测试替身不访问网络或已有数据库，只观察持久状态与远端调用顺序。
type intelligenceRepoStub struct {
	IntelligenceRepository
	config   *IntelligenceConfig
	run      *IntelligenceRun
	runs     []IntelligenceRun
	saves    []IntelligenceRun
	denySave bool
	nextPoll time.Time
}

func (r *intelligenceRepoStub) GetConfig(context.Context, int64) (*IntelligenceConfig, error) {
	if r.config == nil {
		return nil, ErrIntelligenceNotFound
	}
	c := *r.config
	return &c, nil
}
func (r *intelligenceRepoStub) ListConfigs(context.Context) ([]IntelligenceConfig, error) {
	if r.config == nil {
		return []IntelligenceConfig{}, nil
	}
	return []IntelligenceConfig{*r.config}, nil
}
func (r *intelligenceRepoStub) SaveConfig(_ context.Context, c *IntelligenceConfig) error {
	copy := *c
	r.config = &copy
	return nil
}
func (r *intelligenceRepoStub) SaveRun(ctx context.Context, run *IntelligenceRun, next time.Time, _ bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if r.denySave {
		return false, nil
	}
	copy := *run
	r.run = &copy
	r.nextPoll = next
	r.saves = append(r.saves, copy)
	return true, nil
}
func (r *intelligenceRepoStub) ListRuns(context.Context, int64) ([]IntelligenceRun, error) {
	return append([]IntelligenceRun{}, r.runs...), nil
}
func (r *intelligenceRepoStub) GetRun(context.Context, string) (*IntelligenceRun, error) {
	if r.run == nil {
		return nil, ErrIntelligenceNotFound
	}
	copy := *r.run
	return &copy, nil
}
func (r *intelligenceRepoStub) CreateRun(_ context.Context, id int64, now time.Time) (*IntelligenceRun, error) {
	return &IntelligenceRun{ID: "local", ConfigID: id, Status: "queued", CreatedAt: now}, nil
}

type intelligenceKeyStub struct {
	APIKeyRepository
	key *APIKey
}

func (r intelligenceKeyStub) GetByID(context.Context, int64) (*APIKey, error) { return r.key, nil }
func (r intelligenceKeyStub) GetByKey(_ context.Context, key string) (*APIKey, error) {
	if r.key != nil && r.key.Key == key {
		return r.key, nil
	}
	return nil, ErrAPIKeyNotFound
}

type intelligenceGroupStub struct {
	GroupRepository
	group   *Group
	visible []Group
}

func (r intelligenceGroupStub) GetByID(context.Context, int64) (*Group, error) { return r.group, nil }
func (r intelligenceGroupStub) GetAvailableGroups(context.Context, int64) ([]Group, error) {
	return r.visible, nil
}

type intelligenceClientStub struct {
	creates, gets int
	create        func(manxue.CreateRequest) (*manxue.TestResult, error)
	result        *manxue.TestResult
	err           error
}

func (c *intelligenceClientStub) Create(_ context.Context, in manxue.CreateRequest, _ string) (*manxue.TestResult, error) {
	c.creates++
	if c.create != nil {
		return c.create(in)
	}
	return c.result, c.err
}
func (c *intelligenceClientStub) Get(context.Context, string) (*manxue.TestResult, error) {
	c.gets++
	return c.result, c.err
}

func newIntelligenceFixture() (*IntelligenceService, *intelligenceRepoStub, *intelligenceClientStub, *APIKey) {
	gid := int64(7)
	// 普通 Key 创建时默认开启自动降级，夹具必须保留真实默认值。
	key := &APIKey{ID: 9, Key: "sk-privatekey1234567890123456", GroupID: &gid, Status: StatusActive, FallbackToDefaultGroupWhenUnavailable: true, User: &User{Status: StatusActive}}
	cfg := &IntelligenceConfig{ID: 1, GroupID: gid, GroupName: "测试组", APIKeyID: key.ID, Model: "gpt-test", Benchmark: "candy", BaseURL: "https://gateway.example/v1", Protocol: "responses", Enabled: true, IntervalMinutes: 60}
	repo := &intelligenceRepoStub{config: cfg}
	client := &intelligenceClientStub{}
	groups := intelligenceGroupStub{group: &Group{ID: gid, Name: "测试组", Status: StatusActive}, visible: []Group{{ID: gid}}}
	s := NewIntelligenceService(repo, intelligenceKeyStub{key: key}, groups, groups, client, func(context.Context) bool { return true })
	s.resolvePublicHost = func(string) error { return nil }
	return s, repo, client, key
}
func intelligenceInput(key string) IntelligenceConfigInput {
	return IntelligenceConfigInput{GroupID: 7, Model: "gpt-test", Benchmark: "candy", BaseURL: "https://gateway.example/custom/v1/", APIKey: key, Protocol: "responses", Enabled: true, IntervalMinutes: 60}
}
func intelligenceQueued() *IntelligenceRun {
	return &IntelligenceRun{ID: "iq_local", ConfigID: 1, GroupID: 7, Model: "gpt-test", Benchmark: "candy", Status: "queued", Verdict: "pending", LeaseToken: "lease", CreatedAt: time.Now().UTC()}
}

func TestIntelligenceConfigurationCredentialAndProtocolBoundaries(t *testing.T) {
	t.Run("密钥只保存引用且空值保留", func(t *testing.T) {
		s, repo, _, key := newIntelligenceFixture()
		saved, err := s.SaveConfig(context.Background(), 0, intelligenceInput(key.Key))
		require.NoError(t, err)
		require.Equal(t, key.ID, repo.config.APIKeyID)
		require.Equal(t, "https://gateway.example/custom/v1", saved.BaseURL)
		raw, err := json.Marshal(saved)
		require.NoError(t, err)
		require.NotContains(t, string(raw), key.Key)
		require.NotContains(t, string(raw), "api_key_id")
		_, err = s.SaveConfig(context.Background(), 1, intelligenceInput(""))
		require.NoError(t, err)
	})
	for _, tc := range []struct {
		name   string
		mutate func(*APIKey)
	}{
		{"复合", func(k *APIKey) { k.IsComposite = true }},
		{"智能路由", func(k *APIKey) { k.SmartRouting = true }},
		{"复合映射", func(k *APIKey) { k.CompositeGroups = []APIKeyCompositeGroup{{GroupID: 7, Prefix: "a"}} }},
		{"错组", func(k *APIKey) { id := int64(8); k.GroupID = &id }},
		{"无绑定分组", func(k *APIKey) { k.GroupID = nil }},
		{"停用", func(k *APIKey) { k.Status = "disabled" }},
		{"过期", func(k *APIKey) { v := time.Now().Add(-time.Hour); k.ExpiresAt = &v }},
		{"超额", func(k *APIKey) { k.Quota = 1; k.QuotaUsed = 1 }},
		{"系统托管", func(k *APIKey) { managedBy := "creative_studio"; k.ManagedBy = &managedBy }},
		{"禁用用户", func(k *APIKey) { k.User.Status = "disabled" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, client, key := newIntelligenceFixture()
			tc.mutate(key)
			_, err := s.SaveConfig(context.Background(), 0, intelligenceInput(key.Key))
			require.Error(t, err)
			_, err = s.Run(context.Background(), 1)
			require.Error(t, err)
			// 放行普通降级开关后，执行入口仍须拒绝其他无效凭据。
			s.execute(context.Background(), intelligenceQueued())
			require.Zero(t, client.creates)
			require.Equal(t, "error", repo.run.Status)
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*IntelligenceConfigInput)
	}{
		{"糖果不能Chat", func(v *IntelligenceConfigInput) { v.Protocol = "chat_completions" }}, {"画图不能ultra", func(v *IntelligenceConfigInput) { v.Benchmark = "drawing"; v.ReasoningEffort = "ultra" }}, {"无default档", func(v *IntelligenceConfigInput) { v.ServiceTier = "default" }}, {"禁私网", func(v *IntelligenceConfigInput) { v.BaseURL = "https://127.0.0.1/v1" }}, {"禁userinfo", func(v *IntelligenceConfigInput) { v.BaseURL = "https://secret@gateway.example/v1" }}, {"禁query", func(v *IntelligenceConfigInput) { v.BaseURL = "https://gateway.example/v1?key=secret" }}, {"间隔下限", func(v *IntelligenceConfigInput) { v.IntervalMinutes = 4 }}, {"间隔上限", func(v *IntelligenceConfigInput) { v.IntervalMinutes = 10081 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, key := newIntelligenceFixture()
			in := intelligenceInput(key.Key)
			tc.mutate(&in)
			_, err := s.SaveConfig(context.Background(), 0, in)
			require.Error(t, err)
		})
	}
	t.Run("禁止历史换组或换模型", func(t *testing.T) {
		s, _, _, key := newIntelligenceFixture()
		in := intelligenceInput(key.Key)
		in.Model = "other"
		_, err := s.SaveConfig(context.Background(), 1, in)
		require.Error(t, err)
	})
}

func TestIntelligenceOrdinaryKeyFallbackDoesNotBlockDetection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fallback bool
	}{
		{"默认开启自动降级", true},
		{"关闭自动降级", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, client, key := newIntelligenceFixture()
			key.FallbackToDefaultGroupWhenUnavailable = tc.fallback
			saved, err := s.SaveConfig(context.Background(), 1, intelligenceInput(key.Key))
			require.NoError(t, err)
			require.Equal(t, key.ID, saved.APIKeyID)
			run, err := s.Run(context.Background(), saved.ID)
			require.NoError(t, err)
			require.Equal(t, "queued", run.Status)
			client.create = func(in manxue.CreateRequest) (*manxue.TestResult, error) {
				// 检测传递原 Key，不关闭开关、不替换凭据，实际降级由原网关负责。
				require.Equal(t, key.Key, in.APIKey)
				require.Equal(t, saved.Model, in.Model)
				return &manxue.TestResult{ID: "remote", Benchmark: "candy", Status: "running"}, nil
			}
			s.execute(context.Background(), intelligenceQueued())
			require.Equal(t, 1, client.creates)
			require.Equal(t, "running", repo.run.Status)
			require.Equal(t, saved.GroupID, *key.GroupID)
			require.Equal(t, tc.fallback, key.FallbackToDefaultGroupWhenUnavailable)
		})
		t.Run(tc.name+"仍阻止停用分组提交", func(t *testing.T) {
			s, repo, client, key := newIntelligenceFixture()
			key.FallbackToDefaultGroupWhenUnavailable = tc.fallback
			s.groups = intelligenceGroupStub{group: &Group{ID: 7, Status: StatusDisabled}}
			_, err := s.SaveConfig(context.Background(), 1, intelligenceInput(key.Key))
			require.Error(t, err)
			s.execute(context.Background(), intelligenceQueued())
			require.Zero(t, client.creates)
			require.Equal(t, "error", repo.run.Status)
			require.Equal(t, "分组不可用", repo.run.ErrorMessage)
		})
	}
}

func TestIntelligenceCreateOnceAfterDurableState(t *testing.T) {
	s, repo, client, _ := newIntelligenceFixture()
	run := intelligenceQueued()
	repo.config.Benchmark = "drawing"
	run.Benchmark = "drawing"
	client.create = func(in manxue.CreateRequest) (*manxue.TestResult, error) {
		require.Equal(t, "pelican", in.Benchmark)
		require.Equal(t, "submitting", repo.run.Status)
		require.Empty(t, repo.run.RemoteID)
		return &manxue.TestResult{ID: "remote", Benchmark: "pelican", Status: "running"}, nil
	}
	s.execute(context.Background(), run)
	require.Equal(t, 1, client.creates)
	require.Equal(t, "running", repo.run.Status)
	require.Equal(t, "remote", repo.run.RemoteID)
	client.result = &manxue.TestResult{ID: "remote", Benchmark: "pelican", Status: "succeeded", Assessment: &manxue.Assessment{Quality: "good"}}
	s.execute(context.Background(), run)
	require.Equal(t, 1, client.creates)
	require.Equal(t, 1, client.gets)
}
func TestIntelligenceRecoveryDoesNotResubmit(t *testing.T) {
	t.Run("已获远端ID即使关闭也必须持久化", func(t *testing.T) {
		s, repo, client, _ := newIntelligenceFixture()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		client.create = func(manxue.CreateRequest) (*manxue.TestResult, error) {
			cancel()
			return &manxue.TestResult{ID: "remote-after-cancel", Benchmark: "candy", Status: "running"}, nil
		}
		s.execute(ctx, intelligenceQueued())
		require.Equal(t, "running", repo.run.Status)
		require.Equal(t, "remote-after-cancel", repo.run.RemoteID)
		require.Equal(t, 1, client.creates)
	})
	t.Run("提交崩溃未有ID", func(t *testing.T) {
		s, repo, client, _ := newIntelligenceFixture()
		run := intelligenceQueued()
		run.Status = "submitting"
		s.execute(context.Background(), run)
		require.Zero(t, client.creates)
		require.Equal(t, "unknown", repo.run.Status)
	})
	t.Run("租约比较失败不发POST", func(t *testing.T) {
		s, repo, client, _ := newIntelligenceFixture()
		repo.denySave = true
		s.execute(context.Background(), intelligenceQueued())
		require.Zero(t, client.creates)
	})
	t.Run("总开关关闭阻止未提交", func(t *testing.T) {
		s, repo, client, _ := newIntelligenceFixture()
		s.enabled = func(context.Context) bool { return false }
		s.execute(context.Background(), intelligenceQueued())
		require.Zero(t, client.creates)
		require.Equal(t, "error", repo.run.Status)
		_, err := s.Run(context.Background(), 1)
		require.ErrorIs(t, err, ErrIntelligenceDisabled)
	})
	t.Run("总开关关闭仍完成已提交GET", func(t *testing.T) {
		s, repo, client, _ := newIntelligenceFixture()
		s.enabled = func(context.Context) bool { return false }
		run := intelligenceQueued()
		run.Status = "running"
		run.RemoteID = "remote"
		client.result = &manxue.TestResult{ID: "remote", Status: "succeeded", Benchmark: "candy", Candy: &manxue.CandyResult{Status: "passed", Answer: "21"}}
		s.execute(context.Background(), run)
		require.Zero(t, client.creates)
		require.Equal(t, 1, client.gets)
		require.Equal(t, "completed", repo.run.Status)
		require.Equal(t, "passed", repo.run.Verdict)
	})
}

func TestIntelligenceSubmissionFailureDoesNotRetry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		wantStatus  string
		wantMessage string
	}{
		{"HTTP400拒绝创建", &manxue.HTTPError{StatusCode: 400}, "error", "检测服务拒绝创建任务（HTTP 400）"},
		{"HTTP429拒绝创建", &manxue.HTTPError{StatusCode: 429}, "error", "检测服务拒绝创建任务（HTTP 429）"},
		{"HTTP503受理未知", &manxue.HTTPError{StatusCode: 503}, "unknown", "检测服务创建接口异常（HTTP 503）"},
		{"提交超时受理未知", &manxue.TransportError{Timeout: true}, "unknown", "连接检测服务超时"},
		{"原始错误不能回显", errors.New("https://private.example/v1 sk-privatekey1234567890123456 remote-capability"), "unknown", "检测提交未成功确认"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, client, key := newIntelligenceFixture()
			client.err = tc.err
			s.execute(context.Background(), intelligenceQueued())
			require.Equal(t, 1, client.creates)
			require.Zero(t, client.gets)
			require.Len(t, repo.saves, 2)
			require.Equal(t, "submitting", repo.saves[0].Status)
			require.Equal(t, tc.wantStatus, repo.run.Status)
			require.Equal(t, tc.wantStatus, repo.run.Verdict)
			require.Contains(t, repo.run.ErrorMessage, tc.wantMessage)
			require.Contains(t, repo.run.ErrorMessage, "未自动重试")
			require.True(t, repo.run.HasDetail)
			require.NotNil(t, repo.run.FinishedAt)
			for _, private := range []string{"https://private.example", key.Key, "remote-capability"} {
				require.NotContains(t, repo.run.ErrorMessage, private)
			}
			// 模拟仅成功写入 submitting 就中断的恢复路径，不能因错误分类重新发出 POST。
			interrupted := repo.saves[0]
			s.execute(context.Background(), &interrupted)
			require.Equal(t, 1, client.creates)
			require.Zero(t, client.gets)
			require.Equal(t, "unknown", repo.run.Status)
		})
	}
}

func TestIntelligenceResultPrivacyAndUnknownVerdict(t *testing.T) {
	s, repo, _, key := newIntelligenceFixture()
	run := intelligenceQueued()
	run.RemoteID = "remote-capability"
	result := &manxue.TestResult{ID: run.RemoteID, Status: "succeeded", Benchmark: "candy", Phase: "https://secret/" + key.Key, Candy: &manxue.CandyResult{Status: "passed", Question: key.Key, Answer: run.RemoteID + " sk-deletedoldkey1234567890"}}
	s.applyResult(context.Background(), run, result, key.Key)
	require.Equal(t, "passed", repo.run.Verdict)
	raw, err := json.Marshal(repo.run)
	require.NoError(t, err)
	require.NotContains(t, string(raw), key.Key)
	require.NotContains(t, string(raw), run.RemoteID)
	require.NotContains(t, string(raw), "sk-deletedoldkey")
	require.Empty(t, repo.run.Phase)
	run = intelligenceQueued()
	run.RemoteID = "remote"
	s.applyResult(context.Background(), run, &manxue.TestResult{ID: "remote", Status: "succeeded", Benchmark: "candy"}, "")
	require.NotEqual(t, "passed", repo.run.Verdict)
	run = intelligenceQueued()
	run.Benchmark = "drawing"
	run.RemoteID = "remote"
	s.applyResult(context.Background(), run, &manxue.TestResult{ID: "remote", Status: "succeeded", Benchmark: "pelican", Assessment: &manxue.Assessment{Quality: "good"}}, "")
	require.Equal(t, "unknown", repo.run.Status)
	require.Equal(t, "unknown", repo.run.Verdict)
	require.Contains(t, repo.run.ErrorMessage, "未能给出明确的画图评判")
}

func TestIntelligenceDecodedResultPersistsOutcomeAndSafeDiagnostic(t *testing.T) {
	// 使用协议原始 JSON 覆盖解码、判定和持久化全链路，避免结构体夹具绕开错误解析。
	for _, tc := range []struct {
		name         string
		benchmark    string
		payload      string
		wantStatus   string
		wantVerdict  string
		wantMessage  string
		wantDetail   bool
		wantArtifact bool
	}{
		{
			name: "糖果通过", benchmark: "candy",
			payload:    `{"benchmark":"candy","status":"succeeded","candy":{"status":"passed","question":"糖果问题 __KEY__","answer":"21 __REMOTE__"}}`,
			wantStatus: "completed", wantVerdict: "passed", wantDetail: true,
		},
		{
			name: "糖果未通过", benchmark: "candy",
			payload:    `{"benchmark":"candy","status":"succeeded","candy":{"status":"incorrect","answer":"20"}}`,
			wantStatus: "completed", wantVerdict: "failed", wantDetail: true,
		},
		{
			name: "糖果结果无正文", benchmark: "candy",
			payload:    `{"benchmark":"candy","status":"succeeded","candy":{"status":"passed"}}`,
			wantStatus: "completed", wantVerdict: "passed",
		},
		{
			name: "糖果状态未知", benchmark: "candy",
			payload:    `{"benchmark":"candy","status":"succeeded","candy":{"status":"undetermined"}}`,
			wantStatus: "unknown", wantVerdict: "unknown", wantMessage: "糖果判定状态无法识别", wantDetail: true,
		},
		{
			name: "糖果缺少结果", benchmark: "candy",
			payload:    `{"benchmark":"candy","status":"succeeded"}`,
			wantStatus: "unknown", wantVerdict: "unknown", wantMessage: "未返回糖果判定结果", wantDetail: true,
		},
		{
			name: "糖果执行错误无原因", benchmark: "candy",
			payload:    `{"benchmark":"candy","status":"succeeded","candy":{"status":"error"}}`,
			wantStatus: "error", wantVerdict: "error", wantMessage: "未提供可识别的失败原因", wantDetail: true,
		},
		{
			name: "顶层鉴权错误覆盖通过结果", benchmark: "candy",
			payload:    `{"benchmark":"candy","status":"succeeded","candy":{"status":"passed"},"error":{"code":"invalid_api_key","message":"__URL__ __KEY__ __REMOTE__","status_code":401}}`,
			wantStatus: "error", wantVerdict: "error", wantMessage: "检测服务报告失败：模型接口鉴权失败，请检查检测 Key（HTTP 401）", wantDetail: true,
		},
		{
			name: "糖果嵌套额度错误覆盖通过结果", benchmark: "candy",
			payload:    `{"benchmark":"candy","status":"succeeded","candy":{"status":"passed","error":{"error":{"type":"insufficient_quota","message":"__URL__ __KEY__ __REMOTE__"},"status":429}}}`,
			wantStatus: "error", wantVerdict: "error", wantMessage: "糖果检测异常：模型接口余额或额度不足（HTTP 429）", wantDetail: true,
		},
		{
			name: "字符串限流错误", benchmark: "candy",
			payload:    `{"benchmark":"candy","status":"failed","error":"HTTP 429 too many requests __URL__ __KEY__ __REMOTE__"}`,
			wantStatus: "error", wantVerdict: "error", wantMessage: "检测服务报告失败：模型接口请求受限，请稍后重试（HTTP 429）", wantDetail: true,
		},
		{
			name: "未知错误原文不回显", benchmark: "candy",
			payload:    `{"benchmark":"candy","status":"failed","error":{"code":"__KEY__","message":"__URL__ __REMOTE__"}}`,
			wantStatus: "error", wantVerdict: "error", wantMessage: "未提供可安全展示的具体原因", wantDetail: true,
		},
		{
			name: "空错误占位不改变成功", benchmark: "candy",
			payload:    `{"benchmark":"candy","status":"succeeded","error":{},"candy":{"status":"passed","error":""}}`,
			wantStatus: "completed", wantVerdict: "passed",
		},
		{
			name: "糖果取消", benchmark: "candy",
			payload:    `{"benchmark":"candy","status":"cancelled","error":"__URL__ __KEY__ __REMOTE__"}`,
			wantStatus: "error", wantVerdict: "error", wantMessage: "检测任务已被取消", wantDetail: true,
		},
		{
			name: "画图通过", benchmark: "drawing",
			payload:    `{"benchmark":"pelican","status":"succeeded","result":{"html":"<p>作品 __KEY__ __REMOTE__</p>"},"assessment":{"quality":"normal","reason":"内容完整"}}`,
			wantStatus: "completed", wantVerdict: "passed", wantDetail: true, wantArtifact: true,
		},
		{
			name: "画图未通过", benchmark: "drawing",
			payload:    `{"benchmark":"pelican","status":"succeeded","assessment":{"quality":"degraded","reason":"缺少部分元素"}}`,
			wantStatus: "completed", wantVerdict: "failed", wantDetail: true,
		},
		{
			name: "画图评判未知仍保留作品", benchmark: "drawing",
			payload:    `{"benchmark":"pelican","status":"succeeded","result":{"html":"<p>作品</p>"},"assessment":{"quality":"unknown","reason":"评估说明 __KEY__ __REMOTE__"}}`,
			wantStatus: "unknown", wantVerdict: "unknown", wantMessage: "未能给出明确的画图评判", wantDetail: true, wantArtifact: true,
		},
		{
			name: "画图缺少质量评估", benchmark: "drawing",
			payload:    `{"benchmark":"pelican","status":"succeeded","result":{"html":"<p>作品</p>"}}`,
			wantStatus: "unknown", wantVerdict: "unknown", wantMessage: "未返回画图质量评估", wantDetail: true, wantArtifact: true,
		},
		{
			name: "画图执行失败HTTP503", benchmark: "drawing",
			payload:    `{"benchmark":"pelican","status":"failed","error":{"status_code":503,"message":"__URL__ __KEY__ __REMOTE__"}}`,
			wantStatus: "error", wantVerdict: "error", wantMessage: "模型接口服务异常，请稍后重试（HTTP 503）", wantDetail: true,
		},
		{
			name: "画图取消", benchmark: "drawing",
			payload:    `{"benchmark":"pelican","status":"cancelled"}`,
			wantStatus: "error", wantVerdict: "error", wantMessage: "检测任务已被取消", wantDetail: true,
		},
		{
			name: "省略远端类型沿用画图配置", benchmark: "drawing",
			payload:    `{"status":"succeeded","assessment":{"quality":"normal"}}`,
			wantStatus: "completed", wantVerdict: "passed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, _, key := newIntelligenceFixture()
			run := intelligenceQueued()
			run.Benchmark = tc.benchmark
			run.RemoteID = "remote-private-capability"
			privateURL := "https://private.example/custom/v1?credential=private-value"
			payload := strings.NewReplacer("__KEY__", key.Key, "__REMOTE__", run.RemoteID, "__URL__", privateURL).Replace(tc.payload)
			payload = `{"id":"` + run.RemoteID + `",` + strings.TrimPrefix(payload, "{")
			var result manxue.TestResult
			require.NoError(t, json.Unmarshal([]byte(payload), &result))
			s.applyResult(context.Background(), run, &result, key.Key)
			require.Len(t, repo.saves, 1)
			require.Equal(t, tc.wantStatus, repo.run.Status)
			require.Equal(t, tc.wantVerdict, repo.run.Verdict)
			require.Equal(t, tc.wantDetail, repo.run.HasDetail)
			require.Equal(t, tc.wantArtifact, repo.run.HasArtifact)
			require.NotNil(t, repo.run.FinishedAt)
			if tc.wantMessage == "" {
				require.Empty(t, repo.run.ErrorMessage)
			} else {
				require.Contains(t, repo.run.ErrorMessage, tc.wantMessage)
			}
			raw, err := json.Marshal(repo.run)
			require.NoError(t, err)
			for _, private := range []string{key.Key, run.RemoteID, privateURL, "private.example", run.LeaseToken} {
				require.NotContains(t, string(raw), private)
			}
			var visible map[string]any
			require.NoError(t, json.Unmarshal(raw, &visible))
			require.Equal(t, tc.wantDetail, visible["has_detail"])
		})
	}
}

func TestIntelligenceClassifyingPhasePersistsUntilAssessmentArrives(t *testing.T) {
	s, repo, _, key := newIntelligenceFixture()
	run := intelligenceQueued()
	run.Benchmark = "drawing"
	run.RemoteID = "remote-classifying"
	var result manxue.TestResult
	require.NoError(t, json.Unmarshal([]byte(`{"id":"remote-classifying","benchmark":"pelican","status":"running","phase":"classifying","result":{"html":"<p>待评估作品</p>"}}`), &result))
	s.applyResult(context.Background(), run, &result, key.Key)
	require.Equal(t, "classifying", repo.run.Phase)
	require.Equal(t, "running", repo.run.Status)
	require.Equal(t, "pending", repo.run.Verdict)
	require.Nil(t, repo.run.FinishedAt)
	require.Empty(t, repo.run.ErrorMessage)
	require.True(t, repo.run.HasArtifact)
	require.True(t, repo.run.HasDetail)
	// 评估阶段可继续查询同一远端任务，未知终态仍保留已生成的作品供查看。
	result = manxue.TestResult{}
	require.NoError(t, json.Unmarshal([]byte(`{"id":"remote-classifying","benchmark":"pelican","status":"succeeded","phase":"completed","assessment":{"quality":"unknown","reason":"无法给出明确判断"}}`), &result))
	s.applyResult(context.Background(), repo.run, &result, key.Key)
	require.Len(t, repo.saves, 2)
	require.Equal(t, "unknown", repo.run.Status)
	require.Equal(t, "unknown", repo.run.Verdict)
	require.Equal(t, "completed", repo.run.Phase)
	require.Equal(t, "<p>待评估作品</p>", repo.run.HTML)
	require.NotNil(t, repo.run.FinishedAt)
	require.True(t, repo.run.HasDetail)
}

func TestIntelligenceUserVisibilityAndLightweightList(t *testing.T) {
	s, repo, _, _ := newIntelligenceFixture()
	run := intelligenceQueued()
	run.HTML = "<html>large</html>"
	run.Answer = "private answer"
	run.Question = "question"
	run.HasArtifact = true
	run.HasDetail = true
	repo.run = run
	repo.runs = []IntelligenceRun{*run}
	list, err := s.UserList(context.Background(), 99)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Empty(t, list[0].Runs[0].HTML)
	require.Empty(t, list[0].Artifacts[0].HTML)
	require.True(t, list[0].Artifacts[0].HasArtifact)
	data, err := s.UserDetail(context.Background(), 99, run.ID)
	require.NoError(t, err)
	require.Equal(t, run.HTML, data.HTML)
	s.available = intelligenceGroupStub{}
	list, err = s.UserList(context.Background(), 99)
	require.NoError(t, err)
	require.Empty(t, list)
	_, err = s.UserDetail(context.Background(), 99, run.ID)
	require.ErrorIs(t, err, ErrIntelligenceNotFound)
}
func TestIntelligencePollFailureKeepsRemoteTask(t *testing.T) {
	s, repo, client, _ := newIntelligenceFixture()
	run := intelligenceQueued()
	run.Status = "running"
	run.RemoteID = "remote"
	client.err = errors.New("network")
	s.execute(context.Background(), run)
	require.Zero(t, client.creates)
	require.Equal(t, "running", repo.run.Status)
	require.Equal(t, "remote", repo.run.RemoteID)
	client.err = &manxue.HTTPError{StatusCode: 429, RetryAfter: 5 * time.Minute}
	before := time.Now()
	s.execute(context.Background(), run)
	require.GreaterOrEqual(t, repo.nextPoll.Sub(before), 5*time.Minute)
	// 大画图结果完整保留；列表不携带正文，避免在状态刷新时重复传输。
	run.Benchmark = "drawing"
	body := strings.Repeat("a", 512*1024)
	s.applyResult(context.Background(), run, &manxue.TestResult{ID: "remote", Benchmark: "pelican", Status: "succeeded", Result: &manxue.DrawingResult{HTML: body}, Assessment: &manxue.Assessment{Quality: "good"}}, "")
	require.Equal(t, body, repo.run.HTML)
}
