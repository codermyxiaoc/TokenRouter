package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/handler"
	adminhandler "github.com/TokenFlux/TokenRouter/internal/handler/admin"
	servermiddleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Claude 兑换与 Codex 重置都消耗上游次数，必须使用相同管理面保护。
func TestClaudeResetRedeemRouteMatchesCodexResetProtection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Account: &adminhandler.AccountHandler{}, OpenAIOAuth: &adminhandler.OpenAIOAuthHandler{}}}
	chains := map[string][]string{}
	adminAuth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) {
		names := c.HandlerNames()
		chains[c.FullPath()] = names[:len(names)-1]
		if c.GetHeader("Authorization") == "" {
			servermiddleware.AbortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "Authorization required")
			return
		}
		servermiddleware.AbortWithError(c, http.StatusForbidden, "FORBIDDEN", "Admin access required")
	})
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })
	RegisterAdminRoutes(router.Group("/api/v1"), handlers, adminAuth, auditLog, stepUp, nil)

	codex := "/api/v1/admin/openai/accounts/1/reset-quota"
	claude := "/api/v1/admin/accounts/1/claude/reset-credits/redeem"
	for _, path := range []string{codex, claude} {
		for _, auth := range []string{"", "Bearer user-token"} {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, path, nil)
			if auth != "" {
				request.Header.Set("Authorization", auth)
			}
			router.ServeHTTP(recorder, request)
			if auth == "" {
				require.Equal(t, http.StatusUnauthorized, recorder.Code, path)
			} else {
				require.Equal(t, http.StatusForbidden, recorder.Code, path)
			}
		}
	}
	codexChain := chains["/api/v1/admin/openai/accounts/:id/reset-quota"]
	claudeChain := chains["/api/v1/admin/accounts/:id/claude/reset-credits/redeem"]
	require.NotEmpty(t, codexChain)
	require.Equal(t, strings.Join(codexChain, "\n"), strings.Join(claudeChain, "\n"))
}
