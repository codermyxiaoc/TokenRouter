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

type legacyRecoveryRepoStub struct {
	LegacyVideoTaskRepository
	checkpoints   int
	checkpointErr error
}

func (r *legacyRecoveryRepoStub) Checkpoint(_ context.Context, _ *LegacyVideoTask) error {
	r.checkpoints++
	return r.checkpointErr
}
func (*legacyRecoveryRepoStub) HasBilling(context.Context, string, int64) (bool, error) {
	return false, nil
}
func (*legacyRecoveryRepoStub) HasUsage(context.Context, string, int64) (bool, error) {
	return false, nil
}
func (*legacyRecoveryRepoStub) Get(context.Context, string, string, int64, int64) (*LegacyVideoTask, error) {
	return nil, ErrLegacyVideoTaskNotFound
}

type legacyRecoveryKeys struct {
	APIKeyRepository
	key *APIKey
}

func (s *legacyRecoveryKeys) GetByID(context.Context, int64) (*APIKey, error) { return s.key, nil }

type legacyRecoveryUsers struct{ UserRepository }

func (*legacyRecoveryUsers) GetByID(context.Context, int64) (*User, error) { return &User{ID: 1}, nil }

type legacyRecoveryObserver struct {
	calls  int
	status string
	err    error
}

func (s *legacyRecoveryObserver) ObserveMediaTask(_ context.Context, o MediaTaskObservation) error {
	s.calls++
	s.status = o.Status
	return s.err
}

// 恢复只允许 GET 原任务，扣费前必须先落终态证据与冻结报价。
func legacyRecoveryFixture(t *testing.T, body string) (*LegacyVideoRecovery, *LegacyVideoTask, *openAIRecordUsageLogRepoStub, *openAIRecordUsageBillingRepoStub, *int) {
	t.Helper()
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	gateway := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	account := grokMediaContentTestAccount()
	gateway.accountRepo = &openAIRecordUsageAccountRepoStub{account: account}
	gateway.userRepo = &legacyRecoveryUsers{}
	group := &Group{ID: 3, Platform: PlatformGrok, RateMultiplier: 1}
	key := &APIKey{ID: 2, UserID: 1, GroupID: &group.ID, Group: group, BillingMode: APIKeyBillingModeBalance}
	calls := new(int)
	gateway.httpUpstream = &legacyVideoTransportStub{do: func(req *http.Request) (*http.Response, error) {
		*calls++
		require.Equal(t, http.MethodGet, req.Method)
		require.True(t, strings.HasSuffix(req.URL.Path, "/videos/task-1"))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	repo := &legacyRecoveryRepoStub{}
	r := &LegacyVideoRecovery{repo: repo, gateway: gateway, keys: &legacyRecoveryKeys{key: key}, observer: &legacyRecoveryObserver{}}
	task := &LegacyVideoTask{ID: 5, Source: "grok_video", TaskID: "task-1", UserID: 1, BillingUserID: 1, APIKeyID: 2, GroupID: 3, AccountID: account.ID,
		Model: "grok-imagine-video", Status: "queued", BillingStatus: "pending", CreatedAt: time.Now().Add(-time.Minute),
		Pending: &GrokVideoPendingBilling{Model: "grok-imagine-video", BillingModel: "grok-imagine-video", VideoResolution: "480p", VideoDurationSeconds: 8,
			CreatedAt: time.Now().Add(-time.Minute).Format(time.RFC3339Nano), SeedanceBilling: &SeedanceBillingSnapshot{Group: group, BillingMode: APIKeyBillingModeBalance}}}
	return r, task, logs, gateway.usageBillingRepo.(*openAIRecordUsageBillingRepoStub), calls
}

func TestLegacyVideoRecoveryPersistsBeforeBillingAndRetriesLogWithoutGenerating(t *testing.T) {
	r, task, logs, billing, calls := legacyRecoveryFixture(t, `{"status":"done","video":{"url":"https://example.com/v.mp4?token=secret","duration":8}}`)
	logs.err = errors.New("temporary database failure")
	require.Error(t, r.RecoverLegacyVideoTask(context.Background(), task))
	require.Equal(t, 1, *calls)
	require.Equal(t, 1, billing.calls)
	require.NotEqual(t, "settled", task.BillingStatus)
	require.GreaterOrEqual(t, r.repo.(*legacyRecoveryRepoStub).checkpoints, 2)
	require.NotContains(t, string(task.SettlementResult), "token=secret")
	require.NotContains(t, string(task.SettlementResult), "upstream-key")
	// 重启后通过保存的报价恢复，模拟账本返回实际余额分配；不能重新按估算价写日志。
	var restored LegacyVideoTask
	raw, err := json.Marshal(task)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &restored))
	logs.err = nil
	billing.result = &UsageBillingApplyResult{Applied: false, BalanceAmountUSD: 0.12}
	require.NoError(t, r.RecoverLegacyVideoTask(context.Background(), &restored))
	require.Equal(t, 1, *calls)
	require.Equal(t, "settled", restored.BillingStatus)
	require.InDelta(t, 0.12, logs.lastLog.ActualCost, 1e-10)
	require.InDelta(t, 0.12, logs.lastLog.BalanceAmountUSD, 1e-10)
}

func TestLegacyVideoRecoveryStorageFailureCannotCharge(t *testing.T) {
	r, task, _, billing, _ := legacyRecoveryFixture(t, `{"status":"done","video":{"url":"https://example.com/v.mp4","duration":8}}`)
	r.repo.(*legacyRecoveryRepoStub).checkpointErr = errors.New("db offline")
	require.Error(t, r.RecoverLegacyVideoTask(context.Background(), task))
	require.Zero(t, billing.calls)
}

func TestLegacyVideoRecoveryTerminalAndUnknownStates(t *testing.T) {
	for _, state := range []string{"failed", "expired", "pending"} {
		t.Run(state, func(t *testing.T) {
			r, task, _, billing, _ := legacyRecoveryFixture(t, `{"status":"`+state+`"}`)
			require.NoError(t, r.RecoverLegacyVideoTask(context.Background(), task))
			require.Zero(t, billing.calls)
			if state != "pending" {
				require.Equal(t, "not_billable", task.BillingStatus)
			} else {
				require.Equal(t, "pending", task.BillingStatus)
			}
		})
	}
	r, task, _, billing, _ := legacyRecoveryFixture(t, `{"status":"done","video":{"url":"https://example.com/v.mp4","duration":8}}`)
	task.Pending.SeedanceBilling = nil
	require.Error(t, r.RecoverLegacyVideoTask(context.Background(), task))
	require.Equal(t, "completed", task.Status)
	require.Equal(t, "reconciliation", task.BillingStatus)
	require.Zero(t, billing.calls)
}

func TestLegacyVideoQuotePreparationDoesNotChargeOrPersistUsage(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	s := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	in := &OpenAIRecordUsageInput{PrepareLegacyVideoQuote: true, LegacyVideoTaskID: 5,
		Result: &OpenAIForwardResult{RequestID: "grok-video:one", Model: "grok-imagine-video", VideoCount: 1, VideoResolution: "480p", VideoDurationSeconds: 8},
		APIKey: &APIKey{ID: 2, UserID: 1, BillingMode: APIKeyBillingModeBalance}, User: &User{ID: 1}, Account: &Account{ID: 9, Platform: PlatformGrok}}
	require.NoError(t, s.RecordUsage(context.Background(), in))
	require.NotNil(t, in.LegacyVideoQuote)
	require.Zero(t, logs.calls)
	require.Zero(t, s.usageBillingRepo.(*openAIRecordUsageBillingRepoStub).calls)
	require.Equal(t, int64(5), in.LegacyVideoQuote.Command.LegacyVideoTaskID)
}

// 简易模式只累计 Key 窗口时，保留原有成本统计，不把零资金回执误作零用量。
func TestLegacyVideoSimpleModeWindowReplayPreservesUsageCost(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	s := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	s.usageBillingRepo.(*openAIRecordUsageBillingRepoStub).result = &UsageBillingApplyResult{Applied: false}
	quote := &LegacyVideoBillingQuote{
		SimpleModeKeyRateLimitOnly: true,
		Cost:                       &CostBreakdown{TotalCost: 2, ActualCost: 3},
		Command:                    &UsageBillingCommand{LegacyVideoTaskID: 5, RequestID: "grok-video:simple", APIKeyID: 2, UserID: 1, AccountID: 9, APIKeyRateLimitCost: 3},
		Log:                        &UsageLog{APIKeyID: 2, BillingUserID: 1, AccountID: 9, TotalCost: 2, ActualCost: 3, RateMultiplier: 1.5},
	}
	require.NoError(t, s.RecordUsage(context.Background(), &OpenAIRecordUsageInput{LegacyVideoQuote: quote,
		APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 9}}))
	require.Equal(t, float64(3), logs.lastLog.ActualCost)
	require.Equal(t, 1.5, logs.lastLog.RateMultiplier)
	require.Zero(t, logs.lastLog.BalanceAmountUSD)
}
