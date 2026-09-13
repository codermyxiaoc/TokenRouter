//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type modelNotFoundRateLimitCall struct {
	accountID int64
	scope     string
	resetAt   time.Time
	reason    string
}

type modelNotFoundAccountRepoStub struct {
	mockAccountRepoForGemini
	tempCalls           int
	modelRateLimitCalls []modelNotFoundRateLimitCall
	modelRateLimitErr   error
}

func (r *modelNotFoundAccountRepoStub) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.tempCalls++
	return nil
}

func (r *modelNotFoundAccountRepoStub) SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	call := modelNotFoundRateLimitCall{
		accountID: id,
		scope:     scope,
		resetAt:   resetAt,
	}
	if len(reason) > 0 {
		call.reason = reason[0]
	}
	r.modelRateLimitCalls = append(r.modelRateLimitCalls, call)
	return r.modelRateLimitErr
}

func TestRateLimitService_HandleUpstreamError_ModelNotFoundUsesModelRateLimit(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAIModelNotFoundTempAccount()

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"code":"model_not_found","message":"model not found"}}`),
		"gpt-5.4",
	)

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, account.ID, call.accountID)
	require.Equal(t, "gpt-5.4", call.scope)
	require.Equal(t, upstreamModelNotFoundReason, call.reason)
	require.WithinDuration(t, time.Now().Add(upstreamModelNotFoundCooldown), call.resetAt, 5*time.Second)
}

// TestRateLimitService_PoolModeModelNotFoundSkipsDefaultModelPause 验证池模式不会根据
// 聚合上游的一次 404 暂停本地账号与模型组合。
func TestRateLimitService_PoolModeModelNotFoundSkipsDefaultModelPause(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{
		ID:          102,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"pool_mode": true},
	}

	decision := svc.ApplyUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"code":"model_not_found","message":"model not found"}}`),
		"gpt-5.4",
	)

	require.Equal(t, ErrorPolicyPoolBypassed, decision.Policy)
	require.False(t, decision.StopScheduling)
	require.Zero(t, repo.tempCalls)
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestRateLimitService_HandleUpstreamError_ModelNotFoundWriteFailureDoesNotTempUnschedule(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{modelRateLimitErr: errors.New("write failed")}
	svc := &RateLimitService{accountRepo: repo}
	account := openAIModelNotFoundTempAccount()

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"code":"model_not_found","message":"model not found"}}`),
		"gpt-5.4",
	)

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
}

func TestRateLimitService_HandleUpstreamError_Bare404UsesModelScopedTempUnschedulableWhenModelKnown(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAIModelNotFoundTempAccount()

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"message":"endpoint not found"}}`),
		"gpt-5.4",
	)

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, account.ID, call.accountID)
	require.Equal(t, "gpt-5.4", call.scope)
	require.WithinDuration(t, time.Now().Add(10*time.Minute), call.resetAt, 5*time.Second)

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(call.reason), &state))
	require.Equal(t, http.StatusNotFound, state.StatusCode)
	require.Equal(t, "not found", state.MatchedKeyword)
}

func TestRateLimitService_HandleUpstreamError_Bare404WithoutModelKeepsAccountTempUnschedulable(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAIModelNotFoundTempAccount()

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"message":"endpoint not found"}}`),
	)

	require.True(t, handled)
	require.Equal(t, 1, repo.tempCalls)
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestRateLimitService_HandleUpstreamError_ModelTempWriteFailureNeverWidensToAccount(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{modelRateLimitErr: errors.New("write failed")}
	svc := &RateLimitService{accountRepo: repo}
	account := openAIModelNotFoundTempAccount()

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"message":"endpoint not found"}}`),
		"gpt-5.4",
	)

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
}

func TestRateLimitService_HandleTempUnschedulable_PoolModeAppliesExplicitModelRule(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAIModelNotFoundTempAccount()
	account.Credentials["pool_mode"] = true

	handled := svc.HandleTempUnschedulable(
		context.Background(),
		account,
		http.StatusNotFound,
		[]byte(`{"error":{"message":"endpoint not found"}}`),
		"gpt-5.4",
	)

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope)
}

func TestRateLimitService_HandleTempUnschedulable_PoolModeCustomPolicyUsesModelScope(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAIModelNotFoundTempAccount()
	account.Credentials["pool_mode"] = true
	account.Credentials["custom_error_codes_enabled"] = true
	account.Credentials["custom_error_codes"] = []any{float64(http.StatusNotFound)}

	handled := svc.HandleTempUnschedulable(
		context.Background(),
		account,
		http.StatusNotFound,
		[]byte(`{"error":{"message":"endpoint not found"}}`),
		"gpt-5.4",
	)

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope)
}

func TestRateLimitService_TempUnschedulableContextPreservesModelForPoolDependency(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAIModelNotFoundTempAccount()
	account.Credentials["pool_mode"] = true
	ctx := withTempUnschedulableModel(context.Background(), []string{"gpt-5.4"})

	// #4496 的池模式分支调用 tryTempUnschedulable 时不会显式传入模型。
	// 请求上下文必须保留规范模型，确保组合后的行为仍限定在模型范围内。
	handled := svc.tryTempUnschedulable(
		ctx,
		account,
		http.StatusNotFound,
		[]byte(`{"error":{"message":"endpoint not found"}}`),
	)

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope)
}

func TestRateLimitService_HandleUpstreamError_CustomPolicyExclusionSkipsAllState(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAIModelNotFoundTempAccount()
	account.Credentials["custom_error_codes_enabled"] = true
	account.Credentials["custom_error_codes"] = []any{float64(http.StatusServiceUnavailable)}

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"message":"endpoint not found"}}`),
		"gpt-5.4",
	)

	require.False(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestRateLimitService_HandleTempUnschedulable_AuthenticationFailureStaysAccountScoped(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAIModelNotFoundTempAccount()
	account.TempUnschedulableReason = "legacy non-JSON reason"
	account.Credentials["temp_unschedulable_rules"] = []any{
		map[string]any{
			"error_code":       float64(http.StatusUnauthorized),
			"keywords":         []any{"unauthorized"},
			"duration_minutes": float64(10),
		},
	}

	handled := svc.HandleTempUnschedulable(
		context.Background(),
		account,
		http.StatusUnauthorized,
		[]byte(`{"error":{"message":"unauthorized"}}`),
		"gpt-5.4",
	)

	require.True(t, handled)
	require.Equal(t, 1, repo.tempCalls)
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestRateLimitService_ModelTempUnschedulableIsolatesSchedulerByModel(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAIModelNotFoundTempAccount()
	account.Credentials["model_mapping"] = map[string]any{
		"public-a":   "upstream-a",
		"upstream-a": "upstream-b",
	}

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"message":"endpoint not found"}}`),
		"upstream-a",
	)

	require.True(t, handled)
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, "upstream-a", call.scope, "canonical upstream model must not be mapped a second time")

	account.Extra = map[string]any{
		modelRateLimitsKey: map[string]any{
			call.scope: map[string]any{
				"rate_limit_reset_at": call.resetAt.UTC().Format(time.RFC3339),
			},
		},
	}

	require.False(t, account.IsSchedulableForModelWithContext(context.Background(), "public-a"))
	require.True(t, account.IsSchedulableForModelWithContext(context.Background(), "gpt-5.6-sol"))
}

func openAIModelNotFoundTempAccount() *Account {
	return &Account{
		ID:          101,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusNotFound),
					"keywords":         []any{"not found"},
					"duration_minutes": float64(10),
				},
			},
		},
	}
}

func TestRateLimitService_HandleUpstreamError_CodexPlanGatedModelUsesModelRateLimit(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAICodexPlanGatedOAuthAccount()

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusBadRequest,
		http.Header{},
		[]byte(`{"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT account."}`),
		"gpt-5.6-sol",
	)

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, account.ID, call.accountID)
	require.Equal(t, "gpt-5.6-sol", call.scope)
	require.Equal(t, upstreamCodexPlanGatedModelReason, call.reason)
	require.WithinDuration(t, time.Now().Add(upstreamCodexPlanGatedModelCooldown), call.resetAt, 5*time.Second)
}

func TestRateLimitService_HandleUpstreamError_CodexPlanGatedModelUsesFinalUpstreamModel(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAICodexPlanGatedOAuthAccount()
	// 状态入口接收实际发送的最终上游 ID，即使该 ID 也是映射键也不得再次映射。
	account.Credentials["model_mapping"] = map[string]any{
		"gpt-5.6-sol":       "external-model-v1",
		"external-model-v1": "unexpected-remap",
	}

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusBadRequest,
		http.Header{},
		[]byte(`{"detail":"The 'external-model-v1' model is not supported when using Codex with a ChatGPT account."}`),
		"external-model-v1",
	)

	require.True(t, handled)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "external-model-v1", repo.modelRateLimitCalls[0].scope)
}

func TestRateLimitService_HandleUpstreamError_CodexPlanGatedModelIgnoresAPIKeyAccount(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAICodexPlanGatedOAuthAccount()
	account.Type = AccountTypeAPIKey

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusBadRequest,
		http.Header{},
		[]byte(`{"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT account."}`),
		"gpt-5.6-sol",
	)

	require.False(t, handled)
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestRateLimitService_HandleUpstreamError_CodexPlanGatedImageModelSkipsCooldown(t *testing.T) {
	for _, model := range []string{"gpt-image-1", "gpt-image-1.5", "gpt-image-2"} {
		t.Run(model, func(t *testing.T) {
			repo := &modelNotFoundAccountRepoStub{}
			svc := &RateLimitService{accountRepo: repo}
			account := openAICodexPlanGatedOAuthAccount()

			handled := svc.HandleUpstreamError(
				context.Background(),
				account,
				http.StatusBadRequest,
				http.Header{},
				[]byte(`{"detail":"The '`+model+`' model is not supported when using Codex with a ChatGPT account."}`),
				model,
			)

			require.True(t, handled, "当前文本端点尝试仍应切换账号")
			require.Empty(t, repo.modelRateLimitCalls, "文本端点错配不应冷却账号的图片模型")
			require.Zero(t, repo.tempCalls)
		})
	}
}

func TestRateLimitService_HandleUpstreamError_CodexPlanGatedTextModelStillCoolsDown(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAICodexPlanGatedOAuthAccount()

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusBadRequest,
		http.Header{},
		[]byte(`{"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT account."}`),
		"gpt-5.6-sol",
	)

	require.True(t, handled)
	require.Len(t, repo.modelRateLimitCalls, 1, "非图片套餐门控模型应保留原有冷却")
	require.Equal(t, upstreamCodexPlanGatedModelReason, repo.modelRateLimitCalls[0].reason)
}

func TestRateLimitService_HandleUpstreamError_CodexPlanGatedImageModelKeepsCooldownOnImagesEndpoint(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAICodexPlanGatedOAuthAccount()

	handled := svc.HandleUpstreamError(
		WithOpenAIImagesEndpoint(context.Background()),
		account,
		http.StatusBadRequest,
		http.Header{},
		[]byte(`{"detail":"The 'gpt-image-2' model is not supported when using Codex with a ChatGPT account."}`),
		"gpt-image-2",
	)

	require.True(t, handled)
	require.Len(t, repo.modelRateLimitCalls, 1, "专用 Images 端点上的能力拒绝必须保留冷却")
	require.Equal(t, "gpt-image-2", repo.modelRateLimitCalls[0].scope)
	require.Equal(t, upstreamCodexPlanGatedModelReason, repo.modelRateLimitCalls[0].reason)
}

func TestRateLimitService_HandleUpstreamError_CodexPlanGatedImageModelSkipsCooldownOnIntentOnly(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAICodexPlanGatedOAuthAccount()

	handled := svc.HandleUpstreamError(
		WithOpenAIImageGenerationIntent(context.Background()),
		account,
		http.StatusBadRequest,
		http.Header{},
		[]byte(`{"detail":"The 'gpt-image-2' model is not supported when using Codex with a ChatGPT account."}`),
		"gpt-image-2",
	)

	require.True(t, handled)
	require.Empty(t, repo.modelRateLimitCalls, "生图意图不等于专用 Images 入口")
}

func TestRateLimitService_HandleUpstreamError_ModelNotFoundImageModelStillCoolsDown(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := openAICodexPlanGatedOAuthAccount()

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"message":"The model 'gpt-image-2' does not exist","code":"model_not_found"}}`),
		"gpt-image-2",
	)

	require.True(t, handled)
	require.Len(t, repo.modelRateLimitCalls, 1, "图片模型的 404 model_not_found 仍应冷却")
	require.Equal(t, upstreamModelNotFoundReason, repo.modelRateLimitCalls[0].reason)
}

func openAICodexPlanGatedOAuthAccount() *Account {
	return &Account{
		ID:          202,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{},
	}
}
