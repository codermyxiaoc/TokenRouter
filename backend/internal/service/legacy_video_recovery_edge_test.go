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

	"github.com/stretchr/testify/require"
)

type deletedLegacyRecoveryKey struct{ APIKeyRepository }

func (*deletedLegacyRecoveryKey) GetByID(context.Context, int64) (*APIKey, error) {
	return nil, ErrAPIKeyNotFound
}

// 用户删除 Key 只禁止新请求，不能免除已经持久受理任务的原扣款。
func TestLegacyVideoRecoveryDeletedKeyRetainsAcceptedBilling(t *testing.T) {
	r, task, logs, billing, calls := legacyRecoveryFixture(t, `{"status":"done","video":{"url":"https://example.com/v.mp4","duration":8}}`)
	r.keys = &deletedLegacyRecoveryKey{}
	require.NoError(t, r.RecoverLegacyVideoTask(context.Background(), task))
	require.Equal(t, 1, *calls)
	require.Equal(t, 1, billing.calls)
	require.Equal(t, 1, logs.calls)
	require.Equal(t, "settled", task.BillingStatus)
	require.Equal(t, task.UserID, logs.lastLog.UserID)
	require.Equal(t, task.BillingUserID, logs.lastLog.BillingUserID)
}

// 终态证据已保存而投影暂时失败时，重试只补本地数据，不再依赖上游仍保留任务。
func TestLegacyVideoRecoveryFailedTerminalRetriesProjectionWithoutPolling(t *testing.T) {
	r, task, _, billing, calls := legacyRecoveryFixture(t, `{"status":"failed","error":"invalid image"}`)
	observer := r.observer.(*legacyRecoveryObserver)
	observer.err = errors.New("projection temporarily unavailable")
	require.Error(t, r.RecoverLegacyVideoTask(context.Background(), task))
	require.Equal(t, "failed", task.Status)
	require.NotEmpty(t, task.SettlementResult)
	require.Equal(t, 1, *calls)
	observer.err = nil
	r.gateway.accountRepo = &deletedLegacyRecoveryAccount{}
	require.NoError(t, r.RecoverLegacyVideoTask(context.Background(), task))
	require.Equal(t, 1, *calls)
	require.Equal(t, 2, observer.calls)
	require.Equal(t, "not_billable", task.BillingStatus)
	require.Zero(t, billing.calls)
}

// 上游先给完成状态、稍后补 Token 时必须继续恢复用量，不能永久冻结首次空用量。
func TestLegacyVideoRecoverySeedanceDelayedUsageIsPolledAndBilled(t *testing.T) {
	r, task, logs, billing, _ := legacyRecoveryFixture(t, "")
	account := seedanceTestAccount()
	r.gateway.accountRepo = &openAIRecordUsageAccountRepoStub{account: account}
	task.Source, task.AccountID = "seedance_video", account.ID
	task.Pending.Model, task.Pending.BillingModel = "seedance-test", "seedance-test"
	task.Pending.UpstreamModel = "seedance-test"
	price := 10.0
	group := task.Pending.SeedanceBilling.Group
	group.Platform = PlatformOpenAI
	group.ModelPricing = []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"seedance-test"}, BillingMode: BillingModeToken, OutputPrice: &price}}
	// 基础账单 fixture 没有装配分组价格解析器，这里显式使用生产同款解析链。
	r.gateway.resolver = NewModelPricingResolver(nil, r.gateway.billingService)
	calls := 0
	r.gateway.httpUpstream = &legacyVideoTransportStub{do: func(req *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "/api/v3/contents/generations/tasks/task-1", req.URL.Path)
		body := `{"id":"task-1","status":"succeeded","model":"seedance-test","content":{"video_url":"https://example.com/v.mp4"}}`
		if calls > 1 {
			body = `{"id":"task-1","status":"succeeded","model":"seedance-test","content":{"video_url":"https://example.com/v.mp4"},"usage":{"completion_tokens":1000}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	require.ErrorContains(t, r.RecoverLegacyVideoTask(context.Background(), task), "真实 Token")
	require.Equal(t, "completed", task.Status)
	require.Zero(t, billing.calls)
	// 模拟重启从持久记录恢复，不能只依赖上一次调用的内存状态。
	raw, err := json.Marshal(task)
	require.NoError(t, err)
	var restored LegacyVideoTask
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.NoError(t, r.RecoverLegacyVideoTask(context.Background(), &restored))
	require.Equal(t, 2, calls)
	require.Equal(t, 1, billing.calls)
	require.Equal(t, 1000, logs.lastLog.OutputTokens)
	require.Equal(t, "settled", restored.BillingStatus)
}

type deletedLegacyRecoveryAccount struct{ AccountRepository }

func (*deletedLegacyRecoveryAccount) GetByID(context.Context, int64) (*Account, error) {
	return nil, ErrAccountNotFound
}

// 冻结报价已经确认了原账号和费用，删除账号不能阻止付款回执补写使用记录。
func TestLegacyVideoRecoveryDeletedAccountReplaysFrozenQuote(t *testing.T) {
	r, task, logs, billing, calls := legacyRecoveryFixture(t, `{"status":"done","video":{"url":"https://example.com/v.mp4","duration":8}}`)
	logs.err = errors.New("temporary usage write failure")
	require.Error(t, r.RecoverLegacyVideoTask(context.Background(), task))
	require.Equal(t, 1, billing.calls)
	var evidence legacyVideoEvidence
	require.NoError(t, json.Unmarshal(task.SettlementResult, &evidence))
	require.NotNil(t, evidence.Quote)
	// 模拟原付款已提交，重启后账号被删除，但去重账本仍保留真实分配金额。
	raw, err := json.Marshal(task)
	require.NoError(t, err)
	var restored LegacyVideoTask
	require.NoError(t, json.Unmarshal(raw, &restored))
	r.gateway.accountRepo = &deletedLegacyRecoveryAccount{}
	logs.err = nil
	billing.result = &UsageBillingApplyResult{Applied: false, BalanceAmountUSD: 0.12}
	require.NoError(t, r.RecoverLegacyVideoTask(context.Background(), &restored))
	require.Equal(t, 1, *calls, "冻结报价恢复不得再次请求上游")
	require.Equal(t, 2, billing.calls, "重放由账本返回原付款回执")
	require.Equal(t, "settled", restored.BillingStatus)
	require.Equal(t, task.AccountID, logs.lastLog.AccountID)
	require.InDelta(t, 0.12, logs.lastLog.ActualCost, 1e-10)
}
