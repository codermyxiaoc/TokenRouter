//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIQuotaWorkflowStub struct {
	resetResult *service.OpenAIQuotaResetResult
	resetErr    error
	queryResult *service.OpenAIQuotaUsage
	queryErr    error
	cacheErr    error

	resetCalls          int
	queryCalls          int
	cacheCalls          int
	cacheCreditsCalls   int
	cachePostResetCalls int
	queryCtxErr         error
	cacheCtxErr         error
}

func (s *openAIQuotaWorkflowStub) ResetCredit(context.Context, int64) (*service.OpenAIQuotaResetResult, error) {
	s.resetCalls++
	return s.resetResult, s.resetErr
}

func (s *openAIQuotaWorkflowStub) QueryUsage(ctx context.Context, _ int64) (*service.OpenAIQuotaUsage, error) {
	s.queryCalls++
	s.queryCtxErr = ctx.Err()
	return s.queryResult, s.queryErr
}

func (s *openAIQuotaWorkflowStub) CacheResetCreditsSnapshot(ctx context.Context, _ int64, _ *service.OpenAIRateLimitResetCredits) error {
	s.cacheCalls++
	s.cacheCreditsCalls++
	s.cacheCtxErr = ctx.Err()
	return s.cacheErr
}

func (s *openAIQuotaWorkflowStub) CachePostResetSnapshot(ctx context.Context, _ int64, _ *service.OpenAIQuotaUsage) error {
	s.cacheCalls++
	s.cachePostResetCalls++
	s.cacheCtxErr = ctx.Err()
	return s.cacheErr
}

type openAIAccountStateRecovererStub struct {
	err         error
	calls       int
	accountID   int64
	lastOptions service.AccountRecoveryOptions
	lastCtxErr  error
}

func (s *openAIAccountStateRecovererStub) RecoverAccountState(ctx context.Context, accountID int64, options service.AccountRecoveryOptions) (*service.SuccessfulTestRecoveryResult, error) {
	s.calls++
	s.accountID = accountID
	s.lastOptions = options
	s.lastCtxErr = ctx.Err()
	return &service.SuccessfulTestRecoveryResult{}, s.err
}

type openAIResetAdminServiceStub struct {
	service.AdminService
	account *service.Account
	err     error
	calls   int
}

func (s *openAIResetAdminServiceStub) GetAccount(context.Context, int64) (*service.Account, error) {
	s.calls++
	return s.account, s.err
}

type openAIQuotaResetEnvelope struct {
	Data openAIQuotaResetResponse `json:"data"`
}

type openAIQuotaRefreshEnvelope struct {
	Data openAIQuotaRefreshResponse `json:"data"`
}

func performOpenAIQuotaResetRequest(t *testing.T, handler *OpenAIOAuthHandler, ctx context.Context) (int, openAIQuotaResetEnvelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/admin/openai/accounts/:id/reset-quota", handler.ResetQuota)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/openai/accounts/42/reset-quota", nil)
	if ctx != nil {
		request = request.WithContext(ctx)
	}
	router.ServeHTTP(recorder, request)

	var envelope openAIQuotaResetEnvelope
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	return recorder.Code, envelope
}

func performOpenAIQuotaRefreshRequest(t *testing.T, handler *OpenAIOAuthHandler) (int, openAIQuotaRefreshEnvelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/admin/openai/accounts/:id/quota/refresh", handler.RefreshQuota)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/openai/accounts/42/quota/refresh", nil)
	router.ServeHTTP(recorder, request)

	var envelope openAIQuotaRefreshEnvelope
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	return recorder.Code, envelope
}

func successfulOpenAIQuotaWorkflowStub() *openAIQuotaWorkflowStub {
	return &openAIQuotaWorkflowStub{
		resetResult: &service.OpenAIQuotaResetResult{Code: "success", WindowsReset: 1},
		queryResult: &service.OpenAIQuotaUsage{
			FetchedAt: 123,
			RateLimitResetCredits: &service.OpenAIRateLimitResetCredits{
				AvailableCount: 0,
				Credits:        []service.OpenAIRateLimitResetCreditDetail{},
			},
		},
	}
}

func recoveredOpenAIAccountStub() *openAIResetAdminServiceStub {
	return &openAIResetAdminServiceStub{account: &service.Account{
		ID:          42,
		Name:        "recovered",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: false,
	}}
}

func TestOpenAIResetQuotaRecoversAccountBeforeRefreshingCache(t *testing.T) {
	quota := successfulOpenAIQuotaWorkflowStub()
	recoverer := &openAIAccountStateRecovererStub{}
	adminService := recoveredOpenAIAccountStub()
	handler := &OpenAIOAuthHandler{
		adminService:     adminService,
		quotaService:     quota,
		rateLimitService: recoverer,
	}

	status, envelope := performOpenAIQuotaResetRequest(t, handler, nil)

	require.Equal(t, http.StatusOK, status)
	require.Empty(t, envelope.Data.WarningCode)
	require.True(t, envelope.Data.AccountStateRecovered)
	require.True(t, envelope.Data.CacheRefreshed)
	require.NotNil(t, envelope.Data.Quota)
	require.NotNil(t, envelope.Data.Account)
	require.False(t, envelope.Data.Account.Schedulable, "不得改动人工调度开关")
	require.Equal(t, int64(42), recoverer.accountID)
	require.True(t, recoverer.lastOptions.InvalidateToken)
	require.Equal(t, 1, quota.resetCalls)
	require.Equal(t, 1, quota.queryCalls)
	require.Equal(t, 1, quota.cacheCalls)
	require.Zero(t, quota.cacheCreditsCalls, "重置后应写入完整 usage 快照，不应只写 credits")
	require.Equal(t, 1, quota.cachePostResetCalls)
	require.Equal(t, 1, adminService.calls)
}

func TestOpenAIResetQuotaRecoveryFailureStopsPostProcessing(t *testing.T) {
	quota := successfulOpenAIQuotaWorkflowStub()
	recoverer := &openAIAccountStateRecovererStub{err: errors.New("recovery failed")}
	adminService := recoveredOpenAIAccountStub()
	handler := &OpenAIOAuthHandler{
		adminService:     adminService,
		quotaService:     quota,
		rateLimitService: recoverer,
	}

	status, envelope := performOpenAIQuotaResetRequest(t, handler, nil)

	require.Equal(t, http.StatusOK, status)
	require.Equal(t, openAIQuotaResetWarningAccountRecoveryFailed, envelope.Data.WarningCode)
	require.False(t, envelope.Data.AccountStateRecovered)
	require.Zero(t, quota.queryCalls)
	require.Zero(t, quota.cacheCalls)
	require.Zero(t, adminService.calls)
}

func TestOpenAIResetQuotaCacheFailureStillReturnsRecoveredAccount(t *testing.T) {
	quota := successfulOpenAIQuotaWorkflowStub()
	quota.cacheErr = errors.New("cache write failed")
	recoverer := &openAIAccountStateRecovererStub{}
	adminService := recoveredOpenAIAccountStub()
	handler := &OpenAIOAuthHandler{
		adminService:     adminService,
		quotaService:     quota,
		rateLimitService: recoverer,
	}

	status, envelope := performOpenAIQuotaResetRequest(t, handler, nil)

	require.Equal(t, http.StatusOK, status)
	require.Equal(t, openAIQuotaResetWarningCacheRefreshFailed, envelope.Data.WarningCode)
	require.True(t, envelope.Data.AccountStateRecovered)
	require.False(t, envelope.Data.CacheRefreshed)
	require.Nil(t, envelope.Data.Quota)
	require.NotNil(t, envelope.Data.Account)
}

func TestOpenAIResetQuotaPostProcessingSurvivesClientCancellation(t *testing.T) {
	quota := successfulOpenAIQuotaWorkflowStub()
	recoverer := &openAIAccountStateRecovererStub{}
	adminService := recoveredOpenAIAccountStub()
	handler := &OpenAIOAuthHandler{
		adminService:     adminService,
		quotaService:     quota,
		rateLimitService: recoverer,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	status, _ := performOpenAIQuotaResetRequest(t, handler, ctx)

	require.Equal(t, http.StatusOK, status)
	require.NoError(t, recoverer.lastCtxErr)
	require.NoError(t, quota.queryCtxErr)
	require.NoError(t, quota.cacheCtxErr)
}

// 无消费与未知结果必须原样返回，不能清除账号限流、刷新为零用量或自动再次消费。
func TestOpenAIResetQuotaUnconfirmedResultPreservesAccountState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    string
		windows int
	}{
		{"没有可用次数", "no_credit", 0},
		{"无次数结果含矛盾窗口数", "no_credit", 1},
		{"空响应", "", 0},
		{"未知结果", "pending", 0},
		{"未知结果含窗口数", "future_result", 1},
		{"成功码未重置窗口", "reset", 0},
		{"非法负数窗口", "success", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quota := successfulOpenAIQuotaWorkflowStub()
			quota.resetResult = &service.OpenAIQuotaResetResult{Code: tc.code, WindowsReset: tc.windows}
			recoverer := &openAIAccountStateRecovererStub{}
			adminService := recoveredOpenAIAccountStub()
			handler := &OpenAIOAuthHandler{adminService: adminService, quotaService: quota, rateLimitService: recoverer}
			status, envelope := performOpenAIQuotaResetRequest(t, handler, nil)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, tc.code, envelope.Data.Code)
			require.Equal(t, tc.windows, envelope.Data.WindowsReset)
			require.False(t, envelope.Data.AccountStateRecovered)
			require.False(t, envelope.Data.CacheRefreshed)
			require.Nil(t, envelope.Data.Account)
			require.Nil(t, envelope.Data.Quota)
			require.Zero(t, recoverer.calls)
			require.Zero(t, quota.queryCalls)
			require.Zero(t, quota.cacheCalls)
			require.Zero(t, adminService.calls)
			require.Equal(t, 1, quota.resetCalls)
		})
	}
}

// 兼容已有上游返回及测试夹具中的三种明确成功码，保持原有状态恢复流程。
func TestOpenAIResetQuotaAcceptsKnownAppliedResults(t *testing.T) {
	for _, code := range []string{"reset", "success", "ok", " RESET "} {
		t.Run(code, func(t *testing.T) {
			quota := successfulOpenAIQuotaWorkflowStub()
			quota.resetResult.Code = code
			recoverer := &openAIAccountStateRecovererStub{}
			handler := &OpenAIOAuthHandler{adminService: recoveredOpenAIAccountStub(), quotaService: quota, rateLimitService: recoverer}
			status, envelope := performOpenAIQuotaResetRequest(t, handler, nil)
			require.Equal(t, http.StatusOK, status)
			require.True(t, envelope.Data.AccountStateRecovered)
			require.True(t, envelope.Data.CacheRefreshed)
			require.Equal(t, 1, recoverer.calls)
			require.Equal(t, 1, quota.resetCalls)
		})
	}
}

func TestOpenAIRefreshQuotaPersistFailureStillReturnsUsage(t *testing.T) {
	quota := successfulOpenAIQuotaWorkflowStub()
	quota.queryResult = &service.OpenAIQuotaUsage{
		FetchedAt:             456,
		RateLimitResetCredits: &service.OpenAIRateLimitResetCredits{AvailableCount: 2},
	}
	quota.cacheErr = errors.New("expiration details unavailable")
	handler := &OpenAIOAuthHandler{
		adminService: &openAIResetAdminServiceStub{},
		quotaService: quota,
	}

	status, envelope := performOpenAIQuotaRefreshRequest(t, handler)

	require.Equal(t, http.StatusOK, status)
	require.False(t, envelope.Data.CachePersisted)
	require.Equal(t, int64(456), envelope.Data.FetchedAt)
	require.NotNil(t, envelope.Data.RateLimitResetCredits)
	require.Equal(t, 2, envelope.Data.RateLimitResetCredits.AvailableCount)
	require.Equal(t, 1, quota.cacheCreditsCalls, "手动 refresh 只应更新 credits 快照")
	require.Zero(t, quota.cachePostResetCalls)
}

func TestNewOpenAIOAuthHandlerKeepsNilQuotaCapabilitiesGuarded(t *testing.T) {
	handler := NewOpenAIOAuthHandler(nil, newStubAdminService(), nil, nil)

	require.Nil(t, handler.quotaService)
	require.Nil(t, handler.rateLimitService)
}
