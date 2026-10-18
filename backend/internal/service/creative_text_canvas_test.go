//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

type creativeTextStateFake struct {
	CreativeCanvasRepository
	owner            int64
	id               string
	state            CreativeTextExecutionState
	persistErr       error
	starts, persists int
}

func (r *creativeTextStateFake) BeginTextExecution(_ context.Context, e *CreativeCanvasExecution, _ string) (bool, error) {
	if e.UserID != r.owner || e.ID != r.id {
		return false, ErrCanvasNotFound
	}
	if r.state.Phase != "" {
		return false, nil
	}
	r.state.Phase = "provider_inflight"
	r.starts++
	return true, nil
}
func (r *creativeTextStateFake) GetTextExecutionState(_ context.Context, u int64, id string) (*CreativeTextExecutionState, error) {
	if u != r.owner || id != r.id {
		return nil, ErrCanvasNotFound
	}
	state := r.state
	return &state, nil
}
func (r *creativeTextStateFake) SaveTextResponse(_ context.Context, u int64, id string, status int, text string) error {
	if u != r.owner || id != r.id {
		return ErrCanvasNotFound
	}
	r.state.HTTPStatus = status
	r.state.OutputText = text
	return nil
}
func (r *creativeTextStateFake) SaveTextBillingEvidence(_ context.Context, u int64, id string, evidence *CreativeTextBillingEvidence, status int, text string) error {
	if r.persistErr != nil {
		return r.persistErr
	}
	if u != r.owner || id != r.id {
		return ErrCanvasNotFound
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	var copy CreativeTextBillingEvidence
	if err = json.Unmarshal(raw, &copy); err != nil {
		return err
	}
	r.state.Evidence = &copy
	r.state.HTTPStatus = status
	r.state.OutputText = text
	r.state.Phase = "settlement_pending"
	r.persists++
	return nil
}

type creativeTextBillingFake struct {
	UsageBillingRepository
	state             *creativeTextStateFake
	applied, attempts int
	fail              bool
}

func (b *creativeTextBillingFake) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	b.attempts++
	if b.fail {
		return nil, errors.New("temporary billing failure")
	}
	if b.state.state.Evidence == nil {
		return nil, errors.New("missing durable evidence")
	}
	if b.state.state.Receipt != nil {
		copy := *b.state.state.Receipt
		copy.Applied = false
		return &copy, nil
	}
	b.applied++
	result := &UsageBillingApplyResult{Applied: true, BalanceAmountUSD: cmd.BillableAmountUSD}
	b.state.state.Receipt = result
	return result, nil
}

type creativeTextLogFake struct {
	UsageLogRepository
	logs []*UsageLog
	fail bool
}

func (r *creativeTextLogFake) Create(_ context.Context, log *UsageLog) (bool, error) {
	if r.fail {
		return false, errors.New("log store unavailable")
	}
	for _, old := range r.logs {
		if old.RequestID == log.RequestID && old.APIKeyID == log.APIKeyID {
			return false, nil
		}
	}
	copy := *log
	r.logs = append(r.logs, &copy)
	return true, nil
}

func creativeTextBillingFixture(t *testing.T) (context.Context, *creativeTextStateFake, *creativeTextBillingFake, *creativeTextLogFake, *OpenAIGatewayService, *OpenAIRecordUsageInput, *CreativeCanvasExecution) {
	t.Helper()
	e := &CreativeCanvasExecution{ID: "41ab502b-2f80-4445-931d-9cbf0aa5e2c6", UserID: 7, CanvasID: testCreativeWorkspaceID, Kind: "text", Params: CreativeCanvasExecutionParams{GroupID: 12, Model: "grok-4.6", Prompt: "你好", MaxOutputTokens: 1024}}
	repo := &creativeTextStateFake{owner: 7, id: e.ID, state: CreativeTextExecutionState{Phase: "provider_inflight"}}
	sink := &creativeTextUsageCapture{execution: e, repo: repo}
	ctx := context.WithValue(context.Background(), creativeTextUsageContextKey{}, sink)
	BindCreativeTextResponseSource(ctx, func() (int, []byte) { return 200, []byte(`{"choices":[{"message":{"content":"你好，画布！"}}]}`) })
	billing := &creativeTextBillingFake{state: repo}
	logs := &creativeTextLogFake{}
	group := &Group{ID: 12, Platform: PlatformGrok, RateMultiplier: 1}
	user := &User{ID: 7, Status: StatusActive, Balance: 100}
	key := &APIKey{ID: 9, UserID: 7, User: user, Group: group, GroupID: &group.ID, Status: StatusAPIKeyActive}
	pricing := NewBillingService(nil, nil)
	gateway := &OpenAIGatewayService{billingService: pricing, resolver: NewModelPricingResolver(nil, pricing), usageBillingRepo: billing, usageLogRepo: logs, deferredService: &DeferredService{}, cfg: &config.Config{Default: config.DefaultConfig{RateMultiplier: 1}}}
	in := &OpenAIRecordUsageInput{Result: &OpenAIForwardResult{RequestID: "supplier-controlled", Model: "grok-4.6", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 50}}, APIKey: key, User: user, Account: &Account{ID: 55, Platform: PlatformGrok, Type: AccountTypeAPIKey}, RequestPayloadHash: "hash-only", UserAgent: "do-not-store", IPAddress: "127.0.0.1", ClientSessionID: "private-session"}
	return ctx, repo, billing, logs, gateway, in, e
}

func TestCreativeTextNativeUsagePersistsBeforeBillingAndRecoversLog(t *testing.T) {
	ctx, repo, billing, logs, gateway, in, e := creativeTextBillingFixture(t)
	logs.fail = true
	require.NoError(t, gateway.RecordUsage(ctx, in))
	require.Equal(t, 1, billing.applied)
	require.Equal(t, 1, repo.persists)
	evidence := repo.state.Evidence
	require.Equal(t, "creative_text:"+e.ID, evidence.Command.RequestID)
	require.Equal(t, e.ID, evidence.Command.CreativeTextExecutionID)
	require.Equal(t, "text", evidence.Command.MediaType)
	require.Equal(t, "你好，画布！", repo.state.OutputText)
	require.Nil(t, evidence.Usage.User)
	require.Nil(t, evidence.Usage.APIKey)
	require.Nil(t, evidence.Usage.Account)
	require.Nil(t, evidence.Usage.UserAgent)
	require.Nil(t, evidence.Usage.IPAddress)
	require.Nil(t, evidence.Usage.SessionID)
	logs.fail = false
	pub := newCreativeTestService()
	pub.BillingRepo = billing
	pub.UsageLogRepo = logs
	x := &creativeTextCanvasExecutor{public: pub, repo: repo}
	for range 2 {
		result, err := x.Poll(context.Background(), e)
		require.NoError(t, err)
		require.Equal(t, "succeeded", result.Status)
		require.Equal(t, "你好，画布！", result.OutputText)
	}
	require.Equal(t, 1, billing.applied)
	require.Len(t, logs.logs, 1)
	require.Equal(t, repo.state.Receipt.BalanceAmountUSD, logs.logs[0].ActualCost)
	// 只持久回执和原冻结命令参与恢复，当前模型价格没有二次解析机会。
	in.APIKey.Group.RateMultiplier = 999
	_, err := x.Poll(context.Background(), e)
	require.NoError(t, err)
	require.Equal(t, 1, billing.applied)
}

func TestCreativeTextEvidenceFailureNeverDebitsOrWritesFalseUsage(t *testing.T) {
	ctx, repo, billing, logs, gateway, in, _ := creativeTextBillingFixture(t)
	repo.persistErr = errors.New("database unavailable")
	require.Error(t, gateway.RecordUsage(ctx, in))
	require.Zero(t, billing.attempts)
	require.Empty(t, logs.logs)
}

func TestCreativeTextBillingFailureRestoresFrozenCommandOnly(t *testing.T) {
	ctx, repo, billing, logs, gateway, in, e := creativeTextBillingFixture(t)
	billing.fail = true
	require.Error(t, gateway.RecordUsage(ctx, in))
	require.NotNil(t, repo.state.Evidence)
	require.Empty(t, logs.logs)
	pub := newCreativeTestService()
	pub.BillingRepo = billing
	pub.UsageLogRepo = logs
	x := &creativeTextCanvasExecutor{public: pub, repo: repo}
	result, err := x.Poll(context.Background(), e)
	require.NoError(t, err)
	require.Equal(t, "CREATIVE_TEXT_SETTLEMENT_PENDING", result.ErrorCode)
	billing.fail = false
	result, err = x.Poll(context.Background(), e)
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.Status)
	require.Equal(t, 1, billing.applied)
}

func TestCreativeTextMissingPricingDoesNotBecomeFree(t *testing.T) {
	ctx, repo, billing, logs, gateway, in, _ := creativeTextBillingFixture(t)
	in.Result.Model = "model-with-no-known-price"
	require.Error(t, gateway.RecordUsage(ctx, in))
	require.Nil(t, repo.state.Evidence)
	require.Zero(t, billing.attempts)
	require.Empty(t, logs.logs)
}

func TestCreativeTextRecoveryNeverResubmitsAndKeepsOwnerIsolation(t *testing.T) {
	_, repo, _, _, _, _, e := creativeTextBillingFixture(t)
	x := &creativeTextCanvasExecutor{repo: repo, public: newCreativeTestService()}
	id, err := x.Recover(context.Background(), e)
	require.NoError(t, err)
	require.Equal(t, "creative_text:"+e.ID, id)
	for range 2 {
		result, err := x.Poll(context.Background(), e)
		require.NoError(t, err)
		require.Equal(t, "running", result.Status)
		require.Equal(t, "CREATIVE_TEXT_SUBMISSION_UNCONFIRMED", result.ErrorCode)
	}
	require.Zero(t, repo.starts)
	foreign := *e
	foreign.UserID = 8
	_, err = x.Recover(context.Background(), &foreign)
	require.ErrorIs(t, err, ErrCanvasNotFound)
	_, err = x.Poll(context.Background(), &foreign)
	require.ErrorIs(t, err, ErrCanvasNotFound)
}

func TestCreativeTextSingleSendIncludesAllNativeTransportsAndProxyPrefixes(t *testing.T) {
	paths := []string{"/oauth-proxy/v1/chat/completions", "/proxy/responses", "/v1/messages", "/model/claude/invoke", "/model/claude/invoke-with-response-stream", "/v1/projects/p/models/claude:rawPredict", "/v1/models/m:streamRawPredict", "/v1/models/gemini:generateContent", "/v1internal:streamGenerateContent", "/algo/api/v2/service/pro/sse/agent_chat_generation"}
	for _, path := range paths {
		ctx, _, _, _, _, _, _ := creativeTextBillingFixture(t)
		for _, metadata := range []string{"/v1/responses/input_tokens", "/oauth/token", "/algo/api/v3/user/jobToken", "/v1internal:loadCodeAssist", "/v1/models/model:countTokens"} {
			req, _ := http.NewRequestWithContext(ctx, "POST", "https://example.test"+metadata, nil)
			require.NoError(t, ClaimCreativeTextUpstreamRequest(req))
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://example.test"+path, nil)
		require.NoError(t, ClaimCreativeTextUpstreamRequest(req), path)
		require.Error(t, ClaimCreativeTextUpstreamRequest(req), path)
		ordinary := req.WithContext(context.Background())
		require.NoError(t, ClaimCreativeTextUpstreamRequest(ordinary))
		require.NoError(t, ClaimCreativeTextUpstreamRequest(ordinary))
	}
}

func TestCreativeTextParametersAndPlainOutput(t *testing.T) {
	p := CreativeCanvasExecutionParams{GroupID: 12, Model: "grok-4.6", Prompt: " 文本 "}
	require.NoError(t, normalizeCreativeTextParams(&p))
	require.Equal(t, 1024, p.MaxOutputTokens)
	for _, mutate := range []func(*CreativeCanvasExecutionParams){func(p *CreativeCanvasExecutionParams) { p.MaxOutputTokens = 4097 }, func(p *CreativeCanvasExecutionParams) { p.MaxOutputTokens = -1 }, func(p *CreativeCanvasExecutionParams) { p.Prompt = strings.Repeat("文", 8001) }, func(p *CreativeCanvasExecutionParams) { p.References = []CreativeCanvasReference{{AssetID: "private"}} }, func(p *CreativeCanvasExecutionParams) { p.Voice = "eve" }, func(p *CreativeCanvasExecutionParams) { p.Duration = 5 }} {
		copy := p
		mutate(&copy)
		require.Error(t, normalizeCreativeTextParams(&copy))
	}
	for _, tc := range []struct{ body, text string }{
		{`{"choices":[{"message":{"content":"<script>text only</script>"}}]}`, "<script>text only</script>"},
		{`{"choices":[{"message":{"content":null,"refusal":"不能提供该内容"}}]}`, "不能提供该内容"},
		{`{"choices":[{"message":{"content":[],"refusal":"拒绝"}}]}`, "拒绝"},
		{`{"choices":[{"message":{"content":[{"type":"text","text":"one"},{"type":"image_url","text":"ignored"},{"type":"text","text":"two"}]}}]}`, "onetwo"},
		{`{"choices":[]}`, ""},
	} {
		require.Equal(t, tc.text, creativeTextResponseContent([]byte(tc.body)))
	}
}
