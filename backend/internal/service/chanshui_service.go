package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/chanshui"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/openai_compat"
	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
	"github.com/google/uuid"
)

// ChanshuiClient 的创建调用不自动重放，查询只使用固定检测服务的任务编号。
type ChanshuiClient interface {
	Create(context.Context, chanshui.CreateRequest) (*chanshui.Result, error)
	Get(context.Context, string) (*chanshui.Result, error)
}

// ChanshuiAccountReader 刻意使用窄接口；检测不拥有任何用户 Key 或账务接口。
type ChanshuiAccountReader interface {
	GetByID(context.Context, int64) (*Account, error)
	ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Account, error)
}

// ChanshuiService 只编排管理员显式配置的上游账号，不经过站内分组或用户收费链路。
// @project-doc docs/domains/chanshui_tests.md#chanshui_execution
type ChanshuiService struct {
	repo              ChanshuiRepository
	accounts          ChanshuiAccountReader
	client            ChanshuiClient
	enabled           func(context.Context) bool
	resolvePublicHost func(string) error
	onDisabled        func(context.Context, int64)
	ctx               context.Context
	cancel            context.CancelFunc
	start, stop       sync.Once
	wg                sync.WaitGroup
}

func NewChanshuiService(repo ChanshuiRepository, accounts ChanshuiAccountReader, client ChanshuiClient, enabled func(context.Context) bool) *ChanshuiService {
	ctx, cancel := context.WithCancel(context.Background())
	return &ChanshuiService{repo: repo, accounts: accounts, client: client, enabled: enabled, resolvePublicHost: urlvalidator.ValidateResolvedIP, ctx: ctx, cancel: cancel}
}
func (s *ChanshuiService) IsEnabled(ctx context.Context) bool {
	return s != nil && s.enabled != nil && s.enabled(ctx)
}
func chanshuiInvalid(message string) error {
	return infraerrors.BadRequest("CHANSHUI_INVALID", message)
}

// ChanshuiTarget 不返回账号凭据、完整账号对象或管理员账户相关信息。
type ChanshuiTarget struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Platform  string   `json:"platform"`
	Type      string   `json:"type"`
	Supported bool     `json:"supported"`
	Reason    string   `json:"reason"`
	Warning   string   `json:"warning,omitempty"`
	Models    []string `json:"models"`
}

func (s *ChanshuiService) Targets(ctx context.Context) ([]ChanshuiTarget, error) {
	// 管理员诊断按凭据类型列举，不受分组、价格或在线调度状态限制。
	accounts, err := s.accounts.ListAllWithFilters(ctx, "", AccountTypeAPIKey, "", "", 0, "")
	if err != nil {
		return nil, err
	}
	result := make([]ChanshuiTarget, 0, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		if a.Type != AccountTypeAPIKey {
			continue
		}
		_, _, reason := chanshuiAccountEndpoint(a, "auto")
		// 列表尚未选协议；某一分协议端点可用就保留选择机会，保存时再校验实际协议。
		if reason != "" {
			for _, protocol := range []string{"chat", "anthropic", "openai"} {
				if _, _, candidateReason := chanshuiAccountEndpoint(a, protocol); candidateReason == "" {
					reason = ""
					break
				}
			}
		}
		models := chanshuiModelCandidates(a)
		result = append(result, ChanshuiTarget{ID: a.ID, Name: a.Name, Platform: a.Platform, Type: a.Type, Supported: reason == "", Reason: reason, Warning: chanshuiAccountWarning(a), Models: models})
	}
	return result, nil
}

// 检测候选合并账号别名、最终白名单与平台目录；每个候选仍按映射后的权限过滤。
func chanshuiModelCandidates(a *Account) []string {
	models := make([]string, 0)
	if a == nil {
		return models
	}
	mapping := a.GetModelMapping()
	whitelist, _ := resolveFinalModelWhitelist(a.Platform, a.Credentials, mapping)
	candidates := make(map[string]struct{})
	for model := range mapping {
		candidates[model] = struct{}{}
	}
	for model := range whitelist {
		candidates[model] = struct{}{}
	}
	// 通配映射通过目录中的具体型号展开；未知平台不能回落到 Claude 目录。
	switch a.Platform {
	case PlatformDeepseek:
		for _, model := range deepseekServableModels {
			candidates[model] = struct{}{}
		}
	case PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformGrok, PlatformAntigravity,
		PlatformQoder, PlatformOpenCodeGo, PlatformMiniMax, PlatformTypeSafe, PlatformCline, PlatformCommandCode:
		for _, model := range defaultRequestModelIDsForPlatform(a.Platform) {
			candidates[model] = struct{}{}
		}
	}
	for model := range candidates {
		if strings.ContainsAny(model, "*?") || !chanshuiAccountModelSupported(a, model) || chanshuiValidateModel(a.GetMappedModel(model), nil) != nil {
			continue
		}
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

// 诊断可直接访问不参与调度的账号；提示第三方无法复用的本地传输配置。
func chanshuiAccountWarning(a *Account) string {
	if a == nil {
		return ""
	}
	warnings := make([]string, 0, 3)
	if a.Status != StatusActive || !a.Schedulable || (a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now())) {
		warnings = append(warnings, "账号当前未参与正常调度，仍可手动检测；仅配置的自动恢复规则可恢复本功能停用的账号")
	}
	if a.ProxyID != nil || len(a.GetHeaderOverrides()) > 0 {
		warnings = append(warnings, "检测服务直接使用此账号的地址与 API Key，不使用本站代理或自定义请求头")
	}
	return strings.Join(warnings, "；")
}

// 自动协议先尊重账号固定设置，再按映射后的文本模型选择；地址和请求必须使用同一协议。
func chanshuiProtocol(a *Account, requested, model string) string {
	if requested != "auto" && requested != "" {
		return requested
	}
	if a == nil {
		return "chat"
	}
	native := ""
	switch {
	case a.Platform == PlatformAnthropic:
		native = APIProtocolAnthropic
	case a.Platform == PlatformGrok:
		native = APIProtocolResponses
	case a.Platform == PlatformOpenAI:
		switch openai_compat.ResolveTextRouteMode(a.Extra) {
		case openai_compat.TextRouteModeForceChatCompletions:
			native = APIProtocolChatCompletions
		case openai_compat.TextRouteModeForceResponses:
			native = APIProtocolResponses
		}
	case a.IsOpenCodeGo():
		native = a.ResolveOpenCodeGoUpstreamProtocol(model)
	case a.IsCNProvider() || isAdditionalAPIKeyPlatform(a.Platform):
		native = a.GetAPIProtocol()
		if a.IsCommandCode() && native == APIProtocolAdaptive {
			if rules, exists := a.openCodeGoProtocolRules(); exists {
				native = matchOpenCodeGoProtocolRules(model, rules)
			}
		}
	default:
		// 未知平台也可明确保存兼容协议，不强制归到某一供应商。
		native = strings.TrimSpace(a.GetCredential("api_protocol"))
	}
	switch native {
	case APIProtocolChatCompletions:
		return "chat"
	case APIProtocolAnthropic:
		return "anthropic"
	case APIProtocolResponses:
		return "openai"
	}
	lower := strings.ToLower(lastOpenAIModelSegment(model))
	if strings.HasPrefix(lower, "claude-") {
		return "anthropic"
	}
	if strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") || strings.HasPrefix(lower, "o4") {
		return "openai"
	}
	return "chat"
}

// 外部检测只使用账号自身的地址与密钥；本地调度状态不影响管理员手动诊断。
func chanshuiAccountEndpoint(a *Account, protocol string) (string, string, string) {
	if a == nil || a.Type != AccountTypeAPIKey {
		return "", "", "仅支持 API Key 类型的上游账号"
	}
	key := strings.TrimSpace(a.GetCredential("api_key"))
	if key == "" || len(key) > 8192 || strings.ContainsAny(key, "\r\n\x00") {
		return "", "", "账号自身未配置有效的上游 API Key，检测不会借用父账号凭据"
	}
	protocol = chanshuiProtocol(a, protocol, "")
	p := APIProtocolChatCompletions
	if protocol == "anthropic" {
		p = APIProtocolAnthropic
	} else if protocol == "openai" {
		p = APIProtocolResponses
	}
	base, hasConfiguredEndpoint := chanshuiConfiguredBaseURL(a, p)
	if base == "" && !hasConfiguredEndpoint {
		// 仅保留已适配文本平台自身的默认地址；未知或原生专用平台不能误投 OpenAI。
		switch {
		case a.Platform == PlatformAnthropic:
			base = a.GetBaseURL()
		case a.Platform == PlatformOpenAI:
			base = a.GetOpenAIBaseURL()
		case a.Platform == PlatformGrok:
			base = a.GetGrokBaseURL()
		case a.IsCNProvider() || a.IsOpenCodeGo() || (isAdditionalAPIKeyPlatform(a.Platform) && !a.IsTypeSafe()):
			base = selectedAccountTestBaseURL(a, p)
		}
	}
	if base == "" {
		return "", "", "请为此账号配置所选协议的 OpenAI/Anthropic 兼容文本地址，检测服务不支持原生专用协议"
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed == nil || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || len(base) > 2048 || strings.ContainsAny(base, "\r\n") {
		return "", "", "上游地址必须为不含凭据、查询参数或片段的公网 HTTP/HTTPS 地址"
	}
	base, err = urlvalidator.ValidateHTTPURL(base, true, urlvalidator.ValidationOptions{})
	if err != nil {
		return "", "", "上游地址必须为公网 HTTP/HTTPS 地址"
	}
	return base, key, ""
}

// 分协议地址优先，通用地址次之；已有中继地址但缺少当前协议时不回退到供应商站点。
func chanshuiConfiguredBaseURL(a *Account, protocol string) (base string, configured bool) {
	switch urls := a.Credentials["api_base_urls"].(type) {
	case map[string]any:
		for _, value := range urls {
			if raw, ok := value.(string); ok && strings.TrimSpace(raw) != "" {
				configured = true
			}
		}
		base, _ = urls[protocol].(string)
	case map[string]string:
		for _, value := range urls {
			if strings.TrimSpace(value) != "" {
				configured = true
			}
		}
		base = urls[protocol]
	}
	base = strings.TrimSpace(base)
	if base == "" {
		base = strings.TrimSpace(a.GetCredential("base_url"))
	}
	return base, configured || base != ""
}

func normalizeChanshuiInput(in *ChanshuiConfigInput) error {
	in.Model = strings.TrimSpace(in.Model)
	if in.AccountID <= 0 || in.Model == "" || len(in.Model) > 255 || strings.ContainsAny(in.Model, "\r\n\x00") {
		return chanshuiInvalid("请选择账号并填写有效模型")
	}
	if in.Protocol == "" {
		in.Protocol = "auto"
	}
	if !intelligenceChoice(in.Protocol, "auto", "chat", "anthropic", "openai") {
		return chanshuiInvalid("不支持的检测协议")
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = 360
	}
	if in.IntervalMinutes == 0 {
		in.IntervalMinutes = 60
	}
	if in.TimeoutSeconds < 10 || in.TimeoutSeconds > 600 || in.IntervalMinutes < 5 || in.IntervalMinutes > 10080 {
		return chanshuiInvalid("单请求超时为10–600秒，定时间隔为5–10080分钟")
	}
	if in.MinimumScore != nil && (math.IsNaN(*in.MinimumScore) || math.IsInf(*in.MinimumScore, 0) || *in.MinimumScore < 0 || *in.MinimumScore > 100) {
		return chanshuiInvalid("最低得分必须在0–100之间，留空表示关闭低分禁用")
	}
	all := []string{"fingerprint", "injection", "cache", "tools", "web", "knowledge", "thinking", "iq"}
	if len(in.Sections) == 0 {
		in.Sections = all
	}
	seen := map[string]bool{}
	for _, v := range in.Sections {
		if !intelligenceChoice(v, all...) || seen[v] {
			return chanshuiInvalid("检测板块无效或重复")
		}
		seen[v] = true
	}
	if in.DisableOnAdulteration && !seen["fingerprint"] {
		return chanshuiInvalid("按掺水禁用必须启用模型指纹板块")
	}
	if in.MinimumScore != nil && len(in.Sections) == 1 && seen["knowledge"] {
		return chanshuiInvalid("知识截止日期不计总分，不能单独配置低分禁用")
	}
	if in.AutoRecover && (!in.Enabled || !in.ScheduleEnabled || !seen["fingerprint"] || (!in.DisableOnAdulteration && in.MinimumScore == nil)) {
		return chanshuiInvalid("自动恢复需启用配置、定时检测、模型指纹及至少一项自动停用规则")
	}
	return nil
}

func (s *ChanshuiService) SaveConfig(ctx context.Context, id int64, in ChanshuiConfigInput) (*ChanshuiConfig, error) {
	if err := normalizeChanshuiInput(&in); err != nil {
		return nil, err
	}
	var old *ChanshuiConfig
	if id > 0 {
		var err error
		old, err = s.repo.GetConfig(ctx, id)
		if err != nil {
			return nil, err
		}
		if old.AccountID != in.AccountID || old.Model != in.Model || old.Protocol != in.Protocol {
			return nil, chanshuiInvalid("已有配置的账号、模型和协议不能修改，请新增配置")
		}
	}
	a, err := s.accounts.GetByID(ctx, in.AccountID)
	if err != nil || a == nil {
		return nil, chanshuiInvalid("上游账号不存在")
	}
	// 被停用的账号仍可关闭旧计划或阈值，避免只能删除历史才能维护配置。
	if old == nil || in.Enabled || in.ScheduleEnabled {
		if _, _, reason := chanshuiAccountEndpoint(a, chanshuiProtocol(a, in.Protocol, a.GetMappedModel(in.Model))); reason != "" {
			return nil, chanshuiInvalid(reason)
		}
		if !chanshuiAccountModelSupported(a, in.Model) {
			return nil, chanshuiInvalid("模型不在此账号允许的模型范围内")
		}
		if err = chanshuiValidateModel(a.GetMappedModel(in.Model), in.Sections); err != nil {
			return nil, err
		}
	}
	c := &ChanshuiConfig{ID: id, AccountID: in.AccountID, AccountName: a.Name, Model: in.Model, Protocol: in.Protocol, Sections: in.Sections, TimeoutSeconds: in.TimeoutSeconds, Enabled: in.Enabled, ScheduleEnabled: in.ScheduleEnabled, IntervalMinutes: in.IntervalMinutes, DisableOnAdulteration: in.DisableOnAdulteration, MinimumScore: in.MinimumScore, AutoRecover: in.AutoRecover}
	if err = s.repo.SaveConfig(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func chanshuiValidateModel(model string, sections []string) error {
	lower := strings.ToLower(lastOpenAIModelSegment(model))
	if len(sections) == 1 && sections[0] == "thinking" && !strings.HasPrefix(lower, "claude-") {
		return chanshuiInvalid("单独思考检测仅支持 claude- 前缀模型")
	}
	if strings.ContainsAny(model, "\r\n\x00") || len(model) > 255 || strings.TrimSpace(model) == "" {
		return chanshuiInvalid("映射后的模型无效")
	}
	// 仅排除明确媒体/特殊协议模型，未知文本别名仍允许管理员显式填写。
	if IsOpenCodeSystemOneModel(model) || strings.Contains(lower, "image") || strings.Contains(lower, "video") || strings.Contains(lower, "tts") || strings.HasPrefix(lower, "mj") {
		return chanshuiInvalid("掺水检测仅支持文本模型，不能用于图片、视频、音频或特殊协议模型")
	}
	return nil
}

// 管理员选择兼容文本协议时，不套用平台原生目录；仍保留账号显式最终白名单与映射。
func chanshuiAccountModelSupported(a *Account, model string) bool {
	if a == nil {
		return false
	}
	whitelist, _ := resolveFinalModelWhitelist(a.Platform, a.Credentials, a.GetModelMapping())
	return isModelInFinalWhitelist(a.Platform, a.GetMappedModel(model), whitelist)
}

// 正常定时检测需要可调度且未过期，自动停用后的复验由独立所有权规则判断。
func chanshuiAccountScheduled(a *Account) bool {
	return a != nil && a.Status == StatusActive && a.Schedulable && (a.ExpiresAt == nil || a.ExpiresAt.After(time.Now()))
}

// 恢复资格由仓储以本功能持有的停用代次计算，手动停用账号不会被定时器接管。
func chanshuiAccountRetesting(a *Account, c *ChanshuiConfig) bool {
	return a != nil && c != nil && c.AutoRecover && c.Enabled && c.ScheduleEnabled && c.RecoveryPending && a.Status == StatusDisabled && !a.Schedulable && (a.ExpiresAt == nil || a.ExpiresAt.After(time.Now()))
}
func (s *ChanshuiService) AdminList(ctx context.Context) ([]ChanshuiConfig, error) {
	cs, err := s.repo.ListConfigs(ctx)
	if err != nil {
		return nil, err
	}
	return s.populateRecentRuns(ctx, cs)
}
func (s *ChanshuiService) DeleteConfig(ctx context.Context, id int64) error {
	return s.repo.DeleteConfig(ctx, id)
}
func (s *ChanshuiService) AdminDetail(ctx context.Context, id string) (*ChanshuiRun, error) {
	return s.repo.GetRun(ctx, id)
}
func (s *ChanshuiService) AdminRuns(ctx context.Context, id int64) ([]ChanshuiRun, error) {
	if _, err := s.repo.GetConfig(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.ListRuns(ctx, id)
}
func (s *ChanshuiService) Run(ctx context.Context, id int64) (*ChanshuiRun, error) {
	if !s.IsEnabled(ctx) {
		return nil, ErrChanshuiDisabled
	}
	c, err := s.repo.GetConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, ErrChanshuiDisabled
	}
	a, err := s.accounts.GetByID(ctx, c.AccountID)
	if err != nil || a == nil {
		return nil, chanshuiInvalid("上游账号不可用")
	}
	if _, _, reason := chanshuiAccountEndpoint(a, chanshuiProtocol(a, c.Protocol, a.GetMappedModel(c.Model))); reason != "" {
		return nil, chanshuiInvalid(reason)
	}
	if !chanshuiAccountModelSupported(a, c.Model) {
		return nil, chanshuiInvalid("模型已不在账号允许范围内")
	}
	if err = chanshuiValidateModel(a.GetMappedModel(c.Model), c.Sections); err != nil {
		return nil, err
	}
	return s.repo.CreateRun(ctx, id, time.Now().UTC())
}

func (s *ChanshuiService) Start() {
	s.start.Do(func() {
		for i := 0; i < 2; i++ {
			s.wg.Add(1)
			go s.worker(i)
		}
	})
}
func (s *ChanshuiService) Stop() { s.stop.Do(func() { s.cancel(); s.wg.Wait() }) }
func (s *ChanshuiService) worker(index int) {
	defer s.wg.Done()
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			ctx, cancel := context.WithTimeout(s.ctx, 75*time.Second)
			if index == 0 && s.IsEnabled(ctx) {
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

func (s *ChanshuiService) finish(ctx context.Context, run *ChanshuiRun, status, message string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	now := time.Now().UTC()
	run.Status = status
	run.Verdict = "unknown"
	run.ErrorMessage = message
	run.UpdatedAt = now
	run.FinishedAt = &now
	run.ActionStatus = "none"
	_, _ = s.repo.FinalizeRun(ctx, run, nil)
}

func chanshuiAccountSnapshot(a *Account) string {
	// 哈希完整凭据只用于比较，数据库和日志不保存凭据；运行时使用时间不属于配置身份。
	data, err := json.Marshal(struct {
		Platform, Type, Status string
		Credentials            map[string]any
		TextRouteMode          openai_compat.TextRouteMode
		ProxyID, Parent        *int64
		Schedulable            bool
		Expires                *time.Time
	}{a.Platform, a.Type, a.Status, a.Credentials, openai_compat.ResolveTextRouteMode(a.Extra), a.ProxyID, a.ParentAccountID, a.Schedulable, a.ExpiresAt})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// execute 持久 queued -> submitting 是唯一发送入口；未知受理不自动再发收费请求。
func (s *ChanshuiService) execute(ctx context.Context, run *ChanshuiRun) {
	if run.Status == "submitting" && run.RemoteID == "" {
		s.finish(ctx, run, "unknown", "上次提交是否受理无法确认，未自动重发；请核对上游使用记录")
		return
	}
	if time.Since(run.CreatedAt) > 24*time.Hour {
		s.finish(ctx, run, "error", "检测记录已超过24小时观察期限，未自动重试")
		return
	}
	if run.RemoteExpiresAt != nil && !run.RemoteExpiresAt.After(time.Now()) {
		s.finish(ctx, run, "error", "检测服务中的任务已过期，未重新生成")
		return
	}
	if run.Status == "queued" {
		if !s.IsEnabled(ctx) {
			s.finish(ctx, run, "error", "掺水检测已关闭，本次尚未提交")
			return
		}
		c, err := s.repo.GetConfig(ctx, run.ConfigID)
		if err != nil || !c.Enabled {
			s.finish(ctx, run, "error", "检测配置不可用")
			return
		}
		// 独立配置代次由数据库维护，人工停用后恢复也会使旧任务失去自动处置资格。
		revision, err := s.repo.GetAccountRevision(ctx, c.AccountID)
		if err != nil {
			s.finish(ctx, run, "error", "无法核对账号配置版本，本次尚未提交")
			return
		}
		a, err := s.accounts.GetByID(ctx, c.AccountID)
		if err != nil || a == nil {
			s.finish(ctx, run, "error", "上游账号不可用")
			return
		}
		if run.Trigger != "manual" && !chanshuiAccountScheduled(a) && !chanshuiAccountRetesting(a, c) {
			s.finish(ctx, run, "error", "账号已停用、暂停调度或过期，本次定时检测尚未提交")
			return
		}
		model := a.GetMappedModel(c.Model)
		protocol := chanshuiProtocol(a, c.Protocol, model)
		base, key, reason := chanshuiAccountEndpoint(a, protocol)
		if reason != "" {
			s.finish(ctx, run, "error", reason)
			return
		}
		if !chanshuiAccountModelSupported(a, c.Model) || chanshuiValidateModel(model, c.Sections) != nil {
			s.finish(ctx, run, "error", "模型配置已变更或不支持检测")
			return
		}
		input := chanshui.CreateRequest{BaseURL: base, APIKey: key, Model: model, Protocol: protocol, Timeout: c.TimeoutSeconds, Sections: c.Sections}
		if chanshui.ValidateCreate(input) != nil {
			s.finish(ctx, run, "error", "账号或检测参数不符合检测接口要求，本次尚未提交")
			return
		}
		expectedSnapshot := chanshuiAccountSnapshot(a)
		if expectedSnapshot == "" {
			s.finish(ctx, run, "error", "无法建立账号配置快照")
			return
		}
		u, _ := url.Parse(base)
		if s.resolvePublicHost(u.Hostname()) != nil {
			s.finish(ctx, run, "error", "上游地址无法解析为公网地址")
			return
		}
		now := time.Now().UTC()
		slot, err := s.repo.AcquireSubmitSlot(ctx, now)
		if err != nil {
			return
		}
		if slot.After(now) {
			_, _ = s.repo.SaveRun(ctx, run, slot, false)
			return
		}
		// DNS 与数据库等待后再次确认，关停或替换凭据不能继续提交排队时的旧请求。
		if !s.IsEnabled(ctx) {
			s.finish(ctx, run, "error", "掺水检测已关闭，本次尚未提交")
			return
		}
		current, err := s.accounts.GetByID(ctx, c.AccountID)
		if err != nil || current == nil || chanshuiAccountSnapshot(current) != expectedSnapshot {
			s.finish(ctx, run, "error", "账号配置已变化，本次尚未提交，请重新检测")
			return
		}
		if _, _, reason = chanshuiAccountEndpoint(current, protocol); reason != "" {
			s.finish(ctx, run, "error", reason)
			return
		}
		// 等待提交槽期间可能有人修改其它保护模型，必须重新核对恢复所有权。
		freshConfig, configErr := s.repo.GetConfig(ctx, c.ID)
		if configErr != nil || !freshConfig.Enabled || !freshConfig.UpdatedAt.Equal(c.UpdatedAt) {
			s.finish(ctx, run, "error", "检测规则已改变，本次尚未提交")
			return
		}
		if run.Trigger != "manual" && !chanshuiAccountScheduled(current) && !chanshuiAccountRetesting(current, freshConfig) {
			s.finish(ctx, run, "error", "账号已停用、暂停调度或过期，本次定时检测尚未提交")
			return
		}
		a = current
		currentRevision, err := s.repo.GetAccountRevision(ctx, c.AccountID)
		if err != nil || currentRevision != revision {
			s.finish(ctx, run, "error", "账号配置已变化，本次尚未提交，请重新检测")
			return
		}
		run.Status = "submitting"
		run.UpdatedAt = now
		run.AccountSnapshot = expectedSnapshot
		run.AccountUpdatedAt = &a.UpdatedAt
		run.AccountRevision = revision
		if run.AccountSnapshot == "" {
			s.finish(ctx, run, "error", "无法建立账号配置快照")
			return
		}
		ok, err := s.repo.SaveRun(ctx, run, now, true)
		if err != nil || !ok {
			return
		}
		result, err := s.client.Create(ctx, input)
		if err != nil {
			var he *chanshui.HTTPError
			if errors.As(err, &he) && he.StatusCode == 429 {
				// 明确拒绝未受理的429才允许排队；网络错误和5xx不能猜测是否已产生用量。
				delay := time.Minute
				if he.RetryAfter > delay {
					delay = he.RetryAfter
				}
				if delay > 24*time.Hour {
					delay = 24 * time.Hour
				}
				run.Status = "queued"
				run.ErrorMessage = "检测服务限流，等待下一次提交时段"
				persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
				defer persistCancel()
				_, _ = s.repo.SaveRun(persistCtx, run, time.Now().Add(delay), false)
				return
			}
			status := "unknown"
			message := "检测提交未能确认受理，未自动重发；请核对上游用量"
			if errors.As(err, &he) && he.StatusCode >= 400 && he.StatusCode < 500 {
				status = "error"
				message = fmt.Sprintf("检测服务拒绝创建（HTTP %d），请检查模型和协议配置", he.StatusCode)
			}
			s.finish(ctx, run, status, message)
			return
		}
		if result == nil || result.ID == "" {
			s.finish(ctx, run, "unknown", "检测服务未返回有效任务编号，未自动重发")
			return
		}
		run.RemoteID = result.ID
		s.applyResult(ctx, run, result)
		return
	}
	if run.RemoteID == "" {
		s.finish(ctx, run, "unknown", "缺少远端任务编号，无法继续查询")
		return
	}
	result, err := s.client.Get(ctx, run.RemoteID)
	if err != nil {
		var he *chanshui.HTTPError
		if errors.As(err, &he) && (he.StatusCode == 404 || he.StatusCode == 410) {
			s.finish(ctx, run, "error", "检测服务中的任务已失效或过期，未重新生成")
			return
		}
		delay := 15 * time.Second
		if errors.As(err, &he) && he.RetryAfter > delay {
			delay = he.RetryAfter
		}
		if delay > time.Hour {
			delay = time.Hour
		}
		_, _ = s.repo.SaveRun(ctx, run, time.Now().Add(delay), false)
		return
	}
	s.applyResult(ctx, run, result)
}

func (s *ChanshuiService) applyResult(ctx context.Context, run *ChanshuiRun, result *chanshui.Result) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if result == nil || result.ID != run.RemoteID {
		s.finish(ctx, run, "error", "检测服务返回的任务身份不一致")
		return
	}
	if result.ExpiresAt > 0 {
		expires := time.Unix(result.ExpiresAt, 0).UTC()
		run.RemoteExpiresAt = &expires
	}
	// 只保存客户端允许的结构，不保存远端地址、ID、响应原文或模型回答中的潜在凭据。
	run.Score = result.Score
	run.ScoreComplete = result.ScoreComplete
	run.Summary = result.Summary
	run.Report, _ = json.Marshal(struct {
		Probes      any `json:"probes"`
		Parts       any `json:"parts"`
		Fingerprint any `json:"fingerprint"`
		Skipped     any `json:"skipped"`
	}{result.Probes, result.Parts, result.Fingerprint, result.Skipped})
	run.UpdatedAt = time.Now().UTC()
	run.ErrorMessage = ""
	run.Status = "running"
	run.Verdict = "pending"
	if !result.Terminal() {
		_, _ = s.repo.SaveRun(ctx, run, time.Now().Add(10*time.Second), false)
		return
	}
	run.Status = "completed"
	run.Verdict = result.Verdict
	run.FinishedAt = &run.UpdatedAt
	run.ActionStatus = "none"
	if !intelligenceChoice(run.Verdict, "clean", "adulterated", "unknown") {
		run.Verdict = "unknown"
	}
	if result.Status != "done" {
		s.finish(ctx, run, "error", "检测服务未完成有效评估，不执行自动禁用")
		return
	}
	c, err := s.repo.GetConfig(ctx, run.ConfigID)
	var decision *ChanshuiDisableDecision
	if err == nil && c.Enabled && s.IsEnabled(ctx) {
		reason := ""
		if c.DisableOnAdulteration && run.Verdict == "adulterated" {
			reason = "掺水检测发现模型身份异常"
		}
		if c.MinimumScore != nil && run.ScoreComplete && run.Score != nil && !math.IsNaN(*run.Score) && *run.Score >= 0 && *run.Score <= 100 && *run.Score < *c.MinimumScore {
			reason = fmt.Sprintf("掺水检测得分 %.2f 低于阈值 %.2f", *run.Score, *c.MinimumScore)
		}
		recover := c.AutoRecover && c.RecoveryPending && c.ScheduleEnabled && run.Verdict == "clean" && run.ScoreComplete && run.Score != nil && !math.IsNaN(*run.Score) && *run.Score >= 0 && *run.Score <= 100 && (c.MinimumScore == nil || *run.Score >= *c.MinimumScore)
		if reason != "" || recover {
			a, e := s.accounts.GetByID(ctx, run.AccountID)
			if e == nil && a != nil && run.AccountSnapshot != "" && chanshuiAccountSnapshot(a) == run.AccountSnapshot && run.AccountUpdatedAt != nil {
				decision = &ChanshuiDisableDecision{AccountUpdatedAt: a.UpdatedAt, Reason: reason, Recover: recover}
			} else {
				run.ActionStatus = "skipped_changed"
				run.ActionReason = "账号配置或状态已经变化，旧检测结果不自动停用账号"
			}
		}
	}
	ok, err := s.repo.FinalizeRun(ctx, run, decision)
	if err == nil && ok && (run.ActionStatus == "disabled" || run.ActionStatus == "recovered") && s.onDisabled != nil {
		s.onDisabled(ctx, run.AccountID)
	}
}

// IsChanshuiEnabled 缺省关闭，只由管理员设置读写，不进入公开配置或用户菜单。
func (s *SettingService) IsChanshuiEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyChanshuiEnabled)
	return err == nil && value == "true"
}
