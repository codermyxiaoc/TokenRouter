package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 预览测试使用独立仓储替身，不访问真实检测服务、数据库或用户密钥。
type intelligencePreviewTestRepo struct {
	service.IntelligenceRepository
	run    *service.IntelligenceRun
	config *service.IntelligenceConfig
	runErr error
}

func (r *intelligencePreviewTestRepo) GetRun(_ context.Context, id string) (*service.IntelligenceRun, error) {
	if r.runErr != nil {
		return nil, r.runErr
	}
	if r.run == nil || r.run.ID != id {
		return nil, service.ErrIntelligenceNotFound
	}
	return r.run, nil
}
func (r *intelligencePreviewTestRepo) GetConfig(_ context.Context, id int64) (*service.IntelligenceConfig, error) {
	if r.config == nil || r.config.ID != id {
		return nil, service.ErrIntelligenceNotFound
	}
	return r.config, nil
}

type intelligencePreviewTestUsers struct {
	service.UserRepository
	user *service.User
	err  error
}

func (r *intelligencePreviewTestUsers) GetByID(_ context.Context, id int64) (*service.User, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.user == nil || r.user.ID != id {
		return nil, errors.New("user unavailable")
	}
	return r.user, nil
}

type intelligencePreviewTestGroups struct {
	groups []service.Group
	err    error
}

func (r *intelligencePreviewTestGroups) GetAvailableGroups(context.Context, int64) ([]service.Group, error) {
	return r.groups, r.err
}

type intelligencePreviewFixture struct {
	handler *IntelligenceHandler
	router  *gin.Engine
	repo    *intelligencePreviewTestRepo
	users   *intelligencePreviewTestUsers
	groups  *intelligencePreviewTestGroups
	enabled bool
}

func newIntelligencePreviewFixture(t *testing.T) *intelligencePreviewFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &intelligencePreviewFixture{enabled: true}
	f.repo = &intelligencePreviewTestRepo{
		run:    &service.IntelligenceRun{ID: "iq_local_result", ConfigID: 1, GroupID: 11, Model: "test-model", Benchmark: "drawing", Status: "completed", Verdict: "passed", HTML: `<!doctype html><html><body><canvas id="art"></canvas><script>document.querySelector('canvas').width=100</script></body></html>`, RemoteID: "remote-private-bearer-id", HasArtifact: true},
		config: &service.IntelligenceConfig{ID: 1, GroupID: 11, Model: "test-model", Benchmark: "drawing", Enabled: true},
	}
	f.users = &intelligencePreviewTestUsers{user: &service.User{ID: 7, Role: service.RoleUser, Status: service.StatusActive}}
	f.groups = &intelligencePreviewTestGroups{groups: []service.Group{{ID: 11, Status: service.StatusActive}}}
	enabled := func(context.Context) bool { return f.enabled }
	svc := service.NewIntelligenceService(f.repo, nil, nil, f.groups, nil, enabled)
	f.handler = NewIntelligenceHandler(svc)
	f.handler.preview = &intelligencePreviewAccess{secret: []byte("unit-preview-secret"), users: f.users, enabled: enabled}
	f.router = gin.New()
	f.router.Use(func(c *gin.Context) {
		// 模拟全站严格头，确认仅内容响应覆盖，不要求主站放宽 inline script。
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'nonce-main'; frame-ancestors 'none'")
		c.Header("X-Frame-Options", "DENY")
		if role := c.GetHeader("X-Test-Role"); role != "" {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
			c.Set(string(middleware.ContextKeyUserRole), role)
		}
	})
	f.router.GET("/runs/:id/preview", f.handler.UserPreview)
	f.router.GET("/admin/runs/:id/preview", f.handler.AdminPreview)
	f.router.GET("/api/v1/intelligence-tests/preview-content/:ticket", f.handler.PreviewContent)
	return f
}

func (f *intelligencePreviewFixture) request(path, role string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if role != "" {
		req.Header.Set("X-Test-Role", role)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *intelligencePreviewFixture) issue(t *testing.T, admin bool) string {
	t.Helper()
	path, role := "/runs/iq_local_result/preview", service.RoleUser
	if admin {
		path = "/admin/runs/iq_local_result/preview"
		role = service.RoleAdmin
		f.users.user.Role = service.RoleAdmin
	}
	rec := f.request(path, role)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.NotContains(t, rec.Body.String(), f.repo.run.RemoteID)
	var envelope struct {
		Data struct {
			URL       string    `json:"url"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.True(t, strings.HasPrefix(envelope.Data.URL, "/api/v1/intelligence-tests/preview-content/"))
	require.WithinDuration(t, time.Now().Add(intelligencePreviewTTL), envelope.Data.ExpiresAt, 2*time.Second)
	return envelope.Data.URL
}

func TestIntelligencePreviewTicketValidation(t *testing.T) {
	secret := []byte("test-signing-key")
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	claims := intelligencePreviewClaims{RunID: "iq_123", UserID: 7, Expires: now.Add(4 * time.Minute).Unix()}
	ticket := signIntelligencePreview(secret, claims)
	got, err := verifyIntelligencePreview(secret, ticket, now)
	require.NoError(t, err)
	require.Equal(t, claims, got)

	parts := strings.Split(ticket, ".")
	changedClaims := claims
	changedClaims.Admin = true
	changedParts := strings.Split(signIntelligencePreview(secret, changedClaims), ".")
	cases := []struct {
		name, ticket string
		key          []byte
	}{
		{"changed privilege", changedParts[0] + "." + parts[1], secret},
		{"wrong key", ticket, []byte("other-key")},
		{"missing key", ticket, nil},
		{"missing separator", "no-ticket", secret},
		{"too many parts", ticket + ".extra", secret},
		{"malformed signature", parts[0] + ".!", secret},
		{"oversized", strings.Repeat("x", 1025), secret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { _, err := verifyIntelligencePreview(tc.key, tc.ticket, now); require.Error(t, err) })
	}
	for _, tc := range []struct {
		name   string
		change func(*intelligencePreviewClaims)
	}{
		{"expired", func(c *intelligencePreviewClaims) { c.Expires = now.Unix() }},
		{"excessive lifetime", func(c *intelligencePreviewClaims) { c.Expires = now.Add(intelligencePreviewTTL + 2*time.Second).Unix() }},
		{"missing run", func(c *intelligencePreviewClaims) { c.RunID = "" }},
		{"missing user", func(c *intelligencePreviewClaims) { c.UserID = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := claims
			tc.change(&invalid)
			_, err := verifyIntelligencePreview(secret, signIntelligencePreview(secret, invalid), now)
			require.Error(t, err)
		})
	}
}

func TestIntelligencePreviewContentHasIndependentSandboxHeaders(t *testing.T) {
	f := newIntelligencePreviewFixture(t)
	url := f.issue(t, false)
	// iframe 本身不携带 JWT，票据只能读取对应的已保存作品。
	rec := f.request(url, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, f.repo.run.HTML, rec.Body.String())
	require.Equal(t, intelligencePreviewCSP, rec.Header().Get("Content-Security-Policy"))
	require.Contains(t, rec.Header().Get("Content-Security-Policy"), "sandbox allow-scripts")
	require.NotContains(t, rec.Header().Get("Content-Security-Policy"), "allow-same-origin")
	require.Contains(t, rec.Header().Get("Content-Security-Policy"), "connect-src 'none'")
	require.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'")
	require.Equal(t, "SAMEORIGIN", rec.Header().Get("X-Frame-Options"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "inline", rec.Header().Get("Content-Disposition"))
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	require.Contains(t, rec.Header().Get("Permissions-Policy"), "camera=()")
	require.NotContains(t, rec.Body.String(), f.repo.run.RemoteID)
}

func TestIntelligencePreviewRechecksAccessAfterIssuance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		admin  bool
		change func(*intelligencePreviewFixture)
	}{
		{"global disabled", false, func(f *intelligencePreviewFixture) { f.enabled = false }},
		{"user disabled", false, func(f *intelligencePreviewFixture) { f.users.user.Status = "disabled" }},
		{"user deleted", false, func(f *intelligencePreviewFixture) { f.users.user = nil }},
		{"user lookup unavailable", false, func(f *intelligencePreviewFixture) { f.users.err = errors.New("database unavailable") }},
		{"group access revoked", false, func(f *intelligencePreviewFixture) { f.groups.groups = nil }},
		{"group query unavailable", false, func(f *intelligencePreviewFixture) { f.groups.err = errors.New("database unavailable") }},
		{"config disabled", false, func(f *intelligencePreviewFixture) { f.repo.config.Enabled = false }},
		{"config deleted", false, func(f *intelligencePreviewFixture) { f.repo.config = nil }},
		{"group identity mismatch", false, func(f *intelligencePreviewFixture) { f.repo.config.GroupID = 22 }},
		{"run deleted", false, func(f *intelligencePreviewFixture) { f.repo.run = nil }},
		{"old artwork pruned", false, func(f *intelligencePreviewFixture) { f.repo.run.HTML = ""; f.repo.run.HasArtifact = false }},
		{"wrong benchmark", false, func(f *intelligencePreviewFixture) { f.repo.run.Benchmark = "candy" }},
		{"admin downgraded", true, func(f *intelligencePreviewFixture) { f.users.user.Role = service.RoleUser }},
		{"admin disabled", true, func(f *intelligencePreviewFixture) { f.users.user.Status = "disabled" }},
		{"admin global disabled", true, func(f *intelligencePreviewFixture) { f.enabled = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIntelligencePreviewFixture(t)
			url := f.issue(t, tc.admin)
			tc.change(f)
			rec := f.request(url, "")
			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			require.NotContains(t, rec.Body.String(), "<canvas")
			require.NotContains(t, rec.Body.String(), "remote-private")
		})
	}
}

func TestIntelligencePreviewIssuanceRequiresRoleAndAvailableArtwork(t *testing.T) {
	for _, tc := range []struct {
		name, path, role string
		status           int
		change           func(*intelligencePreviewFixture)
	}{
		{"anonymous", "/runs/iq_local_result/preview", "", 401, nil},
		{"user cannot mint admin", "/admin/runs/iq_local_result/preview", service.RoleUser, 403, nil},
		{"disabled feature", "/runs/iq_local_result/preview", service.RoleUser, 403, func(f *intelligencePreviewFixture) { f.enabled = false }},
		{"unavailable preview signer", "/runs/iq_local_result/preview", service.RoleUser, 403, func(f *intelligencePreviewFixture) { f.handler.preview = nil }},
		{"unavailable artwork", "/runs/iq_local_result/preview", service.RoleUser, 404, func(f *intelligencePreviewFixture) { f.repo.run.HTML = "" }},
		{"user hidden group", "/runs/iq_local_result/preview", service.RoleUser, 404, func(f *intelligencePreviewFixture) { f.groups.groups = nil }},
		{"unknown run", "/runs/iq_not_existing/preview", service.RoleUser, 404, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIntelligencePreviewFixture(t)
			if tc.change != nil {
				tc.change(f)
			}
			rec := f.request(tc.path, tc.role)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.NotContains(t, rec.Body.String(), "preview-content/")
		})
	}
}

func TestIntelligencePreviewContentRejectsForgedAndExpiredTickets(t *testing.T) {
	f := newIntelligencePreviewFixture(t)
	url := f.issue(t, false)
	parts := strings.Split(url, "/")
	ticket := parts[len(parts)-1]
	claims := intelligencePreviewClaims{RunID: "iq_local_result", UserID: 7, Expires: time.Now().Add(-time.Second).Unix()}
	for _, bad := range []string{"invalid", ticket + ".invalid", signIntelligencePreview([]byte("wrong-key"), claims), signIntelligencePreview(f.handler.preview.secret, claims)} {
		rec := f.request("/api/v1/intelligence-tests/preview-content/"+bad, "")
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		require.NotContains(t, rec.Body.String(), "<canvas")
	}
}

func TestIntelligencePreviewAdminScopeIsExplicit(t *testing.T) {
	f := newIntelligencePreviewFixture(t)
	f.users.user.Role = service.RoleAdmin
	f.groups.groups = nil
	// 管理员使用用户入口时依然受分组可见性约束，不能隐式升级票据权限。
	rec := f.request("/runs/iq_local_result/preview", service.RoleAdmin)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	url := f.issue(t, true)
	rec = f.request(url, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, f.repo.run.HTML, rec.Body.String())
}
