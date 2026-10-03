package service

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/manxue"
	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
	"github.com/google/uuid"
)

var (
	ErrIntelligenceNotFound = infraerrors.NotFound("INTELLIGENCE_NOT_FOUND", "检测配置或记录不存在")
	ErrIntelligenceDisabled = infraerrors.Forbidden("INTELLIGENCE_DISABLED", "降智检测未开启")
	ErrIntelligenceBusy     = infraerrors.Conflict("INTELLIGENCE_BUSY", "当前配置正在检测，请等待本次检测结束")
)

// 兼容旧密钥已被删除的场景，观测正文不保留回显的站内 sk 凭据。
var intelligenceKeyPattern = regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`)

// IntelligenceService 只编排独立检测，不修改原有账号调度、额度或计费数据。
type IntelligenceService struct {
	repo              IntelligenceRepository
	keys              APIKeyRepository
	groups            GroupRepository
	available         IntelligenceGroupReader
	client            IntelligenceClient
	enabled           func(context.Context) bool
	resolvePublicHost func(string) error
	ctx               context.Context
	cancel            context.CancelFunc
	start             sync.Once
	stop              sync.Once
	wg                sync.WaitGroup
}

func NewIntelligenceService(repo IntelligenceRepository, keys APIKeyRepository, groups GroupRepository, available IntelligenceGroupReader, client IntelligenceClient, enabled func(context.Context) bool) *IntelligenceService {
	ctx, cancel := context.WithCancel(context.Background())
	return &IntelligenceService{repo: repo, keys: keys, groups: groups, available: available, client: client, enabled: enabled, resolvePublicHost: urlvalidator.ValidateResolvedIP, ctx: ctx, cancel: cancel}
}
func (s *IntelligenceService) isEnabled(ctx context.Context) bool {
	return s != nil && s.enabled != nil && s.enabled(ctx)
}

func (s *IntelligenceService) AdminList(ctx context.Context) ([]IntelligenceConfig, error) {
	configs, err := s.repo.ListConfigs(ctx)
	if err != nil {
		return nil, err
	}
	for i := range configs {
		runs, e := s.repo.ListRuns(ctx, configs[i].ID)
		if e != nil {
			return nil, e
		}
		if len(runs) > 0 {
			v := intelligenceSummary(runs[0])
			configs[i].LatestRun = &v
		}
	}
	return configs, nil
}

func (s *IntelligenceService) SaveConfig(ctx context.Context, id int64, in IntelligenceConfigInput) (*IntelligenceConfig, error) {
	in.Model = strings.TrimSpace(in.Model)
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	in.APIKey = strings.TrimSpace(in.APIKey)
	if in.GroupID <= 0 || in.Model == "" || len(in.Model) > 255 || (in.Benchmark != "candy" && in.Benchmark != "drawing") {
		return nil, intelligenceInvalid("请选择有效分组、模型和测试类型")
	}
	if in.IntervalMinutes == 0 {
		in.IntervalMinutes = 60
	}
	if in.IntervalMinutes < 5 || in.IntervalMinutes > 10080 {
		return nil, intelligenceInvalid("检测间隔须为5至10080分钟")
	}
	if in.Protocol == "" {
		in.Protocol = "responses"
	}
	if !intelligenceChoice(in.Protocol, "responses", "chat_completions") || (in.Benchmark == "candy" && in.Protocol != "responses") {
		return nil, intelligenceInvalid("糖果测试仅支持 Responses；画图支持 Responses 或 Chat Completions")
	}
	if !intelligenceChoice(in.ReasoningEffort, "", "low", "medium", "high", "xhigh", "max", "ultra") || (in.Benchmark == "drawing" && !intelligenceChoice(in.ReasoningEffort, "", "low", "medium", "high")) {
		return nil, intelligenceInvalid("不支持的推理程度")
	}
	if !intelligenceChoice(in.ServiceTier, "", "priority", "ultrafast") {
		return nil, intelligenceInvalid("不支持的服务档位")
	}
	base, err := validateIntelligenceBaseURL(in.BaseURL)
	if err != nil {
		return nil, err
	}
	group, err := s.groups.GetByID(ctx, in.GroupID)
	if err != nil || group == nil || group.Status != StatusActive {
		return nil, intelligenceInvalid("分组不存在或已停用")
	}
	c := &IntelligenceConfig{ID: id, GroupID: in.GroupID, GroupName: group.Name, Model: in.Model, Benchmark: in.Benchmark, BaseURL: base, Protocol: in.Protocol, ReasoningEffort: in.ReasoningEffort, ServiceTier: in.ServiceTier, Enabled: in.Enabled, ScheduleEnabled: in.ScheduleEnabled, IntervalMinutes: in.IntervalMinutes}
	if id > 0 {
		old, e := s.repo.GetConfig(ctx, id)
		if e != nil {
			return nil, e
		}
		// 身份字段固定，避免旧记录被移到另一个分组或型号下展示。
		if old.GroupID != c.GroupID || old.Model != c.Model || old.Benchmark != c.Benchmark {
			return nil, intelligenceInvalid("已有配置的分组、模型和测试类型不能更改，请新增配置")
		}
		c.APIKeyID = old.APIKeyID
	}
	var key *APIKey
	if in.APIKey != "" {
		key, err = s.keys.GetByKey(ctx, in.APIKey)
	} else if c.APIKeyID > 0 {
		key, err = s.keys.GetByID(ctx, c.APIKeyID)
	}
	if err != nil || !intelligenceValidKey(key, c.GroupID) {
		return nil, intelligenceInvalid("需要有效、未过期且仅绑定所选分组的普通站内 API Key，不支持复合或智能路由 Key")
	}
	c.APIKeyID = key.ID
	c.APIKeyConfigured = true
	if err = s.repo.SaveConfig(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}
func intelligenceChoice(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}
func intelligenceInvalid(message string) error {
	return infraerrors.BadRequest("INTELLIGENCE_INVALID", message)
}
func intelligenceValidKey(key *APIKey, groupID int64) bool {
	return key != nil && key.Key != "" && key.IsActive() && !key.IsExpired() && !key.IsQuotaExhausted() && !key.IsComposite && !key.SmartRouting && len(key.CompositeGroups) == 0 && key.GroupID != nil && *key.GroupID == groupID && key.ManagedBy == nil && !key.FallbackToDefaultGroupWhenUnavailable && key.User != nil && key.User.Status == StatusActive && key.User.DeletedAt == nil
}
func validateIntelligenceBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "\r\n") {
		return "", intelligenceInvalid("检测 Base URL 必须为公网 HTTPS 地址，不能包含凭据、查询参数或片段")
	}
	normalized, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{})
	if err != nil {
		return "", intelligenceInvalid("检测 Base URL 必须为公网 HTTPS 地址")
	}
	return normalized, nil
}
func (s *IntelligenceService) DeleteConfig(ctx context.Context, id int64) error {
	return s.repo.DeleteConfig(ctx, id)
}
func (s *IntelligenceService) Run(ctx context.Context, id int64) (*IntelligenceRun, error) {
	if !s.isEnabled(ctx) {
		return nil, ErrIntelligenceDisabled
	}
	c, err := s.repo.GetConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, ErrIntelligenceDisabled
	}
	key, err := s.keys.GetByID(ctx, c.APIKeyID)
	if err != nil || !intelligenceValidKey(key, c.GroupID) {
		return nil, intelligenceInvalid("检测 Key 不可用或已更换分组，请更新配置")
	}
	return s.repo.CreateRun(ctx, id, time.Now().UTC())
}
func (s *IntelligenceService) AdminRuns(ctx context.Context, id int64) ([]IntelligenceRun, error) {
	if _, err := s.repo.GetConfig(ctx, id); err != nil {
		return nil, err
	}
	runs, err := s.repo.ListRuns(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		runs[i] = intelligenceSummary(runs[i])
	}
	return runs, nil
}
func (s *IntelligenceService) AdminDetail(ctx context.Context, id string) (*IntelligenceRun, error) {
	return s.repo.GetRun(ctx, id)
}
func intelligenceSummary(run IntelligenceRun) IntelligenceRun {
	run.HTML = ""
	run.Question = ""
	run.Answer = ""
	run.AssessmentReason = ""
	return run
}

func (s *IntelligenceService) visibleGroups(ctx context.Context, userID int64) (map[int64]bool, error) {
	groups, err := s.available.GetAvailableGroups(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, g := range groups {
		out[g.ID] = true
	}
	return out, nil
}
func (s *IntelligenceService) UserList(ctx context.Context, userID int64) ([]IntelligenceTest, error) {
	if !s.isEnabled(ctx) {
		return nil, ErrIntelligenceDisabled
	}
	visible, err := s.visibleGroups(ctx, userID)
	if err != nil {
		return nil, err
	}
	configs, err := s.repo.ListConfigs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]IntelligenceTest, 0)
	for _, c := range configs {
		if !c.Enabled || !visible[c.GroupID] {
			continue
		}
		runs, err := s.repo.ListRuns(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		test := IntelligenceTest{ID: c.ID, GroupID: c.GroupID, GroupName: c.GroupName, Model: c.Model, Benchmark: c.Benchmark, Runs: make([]IntelligenceRun, 0, len(runs)), Artifacts: make([]IntelligenceRun, 0)}
		for _, run := range runs {
			test.Runs = append(test.Runs, intelligenceSummary(run))
			if run.HasArtifact && len(test.Artifacts) < 10 {
				test.Artifacts = append(test.Artifacts, intelligenceSummary(run))
			}
		}
		out = append(out, test)
	}
	return out, nil
}
func (s *IntelligenceService) UserDetail(ctx context.Context, userID int64, id string) (*IntelligenceRun, error) {
	if !s.isEnabled(ctx) {
		return nil, ErrIntelligenceDisabled
	}
	run, err := s.repo.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	c, err := s.repo.GetConfig(ctx, run.ConfigID)
	if err != nil {
		return nil, err
	}
	visible, err := s.visibleGroups(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !c.Enabled || !visible[run.GroupID] || c.GroupID != run.GroupID {
		return nil, ErrIntelligenceNotFound
	}
	return run, nil
}

// Start 固定两个 worker，已提交远端的任务在总开关关闭后仍可完成只读轮询。
func (s *IntelligenceService) Start() {
	s.start.Do(func() {
		for i := 0; i < 2; i++ {
			s.wg.Add(1)
			go s.worker(i)
		}
	})
}
func (s *IntelligenceService) Stop() { s.stop.Do(func() { s.cancel(); s.wg.Wait() }) }
func (s *IntelligenceService) worker(index int) {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, 75*time.Second)
			if index == 0 && s.isEnabled(ctx) {
				_ = s.repo.ScheduleDue(ctx, time.Now().UTC(), 2)
			}
			now := time.Now().UTC()
			run, err := s.repo.ClaimRun(ctx, now, now.Add(2*time.Minute), uuid.NewString())
			if err == nil && run != nil {
				s.execute(ctx, run)
			}
			cancel()
		}
	}
}

func (s *IntelligenceService) finish(ctx context.Context, run *IntelligenceRun, status, verdict, message string) {
	// 已发送请求的收尾与客户端取消分离，但数据库收尾仍有独立短超时。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	now := time.Now().UTC()
	run.Status = status
	run.Verdict = verdict
	run.ErrorMessage = message
	run.UpdatedAt = now
	run.FinishedAt = &now
	run.HasDetail = true
	_, _ = s.repo.SaveRun(ctx, run, now, false)
}

// execute 只有持久状态 queued 能发送一次 POST；submitting 恢复时绝不重发。
func (s *IntelligenceService) execute(ctx context.Context, run *IntelligenceRun) {
	if run.Status == "submitting" && run.RemoteID == "" {
		s.finish(ctx, run, "unknown", "unknown", "上次提交结果不确定，未自动重新发起检测，请先核对使用记录")
		return
	}
	if time.Since(run.CreatedAt) > 30*time.Minute {
		s.finish(ctx, run, "error", "error", "检测超过等待时限，未自动重试")
		return
	}
	if run.Status == "queued" {
		if !s.isEnabled(ctx) {
			s.finish(ctx, run, "error", "error", "降智检测已关闭，本次尚未提交")
			return
		}
		c, err := s.repo.GetConfig(ctx, run.ConfigID)
		if err != nil || !c.Enabled {
			s.finish(ctx, run, "error", "error", "检测配置不可用")
			return
		}
		group, err := s.groups.GetByID(ctx, c.GroupID)
		if err != nil || group == nil || group.Status != StatusActive {
			s.finish(ctx, run, "error", "error", "分组不可用")
			return
		}
		key, err := s.keys.GetByID(ctx, c.APIKeyID)
		if err != nil || !intelligenceValidKey(key, c.GroupID) {
			s.finish(ctx, run, "error", "error", "检测 Key 不可用或分组已变更")
			return
		}
		u, err := url.Parse(c.BaseURL)
		if err != nil || s.resolvePublicHost(u.Hostname()) != nil {
			s.finish(ctx, run, "error", "error", "检测目标必须可以解析为公网地址")
			return
		}
		run.Status = "submitting"
		run.UpdatedAt = time.Now().UTC()
		ok, err := s.repo.SaveRun(ctx, run, time.Now().UTC(), true)
		if err != nil || !ok {
			return
		}
		benchmark := c.Benchmark
		if benchmark == "drawing" {
			benchmark = "pelican"
		}
		result, err := s.client.Create(ctx, manxue.CreateRequest{Benchmark: benchmark, BaseURL: c.BaseURL, APIKey: key.Key, Model: c.Model, Protocol: c.Protocol, ReasoningEffort: c.ReasoningEffort, ServiceTier: c.ServiceTier}, run.ID)
		if err != nil {
			status := "unknown"
			var httpErr *manxue.HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode >= 400 && httpErr.StatusCode < 500 {
				status = "error"
			}
			s.finish(context.WithoutCancel(ctx), run, status, status, "检测提交未成功确认，未自动重试，请核对站内使用记录")
			return
		}
		if result == nil || result.ID == "" {
			s.finish(ctx, run, "unknown", "unknown", "检测服务未返回有效任务编号，未自动重试")
			return
		}
		run.RemoteID = result.ID
		s.applyResult(ctx, run, result, key.Key)
		return
	}
	if run.RemoteID == "" {
		s.finish(ctx, run, "unknown", "unknown", "检测任务缺少远端编号，无法继续查询")
		return
	}
	result, err := s.client.Get(ctx, run.RemoteID)
	if err != nil {
		var he *manxue.HTTPError
		if errors.As(err, &he) && (he.StatusCode == 404 || he.StatusCode == 410) {
			s.finish(ctx, run, "error", "error", "检测服务中的结果已失效")
			return
		}
		delay := 15 * time.Second
		if errors.As(err, &he) && he.RetryAfter > delay {
			delay = he.RetryAfter
		}
		// 遵守检测服务给出的退避时间；超过本地总期限时只在期限到达后终止，不提前重打。
		remaining := time.Until(run.CreatedAt.Add(30 * time.Minute))
		if remaining <= 0 {
			s.finish(ctx, run, "error", "error", "检测超过等待时限，未自动重试")
			return
		}
		if delay > remaining && remaining > 0 {
			delay = remaining
		}
		run.UpdatedAt = time.Now().UTC()
		_, _ = s.repo.SaveRun(ctx, run, time.Now().Add(delay), false)
		return
	}
	// 已有任务只轮询；即使本地 Key 被删除或停用，也不再次向检测服务发送该 Key。
	secret := ""
	if c, e := s.repo.GetConfig(ctx, run.ConfigID); e == nil {
		if key, e := s.keys.GetByID(ctx, c.APIKeyID); e == nil && key != nil {
			secret = key.Key
		}
	}
	s.applyResult(ctx, run, result, secret)
}

func (s *IntelligenceService) applyResult(ctx context.Context, run *IntelligenceRun, result *manxue.TestResult, secret string) {
	// 远端已经返回任务编号时，关闭或请求超时不能丢掉这个唯一恢复凭证。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	expectedBenchmark := run.Benchmark
	if expectedBenchmark == "drawing" {
		expectedBenchmark = "pelican"
	}
	if result == nil || result.ID != run.RemoteID || (result.Benchmark != "" && result.Benchmark != expectedBenchmark) {
		s.finish(ctx, run, "error", "error", "检测服务返回的任务信息不一致")
		return
	}
	run.UpdatedAt = time.Now().UTC()
	// 查询接口可以省略类型，此时只使用已持久化的本次检测类型解释结果。
	if result.Benchmark == "" {
		copy := *result
		copy.Benchmark = expectedBenchmark
		result = &copy
	}
	run.Phase = ""
	if intelligenceChoice(result.Phase, "queued", "generating", "testing", "assessing", "completed", "complete", "failed", "finished", "candy", "pelican") {
		run.Phase = result.Phase
	}
	run.Verdict = result.Outcome()
	run.Status = "running"
	redact := func(v string) string {
		if run.RemoteID != "" {
			v = strings.ReplaceAll(v, run.RemoteID, "[已隐藏任务凭证]")
		}
		if secret != "" {
			v = strings.ReplaceAll(v, secret, "[已隐藏密钥]")
		}
		return intelligenceKeyPattern.ReplaceAllString(v, "[已隐藏密钥]")
	}
	if result.Candy != nil {
		run.Question = redact(result.Candy.Question)
		run.Answer = redact(result.Candy.Answer)
		run.InputTokens = result.Candy.InputTokens
		run.OutputTokens = result.Candy.OutputTokens
		run.ReasoningTokens = result.Candy.ReasoningTokens
		run.DurationMS = result.Candy.DurationMS
	}
	if result.Result != nil {
		run.Question = redact(result.Result.Prompt)
		run.HTML = redact(result.Result.HTML)
		run.InputTokens = result.Result.InputTokens
		run.OutputTokens = result.Result.OutputTokens
		run.ReasoningTokens = result.Result.ReasoningTokens
		run.DurationMS = result.Result.DurationMS
	}
	if result.Assessment != nil {
		run.AssessmentReason = redact(result.Assessment.Reason)
	}
	if len(run.HTML) > 2*1024*1024 {
		run.HTML = ""
		run.ErrorMessage = "画图内容超过2MiB保存限制，仅保留检测结果"
	}
	if result.Terminal() {
		run.Status = "completed"
		if run.Verdict == "error" || run.Verdict == "unknown" {
			run.Status = "error"
			run.ErrorMessage = "检测调用失败或未获得明确评判，请查看站内使用记录"
		}
		run.FinishedAt = &run.UpdatedAt
	}
	run.HasArtifact = run.HTML != ""
	run.HasDetail = run.Question != "" || run.Answer != "" || run.HTML != "" || run.AssessmentReason != "" || run.ErrorMessage != ""
	_, _ = s.repo.SaveRun(ctx, run, time.Now().Add(10*time.Second), false)
}
