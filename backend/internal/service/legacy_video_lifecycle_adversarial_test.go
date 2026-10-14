//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 错误响应还会更新账号观测；本测试仅验证任务恢复，不依赖额外的账号持久桩。
type legacyAdversarialAccountRepo struct {
	*openAIRecordUsageAccountRepoStub
}

func (*legacyAdversarialAccountRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}
func (*legacyAdversarialAccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}
func (*legacyAdversarialAccountRepo) SetRateLimited(context.Context, int64, time.Time) error {
	return nil
}

// 报价落盘后进程重启、Key 改绑分组和套餐、账号改倍率，都不得重算已冻结命令。
func TestLegacyVideoRecoveryFrozenQuoteSurvivesConfigurationChanges(t *testing.T) {
	r, task, logs, billing, calls := legacyRecoveryFixture(t, `{"status":"done","video":{"url":"https://example.com/v.mp4","duration":8}}`)
	originalSub := int64(55)
	task.Pending.SeedanceBilling.BillingMode = APIKeyBillingModeAuto
	task.Pending.SeedanceBilling.Subscription = &UserSubscription{ID: originalSub, UserID: task.BillingUserID,
		Status: SubscriptionStatusActive, StartsAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour)}
	billing.err = errors.New("billing temporarily unavailable")
	require.Error(t, r.RecoverLegacyVideoTask(context.Background(), task))
	require.Equal(t, 1, billing.calls)
	require.Zero(t, logs.calls)
	wantCommand := *billing.lastCmd
	require.Equal(t, &originalSub, wantCommand.SubscriptionScopeID)
	var restored LegacyVideoTask
	raw, err := json.Marshal(task)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &restored))
	newGroup, newSub, newTeam := int64(300), int64(550), int64(42)
	key := r.keys.(*legacyRecoveryKeys).key
	key.GroupID, key.Group = &newGroup, &Group{ID: newGroup, Platform: PlatformGrok, RateMultiplier: 99}
	key.BillingMode, key.PreferredSubscriptionID, key.TeamID = APIKeyBillingModeSubscription, &newSub, &newTeam
	newAccountRate := 99.0
	r.gateway.accountRepo.(*openAIRecordUsageAccountRepoStub).account.RateMultiplier = &newAccountRate
	r.gateway.cfg.Default.RateMultiplier = 99
	billing.err = nil
	// 原账本仍可返回不同来源的真实金额，日志必须完整重放这份回执。
	billing.result = &UsageBillingApplyResult{Applied: false, SubscriptionAmountUSD: 0.72, BalanceAmountUSD: 0.12}
	require.NoError(t, r.RecoverLegacyVideoTask(context.Background(), &restored))
	require.Equal(t, wantCommand, *billing.lastCmd)
	require.Equal(t, &originalSub, billing.lastCmd.SubscriptionScopeID)
	require.Equal(t, 1, *calls, "重启恢复不能再次发起生成或查询已存终态")
	require.Equal(t, "settled", restored.BillingStatus)
	require.Equal(t, task.GroupID, *logs.lastLog.GroupID)
	require.Nil(t, logs.lastLog.TeamID, "改绑团队不能改变原付款归属")
	require.InDelta(t, 0.84, logs.lastLog.ActualCost, 1e-10)
	require.InDelta(t, 0.72, logs.lastLog.SubscriptionAmountUSD, 1e-10)
	require.InDelta(t, 0.12, logs.lastLog.BalanceAmountUSD, 1e-10)
}

// 身份和持久证据异常必须拒绝结算，不能因为上游已经返回成果就绕过归属检查。
func TestLegacyVideoRecoveryRejectsOwnershipAndCorruptEvidence(t *testing.T) {
	for _, scenario := range []string{"key_owner", "snapshot_group", "subscription_payer", "corrupt_evidence"} {
		t.Run(scenario, func(t *testing.T) {
			r, task, logs, billing, calls := legacyRecoveryFixture(t, `{"status":"done","video":{"url":"https://example.com/v.mp4","duration":8}}`)
			switch scenario {
			case "key_owner":
				r.keys.(*legacyRecoveryKeys).key.UserID++
			case "snapshot_group":
				task.Pending.SeedanceBilling.Group.ID++
			case "subscription_payer":
				task.Pending.SeedanceBilling.Subscription = &UserSubscription{ID: 55, UserID: task.BillingUserID + 1}
			case "corrupt_evidence":
				task.SettlementResult = json.RawMessage(`{"Observation":`)
			}
			require.Error(t, r.RecoverLegacyVideoTask(context.Background(), task))
			require.Zero(t, billing.calls)
			require.Zero(t, logs.calls)
			require.NotEqual(t, "settled", task.BillingStatus)
			if scenario == "key_owner" || scenario == "corrupt_evidence" {
				require.Zero(t, *calls, "已知归属或证据错误应在调用上游前拒绝")
			}
		})
	}
}

// 查询错误与不可识别状态不等于生成失败，不得清零账务、猜测免费或提交第二个视频。
func TestLegacyVideoRecoveryUnknownUpstreamKeepsTaskRecoverable(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"not_found", 404, `{"error":"task unavailable"}`},
		{"rate_limited", 429, `{"error":"busy"}`},
		{"server_error", 503, `{"error":"https://upstream.test/?secret=never-store"}`},
		{"malformed_json", 200, `{"status":`},
		{"unknown_status", 200, `{"status":"future-provider-state"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, task, logs, billing, _ := legacyRecoveryFixture(t, "")
			r.gateway.accountRepo = &legacyAdversarialAccountRepo{r.gateway.accountRepo.(*openAIRecordUsageAccountRepoStub)}
			task.Status = "processing"
			calls := 0
			r.gateway.httpUpstream = &legacyVideoTransportStub{do: func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, http.MethodGet, req.Method)
				return &http.Response{StatusCode: tc.code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}}
			err := r.RecoverLegacyVideoTask(context.Background(), task)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "never-store")
			require.Equal(t, 1, calls)
			require.Equal(t, "processing", task.Status)
			require.Equal(t, "pending", task.BillingStatus)
			require.Empty(t, task.SettlementResult)
			require.Zero(t, billing.calls)
			require.Zero(t, logs.calls)
		})
	}
}

// 完成但缺 Token 的任务补查遇到迟到处理中状态时，应保留完成证据并等待真实用量。
func TestLegacyVideoRecoverySeedanceUsageRefreshCannotRegressCompletion(t *testing.T) {
	r, task, logs, billing, _ := legacyRecoveryFixture(t, "")
	account := seedanceTestAccount()
	r.gateway.accountRepo = &openAIRecordUsageAccountRepoStub{account: account}
	task.Source, task.AccountID = "seedance_video", account.ID
	task.Pending.Model, task.Pending.BillingModel, task.Pending.UpstreamModel = "seedance-test", "seedance-test", "seedance-test"
	price := 10.0
	group := task.Pending.SeedanceBilling.Group
	group.Platform = PlatformOpenAI
	group.ModelPricing = []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"seedance-test"}, BillingMode: BillingModeToken, OutputPrice: &price}}
	r.gateway.resolver = NewModelPricingResolver(nil, r.gateway.billingService)
	responses := []string{
		`{"id":"task-1","status":"succeeded","usage":{"completion_tokens":0}}`,
		`{"id":"task-1","status":"running"}`,
		`{"id":"task-1","status":"succeeded","usage":{"completion_tokens":1000}}`,
	}
	calls := 0
	r.gateway.httpUpstream = &legacyVideoTransportStub{do: func(req *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodGet, req.Method)
		require.Less(t, calls, len(responses))
		body := responses[calls]
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	require.ErrorContains(t, r.RecoverLegacyVideoTask(context.Background(), task), "真实 Token")
	firstEvidence := string(task.SettlementResult)
	require.ErrorContains(t, r.RecoverLegacyVideoTask(context.Background(), task), "用量尚未可用")
	require.Equal(t, "completed", task.Status)
	require.Equal(t, firstEvidence, string(task.SettlementResult))
	require.Zero(t, billing.calls)
	require.NoError(t, r.RecoverLegacyVideoTask(context.Background(), task))
	require.Equal(t, 3, calls)
	require.Equal(t, 1, billing.calls)
	require.Equal(t, 1000, logs.lastLog.OutputTokens)
	require.Equal(t, "settled", task.BillingStatus)
}

// 上游返回 done 但媒体地址类型异常不能当成果；状态与计费必须使用同一个严格判定。
func TestLegacyVideoRecoveryRejectsMalformedVideoURL(t *testing.T) {
	for _, tc := range []struct {
		name     string
		url      string
		billable bool
	}{
		{"object", `{}`, false},
		{"array", `["https://example.com/v.mp4"]`, false},
		{"null", `null`, false},
		{"boolean", `true`, false},
		{"number", `123`, false},
		{"whitespace", `"  \t "`, false},
		{"empty", `""`, false},
		{"valid", `"https://example.com/v.mp4"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"status":"done","video":{"url":` + tc.url + `,"duration":8}}`
			require.Equal(t, tc.billable, IsGrokVideoStatusBillable([]byte(body)))
			r, task, logs, billing, calls := legacyRecoveryFixture(t, body)
			require.NoError(t, r.RecoverLegacyVideoTask(context.Background(), task))
			require.Equal(t, 1, *calls)
			if tc.billable {
				require.Equal(t, "completed", task.Status)
				require.Equal(t, "settled", task.BillingStatus)
				require.Equal(t, 1, billing.calls)
				require.Equal(t, 1, logs.calls)
			} else {
				require.Equal(t, "processing", task.Status)
				require.Equal(t, "pending", task.BillingStatus)
				require.Zero(t, billing.calls)
				require.Zero(t, logs.calls)
			}
		})
	}
}
