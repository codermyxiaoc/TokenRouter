//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/model"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// errorPassthroughRepoStub 是 handler 层规则测试使用的最小 ErrorPassthroughRepository，
// 原样返回规则，写方法在此不会被调用。
type errorPassthroughRepoStub struct {
	rules []*model.ErrorPassthroughRule
}

func (s *errorPassthroughRepoStub) List(context.Context) ([]*model.ErrorPassthroughRule, error) {
	return s.rules, nil
}

func (s *errorPassthroughRepoStub) GetByID(context.Context, int64) (*model.ErrorPassthroughRule, error) {
	return nil, nil
}

func (s *errorPassthroughRepoStub) Create(_ context.Context, rule *model.ErrorPassthroughRule) (*model.ErrorPassthroughRule, error) {
	return rule, nil
}

func (s *errorPassthroughRepoStub) Update(_ context.Context, rule *model.ErrorPassthroughRule) (*model.ErrorPassthroughRule, error) {
	return rule, nil
}

func (s *errorPassthroughRepoStub) Delete(context.Context, int64) error { return nil }

func newBodyLimitPassthroughHandler(t *testing.T, rule *model.ErrorPassthroughRule) *OpenAIGatewayHandler {
	t.Helper()
	repo := &errorPassthroughRepoStub{}
	if rule != nil {
		repo.rules = []*model.ErrorPassthroughRule{rule}
	}
	return &OpenAIGatewayHandler{
		errorPassthroughService: service.NewErrorPassthroughService(repo, nil),
	}
}

func newBodyLimitPassthroughRule(responseCode int, customMessage string) *model.ErrorPassthroughRule {
	msg := customMessage
	return &model.ErrorPassthroughRule{
		ID:              1,
		Name:            "body limit -> client error",
		Enabled:         true,
		Priority:        1,
		ErrorCodes:      []int{http.StatusRequestEntityTooLarge},
		MatchMode:       model.MatchModeAny,
		Platforms:       []string{model.PlatformOpenAI},
		PassthroughCode: false,
		ResponseCode:    &responseCode,
		PassthroughBody: false,
		CustomMessage:   &msg,
	}
}

// 已配置的 413 规则必须优先于内置请求体过大映射；否则内置分支会先于通用透传规则返回，规则永远无法命中。
func TestOpenAIBodyLimitFailoverExhausted_PassthroughRuleOverridesBuiltInMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

	h := newBodyLimitPassthroughHandler(t, newBodyLimitPassthroughRule(http.StatusBadRequest, "Request body exceeds the upstream limit"))

	h.handleFailoverExhausted(c, bodyLimitFailoverTestError(), false)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	errBody, ok := envelope["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "upstream_error", errBody["type"])
	require.Equal(t, "Request body exceeds the upstream limit", errBody["message"])
	require.NotContains(t, rec.Body.String(), "must-not-leak")
	// fork 语义：规则命中时仍标记为请求级错误，避免智能路由跨组重放超大请求
	require.True(t, service.IsOpsRequestScopedError(c))
}

// SSE 流已开始时，规则覆盖同样生效。
func TestOpenAIBodyLimitFailoverExhausted_PassthroughRuleAppliesToStartedStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

	h := newBodyLimitPassthroughHandler(t, newBodyLimitPassthroughRule(http.StatusBadRequest, "Request body exceeds the upstream limit"))

	h.handleFailoverExhausted(c, bodyLimitFailoverTestError(), true)

	body := rec.Body.String()
	require.Contains(t, body, `"message":"Request body exceeds the upstream limit"`)
	require.NotContains(t, body, "must-not-leak")
}

// 未配置规则时保持内置 413 映射。
func TestOpenAIBodyLimitFailoverExhausted_NoRuleKeepsBuiltIn413(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

	h := newBodyLimitPassthroughHandler(t, nil)

	h.handleFailoverExhausted(c, bodyLimitFailoverTestError(), false)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Contains(t, rec.Body.String(), service.OpenAIRequestBodyTooLargeClientMessage)
}

// 针对其他上游状态码的规则不影响 413 路径。
func TestOpenAIBodyLimitFailoverExhausted_NonMatchingRuleKeepsBuiltIn413(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

	rule := newBodyLimitPassthroughRule(http.StatusBadRequest, "Request body exceeds the upstream limit")
	rule.ErrorCodes = []int{http.StatusServiceUnavailable}
	h := newBodyLimitPassthroughHandler(t, rule)

	h.handleFailoverExhausted(c, bodyLimitFailoverTestError(), false)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Contains(t, rec.Body.String(), service.OpenAIRequestBodyTooLargeClientMessage)
}

// 命中规则的 skip_monitoring 在该路径上仍需生效。
func TestOpenAIBodyLimitFailoverExhausted_PassthroughRuleSkipMonitoring(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

	rule := newBodyLimitPassthroughRule(http.StatusBadRequest, "Request body exceeds the upstream limit")
	rule.SkipMonitoring = true
	h := newBodyLimitPassthroughHandler(t, rule)

	h.handleFailoverExhausted(c, bodyLimitFailoverTestError(), false)

	value, exists := c.Get(service.OpsSkipPassthroughKey)
	require.True(t, exists)
	require.Equal(t, true, value)
}
