package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// SetTextGateway 由网关路由装配阶段调用；恢复器可先恢复已存在的账务证据。
func (s *CreativeCanvasService) SetTextGateway(gateway CreativeTextGateway) {
	repo, _ := s.repo.(CreativeTextRepository)
	s.SetExecutor("text", &creativeTextCanvasExecutor{public: s.creative, repo: repo, gateway: gateway})
}

type creativeTextCanvasExecutor struct {
	public  *CreativePublicService
	repo    CreativeTextRepository
	gateway CreativeTextGateway
}

func normalizeCreativeTextParams(p *CreativeCanvasExecutionParams) error {
	if p == nil || p.GroupID <= 0 || strings.TrimSpace(p.Model) == "" || p.Operation != "" && p.Operation != CreativeOperationGenerate {
		return ErrCanvasInvalid
	}
	if len(p.References) != 0 || p.MaskAssetID != "" || p.ImageSize != "" || p.AspectRatio != "" || p.Quality != "" || p.Background != "" || p.ThinkingLevel != "" || p.Endpoint != "" || p.Duration != 0 || p.Resolution != "" || p.GenerateAudio != nil || p.Watermark != nil || p.Voice != "" || p.AudioFormat != "" || p.Speed != 0 {
		return ErrCanvasInvalid
	}
	p.Prompt = strings.TrimSpace(p.Prompt)
	p.Model = strings.TrimSpace(p.Model)
	p.Operation = CreativeOperationGenerate
	if !utf8.ValidString(p.Prompt) || p.Prompt == "" || utf8.RuneCountInString(p.Prompt) > 8000 {
		return ErrCreativeInvalidParams
	}
	if p.MaxOutputTokens == 0 {
		p.MaxOutputTokens = 1024
	}
	if p.MaxOutputTokens < 1 || p.MaxOutputTokens > 4096 {
		return ErrCreativeInvalidParams
	}
	return nil
}

func (x *creativeTextCanvasExecutor) Recover(ctx context.Context, e *CreativeCanvasExecution) (string, error) {
	if x == nil || x.repo == nil {
		return "", ErrCanvasUnavailable
	}
	state, err := x.repo.GetTextExecutionState(ctx, e.UserID, e.ID)
	if err != nil {
		return "", err
	}
	if state.Phase != "" {
		return "creative_text:" + e.ID, nil
	}
	return "", nil
}

func (x *creativeTextCanvasExecutor) Submit(ctx context.Context, e *CreativeCanvasExecution, assets []CreativeCanvasInputAsset) (string, error) {
	if x == nil || x.public == nil || x.repo == nil || x.gateway == nil {
		return "", ErrCanvasUnavailable
	}
	if id, err := x.Recover(ctx, e); id != "" || err != nil {
		return id, err
	}
	if e.Kind != "text" || len(assets) != 0 {
		return "", ErrCanvasInvalid
	}
	p := e.Params
	if err := normalizeCreativeTextParams(&p); err != nil {
		return "", err
	}
	if !x.public.enabled(ctx) {
		return "", ErrCreativeDisabled
	}
	user, err := x.public.UserRepo.GetByID(ctx, e.UserID)
	if err != nil || user == nil || user.ID != e.UserID || !user.IsActive() {
		return "", ErrUserNotFound
	}
	group, err := x.public.GroupRepo.GetByIDLite(ctx, p.GroupID)
	if err != nil || group == nil || !group.IsActive() || !user.CanBindGroup(group.ID, group.IsExclusive) {
		return "", ErrCreativeGroupForbidden
	}
	models, err := x.public.creativeTextModelsForGroup(ctx, group, x.public.creativeModelSettings(ctx))
	if err != nil {
		return "", err
	}
	allowed := false
	for _, model := range models {
		if model.Model == p.Model {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", ErrCreativeInvalidModel
	}
	key, err := x.public.ensureCreativeManagedKey(ctx, e.UserID, p.GroupID)
	if err != nil {
		return "", err
	}
	if !creativeVideoKeyMatches(key, e.UserID, p.GroupID) || key.Key == "" {
		return "", ErrCreativeGroupForbidden
	}
	keyCopy := *key
	keyCopy.User = user
	keyCopy.Group = group
	key = &keyCopy
	body, err := json.Marshal(map[string]any{"model": p.Model, "messages": []map[string]string{{"role": "user", "content": p.Prompt}}, "stream": false, "max_tokens": p.MaxOutputTokens})
	if err != nil {
		return "", err
	}
	id := "creative_text:" + e.ID
	started, err := x.repo.BeginTextExecution(ctx, e, id)
	if err != nil {
		return "", err
	}
	if !started {
		return id, nil
	}
	// 发送前已经持久化身份，任何异常恢复都只查询此执行，不重新请求模型。
	sink := &creativeTextUsageCapture{execution: e, repo: x.repo}
	work := context.WithValue(ctx, creativeTextUsageContextKey{}, sink)
	work = WithHTTPUpstreamRedirectsDisabled(work)
	response, invokeErr := x.gateway.Generate(work, key, body)
	status := response.StatusCode
	if status == 0 {
		status = 502
	}
	// 未发送任何生成请求且本地准入失败，可以安全结束，不必无限等待受理核对。
	if status >= 500 && sink.requests.Load() == 0 {
		status = 400
	}
	text := creativeTextResponseContent(response.Body)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := x.repo.SaveTextResponse(cleanup, e.UserID, e.ID, status, text); err != nil {
		return id, nil
	}
	_ = invokeErr // 受理状态以持久证据和完整响应判断，不能因错误自动重新生成。
	return id, nil
}

func (x *creativeTextCanvasExecutor) Poll(ctx context.Context, e *CreativeCanvasExecution) (*CreativeCanvasExecutionResult, error) {
	if x == nil || x.repo == nil || x.public == nil {
		return nil, ErrCanvasUnavailable
	}
	state, err := x.repo.GetTextExecutionState(ctx, e.UserID, e.ID)
	if err != nil {
		return nil, err
	}
	pending := &CreativeCanvasExecutionResult{Status: "running", ErrorCode: "CREATIVE_TEXT_SUBMISSION_UNCONFIRMED", ErrorMessage: "文本请求结果正在核对，请勿重复生成"}
	if state.Evidence == nil {
		if state.HTTPStatus >= 400 && state.HTTPStatus < 500 {
			return &CreativeCanvasExecutionResult{Status: "failed", ErrorCode: "CREATIVE_TEXT_REQUEST_FAILED", ErrorMessage: "文本请求未能完成，请检查模型权限、参数或服务状态"}, nil
		}
		return pending, nil
	}
	pending.ErrorCode, pending.ErrorMessage = "CREATIVE_TEXT_SETTLEMENT_PENDING", "文本已返回，正在确认用量结算，请勿重复生成"
	evidence := state.Evidence
	if evidence.Version != 1 || evidence.Command.CreativeTextExecutionID != e.ID || evidence.Command.UserID != e.UserID || evidence.Usage.UserID != e.UserID {
		return nil, ErrCanvasConflict
	}
	if x.public.BillingRepo == nil || x.public.UsageLogRepo == nil {
		return pending, nil
	}
	command := evidence.Command
	result, err := x.public.BillingRepo.Apply(ctx, &command)
	if err != nil || result == nil {
		return pending, nil
	}
	log := evidence.Usage
	applyUsageBillingResultToUsageLog(&log, result)
	log.ActualCost = result.BalanceAmountUSD + result.SubscriptionAmountUSD
	if log.TotalCost > 0 {
		log.RateMultiplier = log.ActualCost / log.TotalCost
	}
	if _, err = x.public.UsageLogRepo.Create(ctx, &log); err != nil {
		return pending, nil
	}
	x.public.invalidateCreativeAuthCache(ctx, e.UserID)
	if state.HTTPStatus < 200 || state.HTTPStatus >= 300 || strings.TrimSpace(state.OutputText) == "" {
		return &CreativeCanvasExecutionResult{Status: "failed", ErrorCode: "CREATIVE_TEXT_RESULT_UNAVAILABLE", ErrorMessage: "文本结果不完整；已产生的用量按原计费记录结算"}, nil
	}
	return &CreativeCanvasExecutionResult{Status: "succeeded", OutputText: state.OutputText}, nil
}

var _ CreativeCanvasExecutorRecoverer = (*creativeTextCanvasExecutor)(nil)
