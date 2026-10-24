package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/chanshui"
	"github.com/stretchr/testify/require"
)

// 编排测试仅使用内存替身，观察真实提交顺序与处置条件，不访问远端或账务。
type chanshuiRepoStub struct {
	ChanshuiRepository
	config   *ChanshuiConfig
	run      *ChanshuiRun
	saves    []ChanshuiRun
	decision *ChanshuiDisableDecision
	next     time.Time
	slot     time.Time
	denySave bool
	finals   int
}

func (r *chanshuiRepoStub) GetAccountRevision(context.Context, int64) (int64, error) { return 0, nil }

func (r *chanshuiRepoStub) GetConfig(context.Context, int64) (*ChanshuiConfig, error) {
	c := *r.config
	return &c, nil
}
func (r *chanshuiRepoStub) SaveConfig(_ context.Context, c *ChanshuiConfig) error {
	copy := *c
	r.config = &copy
	return nil
}
func (r *chanshuiRepoStub) CreateRun(context.Context, int64, time.Time) (*ChanshuiRun, error) {
	return chanshuiQueued(), nil
}
func (r *chanshuiRepoStub) AcquireSubmitSlot(_ context.Context, now time.Time) (time.Time, error) {
	if !r.slot.IsZero() {
		return r.slot, nil
	}
	return now, nil
}
func (r *chanshuiRepoStub) SaveRun(ctx context.Context, run *ChanshuiRun, next time.Time, _ bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if r.denySave {
		return false, nil
	}
	copy := *run
	r.run = &copy
	r.saves = append(r.saves, copy)
	r.next = next
	return true, nil
}
func (r *chanshuiRepoStub) FinalizeRun(_ context.Context, run *ChanshuiRun, d *ChanshuiDisableDecision) (bool, error) {
	r.finals++
	r.decision = d
	copy := *run
	r.run = &copy
	return true, nil
}

type chanshuiAccountsStub struct{ account *Account }

func (r *chanshuiAccountsStub) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}
func (r *chanshuiAccountsStub) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Account, error) {
	return []Account{*r.account}, nil
}

type chanshuiClientStub struct {
	creates, gets int
	result        *chanshui.Result
	err           error
	create        func(chanshui.CreateRequest)
}

func (c *chanshuiClientStub) Create(_ context.Context, in chanshui.CreateRequest) (*chanshui.Result, error) {
	c.creates++
	if c.create != nil {
		c.create(in)
	}
	return c.result, c.err
}
func (c *chanshuiClientStub) Get(context.Context, string) (*chanshui.Result, error) {
	c.gets++
	return c.result, c.err
}
func newChanshuiFixture() (*ChanshuiService, *chanshuiRepoStub, *chanshuiClientStub, *Account) {
	a := &Account{ID: 7, Name: "上游测试账号", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, UpdatedAt: time.Now().UTC(), Credentials: map[string]any{"api_key": "sk-upstream-secret", "base_url": "https://upstream.example/v1", "model_mapping": map[string]any{"local-alias": "gpt-5"}}}
	r := &chanshuiRepoStub{config: &ChanshuiConfig{ID: 1, AccountID: 7, Model: "local-alias", Protocol: "chat", Sections: []string{"fingerprint", "iq"}, TimeoutSeconds: 360, Enabled: true, IntervalMinutes: 60}}
	c := &chanshuiClientStub{result: &chanshui.Result{ID: "0123456789abcdef0123456789abcdef", Status: "running"}}
	s := NewChanshuiService(r, &chanshuiAccountsStub{a}, c, func(context.Context) bool { return true })
	s.resolvePublicHost = func(string) error { return nil }
	return s, r, c, a
}
func chanshuiQueued() *ChanshuiRun {
	return &ChanshuiRun{ID: "cs_local", ConfigID: 1, AccountID: 7, Model: "local-alias", Status: "queued", CreatedAt: time.Now().UTC(), LeaseToken: "lease"}
}
func chanshuiInput() ChanshuiConfigInput {
	return ChanshuiConfigInput{AccountID: 7, Model: "local-alias", Protocol: "chat", Enabled: true}
}

func TestChanshuiConfigurationValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ChanshuiConfigInput)
	}{
		{"账号", func(i *ChanshuiConfigInput) { i.AccountID = 0 }},
		{"模型", func(i *ChanshuiConfigInput) { i.Model = "bad\nmodel" }},
		{"协议", func(i *ChanshuiConfigInput) { i.Protocol = "gemini" }},
		{"超时", func(i *ChanshuiConfigInput) { i.TimeoutSeconds = 601 }},
		{"间隔", func(i *ChanshuiConfigInput) { i.IntervalMinutes = 4 }},
		{"分数", func(i *ChanshuiConfigInput) { v := math.NaN(); i.MinimumScore = &v }},
		{"重复板块", func(i *ChanshuiConfigInput) { i.Sections = []string{"iq", "iq"} }},
		{"未知板块", func(i *ChanshuiConfigInput) { i.Sections = []string{"other"} }},
		{"缺少指纹", func(i *ChanshuiConfigInput) { i.Sections = []string{"iq"}; i.DisableOnAdulteration = true }},
		{"不可计分", func(i *ChanshuiConfigInput) { i.Sections = []string{"knowledge"}; v := 60.0; i.MinimumScore = &v }},
		{"恢复缺少定时", func(i *ChanshuiConfigInput) { i.AutoRecover = true; i.DisableOnAdulteration = true }},
		{"恢复缺少停用规则", func(i *ChanshuiConfigInput) { i.AutoRecover = true; i.ScheduleEnabled = true }},
		{"恢复缺少指纹", func(i *ChanshuiConfigInput) {
			i.AutoRecover = true
			i.ScheduleEnabled = true
			i.Sections = []string{"iq"}
			v := 60.0
			i.MinimumScore = &v
		}},
		{"恢复配置关闭", func(i *ChanshuiConfigInput) {
			i.AutoRecover = true
			i.ScheduleEnabled = true
			i.DisableOnAdulteration = true
			i.Enabled = false
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := chanshuiInput()
			tc.mutate(&in)
			require.Error(t, normalizeChanshuiInput(&in))
		})
	}
	s, r, _, a := newChanshuiFixture()
	saved, err := s.SaveConfig(context.Background(), 0, chanshuiInput())
	require.NoError(t, err)
	require.Len(t, saved.Sections, 8)
	require.Equal(t, 360, saved.TimeoutSeconds)
	require.Equal(t, 60, saved.IntervalMinutes)
	data, err := json.Marshal(saved)
	require.NoError(t, err)
	require.NotContains(t, string(data), a.GetCredential("api_key"))
	require.NotContains(t, string(data), "base_url")
	r.config.ID = 1
	in := chanshuiInput()
	in.Model = "new"
	_, err = s.SaveConfig(context.Background(), 1, in)
	require.Error(t, err)
	a.Status = StatusDisabled
	in = chanshuiInput()
	in.Enabled = false
	_, err = s.SaveConfig(context.Background(), 1, in)
	require.NoError(t, err)
}

// 自动恢复需要当前账号所有权与完整通过证据；人工停用和未知结果绝不被接管。
func TestChanshuiRecoveryDecisionRequiresCompleteCurrentEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ChanshuiConfig, *chanshui.Result, *Account)
		want   bool
	}{
		{"全部达标", func(*ChanshuiConfig, *chanshui.Result, *Account) {}, true},
		{"分数不完整", func(_ *ChanshuiConfig, r *chanshui.Result, _ *Account) { r.ScoreComplete = false }, false},
		{"指纹未知", func(_ *ChanshuiConfig, r *chanshui.Result, _ *Account) { r.Verdict = "unknown" }, false},
		{"无所有权", func(c *ChanshuiConfig, _ *chanshui.Result, _ *Account) { c.RecoveryPending = false }, false},
		{"规则关闭", func(c *ChanshuiConfig, _ *chanshui.Result, _ *Account) { c.AutoRecover = false }, false},
		{"低分", func(_ *ChanshuiConfig, r *chanshui.Result, _ *Account) { v := 1.0; r.Score = &v }, false},
		{"账号已改", func(_ *ChanshuiConfig, _ *chanshui.Result, a *Account) { a.Credentials["api_key"] = "replacement" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, client, a := newChanshuiFixture()
			a.Status, a.Schedulable = StatusDisabled, false
			repo.config.AutoRecover, repo.config.ScheduleEnabled, repo.config.RecoveryPending = true, true, true
			minimum, score := 70.0, 90.0
			repo.config.MinimumScore = &minimum
			run := chanshuiQueued()
			run.Status, run.RemoteID = "running", client.result.ID
			run.AccountSnapshot, run.AccountUpdatedAt = chanshuiAccountSnapshot(a), &a.UpdatedAt
			result := &chanshui.Result{ID: run.RemoteID, Status: "done", Verdict: "clean", Score: &score, ScoreComplete: true}
			tc.mutate(repo.config, result, a)
			s.applyResult(context.Background(), run, result)
			require.Equal(t, tc.want, repo.decision != nil && repo.decision.Recover)
		})
	}
}

func TestChanshuiScheduledRetestOnlyForOwnedSuspension(t *testing.T) {
	for _, owned := range []bool{false, true} {
		s, repo, client, a := newChanshuiFixture()
		a.Status, a.Schedulable = StatusDisabled, false
		repo.config.AutoRecover, repo.config.ScheduleEnabled, repo.config.RecoveryPending = true, true, owned
		run := chanshuiQueued()
		run.Trigger = "schedule"
		s.execute(context.Background(), run)
		if owned {
			require.Equal(t, 1, client.creates)
		} else {
			require.Zero(t, client.creates)
		}
	}
}

func TestChanshuiDirectAccountSubmissionAndSafeTargets(t *testing.T) {
	s, r, c, a := newChanshuiFixture()
	c.create = func(in chanshui.CreateRequest) {
		require.Equal(t, "submitting", r.saves[len(r.saves)-1].Status)
		require.Equal(t, a.GetCredential("api_key"), in.APIKey)
		require.Equal(t, "gpt-5", in.Model)
		require.Equal(t, "https://upstream.example/v1", in.BaseURL)
		require.Equal(t, "chat", in.Protocol)
		require.Equal(t, 360, in.Timeout)
	}
	s.execute(context.Background(), chanshuiQueued())
	require.Equal(t, 1, c.creates)
	require.Equal(t, "running", r.run.Status)
	require.NotEmpty(t, r.run.AccountSnapshot)
	targets, err := s.Targets(context.Background())
	require.NoError(t, err)
	require.Contains(t, targets[0].Models, "local-alias")
	require.Contains(t, targets[0].Models, "gpt-5.6-sol")
	raw, err := json.Marshal(targets)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "sk-upstream")
	require.NotContains(t, string(raw), "upstream.example")
}

func TestChanshuiSubmissionRecoveryAndRateGate(t *testing.T) {
	t.Run("落库失败不提交", func(t *testing.T) {
		s, r, c, _ := newChanshuiFixture()
		r.denySave = true
		s.execute(context.Background(), chanshuiQueued())
		require.Zero(t, c.creates)
	})
	t.Run("未知提交不重发", func(t *testing.T) {
		s, r, c, _ := newChanshuiFixture()
		run := chanshuiQueued()
		run.Status = "submitting"
		s.execute(context.Background(), run)
		require.Zero(t, c.creates)
		require.Equal(t, "unknown", r.run.Status)
	})
	t.Run("网络错误不重发", func(t *testing.T) {
		s, r, c, _ := newChanshuiFixture()
		c.err = errors.New("sk-private should not leak")
		s.execute(context.Background(), chanshuiQueued())
		require.Equal(t, 1, c.creates)
		require.Equal(t, "unknown", r.run.Status)
		require.NotContains(t, r.run.ErrorMessage, "sk-private")
	})
	t.Run("忙时仅排队", func(t *testing.T) {
		s, r, c, _ := newChanshuiFixture()
		r.slot = time.Now().Add(time.Minute)
		s.execute(context.Background(), chanshuiQueued())
		require.Zero(t, c.creates)
		require.Equal(t, "queued", r.run.Status)
		require.Equal(t, r.slot, r.next)
	})
	t.Run("明确限流才重排", func(t *testing.T) {
		s, r, c, _ := newChanshuiFixture()
		c.err = &chanshui.HTTPError{StatusCode: 429, RetryAfter: 90 * time.Second}
		s.execute(context.Background(), chanshuiQueued())
		require.Equal(t, 1, c.creates)
		require.Equal(t, "queued", r.run.Status)
		require.WithinDuration(t, time.Now().Add(90*time.Second), r.next, 2*time.Second)
	})
	t.Run("创建被拒绝", func(t *testing.T) {
		s, r, c, _ := newChanshuiFixture()
		c.err = &chanshui.HTTPError{StatusCode: 400}
		s.execute(context.Background(), chanshuiQueued())
		require.Equal(t, "error", r.run.Status)
		require.Nil(t, r.decision)
	})
	t.Run("关闭后不创建", func(t *testing.T) {
		s, r, c, _ := newChanshuiFixture()
		s.enabled = func(context.Context) bool { return false }
		_, err := s.Run(context.Background(), 1)
		require.Error(t, err)
		s.execute(context.Background(), chanshuiQueued())
		require.Zero(t, c.creates)
		require.Equal(t, "error", r.run.Status)
	})
	t.Run("远端过期", func(t *testing.T) {
		s, r, c, _ := newChanshuiFixture()
		run := chanshuiQueued()
		run.Status = "running"
		run.RemoteID = c.result.ID
		past := time.Now().Add(-time.Minute)
		run.RemoteExpiresAt = &past
		s.execute(context.Background(), run)
		require.Zero(t, c.gets)
		require.Equal(t, "error", r.run.Status)
	})
	t.Run("关闭仍查询已提交", func(t *testing.T) {
		s, r, c, _ := newChanshuiFixture()
		s.enabled = func(context.Context) bool { return false }
		run := chanshuiQueued()
		run.Status = "running"
		run.RemoteID = c.result.ID
		c.result.ExpiresAt = time.Now().Add(time.Hour).Unix()
		s.execute(context.Background(), run)
		require.Equal(t, 1, c.gets)
		require.Equal(t, "running", r.run.Status)
		require.NotNil(t, r.run.RemoteExpiresAt)
	})
}

func TestChanshuiAutomaticDisableRequiresReliableUnchangedResult(t *testing.T) {
	for _, tc := range []struct {
		name, status, verdict                     string
		complete, changed, disabled, wantDecision bool
	}{
		{"未知分数不处置", "done", "unknown", false, false, false, false},
		{"有效低分处置", "done", "unknown", true, false, false, true},
		{"可信指纹处置", "done", "adulterated", false, false, false, true},
		{"部分结果不处置", "partial", "adulterated", false, false, false, false},
		{"执行错误不处置", "error", "adulterated", true, false, false, false},
		{"账号变更不处置", "done", "adulterated", true, true, false, false},
		{"开关关闭不处置", "done", "adulterated", true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, c, a := newChanshuiFixture()
			threshold := 80.0
			r.config.MinimumScore = &threshold
			r.config.DisableOnAdulteration = true
			run := chanshuiQueued()
			run.RemoteID = c.result.ID
			run.AccountSnapshot = chanshuiAccountSnapshot(a)
			updated := a.UpdatedAt
			run.AccountUpdatedAt = &updated
			if tc.changed {
				a.Credentials["api_key"] = "replacement-secret"
			}
			if tc.disabled {
				s.enabled = func(context.Context) bool { return false }
			}
			score := 20.0
			result := &chanshui.Result{ID: run.RemoteID, Status: tc.status, Verdict: tc.verdict, Score: &score, ScoreComplete: tc.complete}
			s.applyResult(context.Background(), run, result)
			require.Equal(t, tc.wantDecision, r.decision != nil)
			require.Equal(t, 1, r.finals)
			if tc.changed {
				require.Equal(t, "skipped_changed", r.run.ActionStatus)
			}
		})
	}
	t.Run("运行时间改变不等同配置变化", func(t *testing.T) {
		s, r, c, a := newChanshuiFixture()
		r.config.DisableOnAdulteration = true
		run := chanshuiQueued()
		run.RemoteID = c.result.ID
		run.AccountSnapshot = chanshuiAccountSnapshot(a)
		old := a.UpdatedAt
		run.AccountUpdatedAt = &old
		a.UpdatedAt = a.UpdatedAt.Add(time.Second)
		s.applyResult(context.Background(), run, &chanshui.Result{ID: run.RemoteID, Status: "done", Verdict: "adulterated"})
		require.NotNil(t, r.decision)
		require.Equal(t, a.UpdatedAt, r.decision.AccountUpdatedAt)
	})
}
