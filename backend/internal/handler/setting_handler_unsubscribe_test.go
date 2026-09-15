package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const testUnsubscribeSecret = "notification-unsubscribe-test-secret"
const testUnsubscribePath = "/api/v1/settings/email-unsubscribe"

// 使用内存设置和签名夹具，不连接数据库、SMTP 或真实用户配置。
type unsubscribeSettingRepo struct {
	service.SettingRepository
	values map[string]string
	writes int
}

func (r *unsubscribeSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", service.ErrSettingNotFound
}

func (r *unsubscribeSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	r.writes++
	return nil
}

func newUnsubscribeTestRouter() (*gin.Engine, *unsubscribeSettingRepo, *service.NotificationEmailService) {
	gin.SetMode(gin.TestMode)
	repo := &unsubscribeSettingRepo{values: map[string]string{"notification_email_unsubscribe_secret": testUnsubscribeSecret}}
	mail := service.NewNotificationEmailService(repo, nil)
	h := NewSettingHandler(nil, "test")
	h.SetNotificationEmailService(mail)
	router := gin.New()
	router.GET(testUnsubscribePath, h.UnsubscribeNotificationEmail)
	router.POST(testUnsubscribePath, h.ConfirmNotificationEmailUnsubscribe)
	return router, repo, mail
}

// 按既有邮件令牌格式构建夹具，覆盖旧邮件链接在升级后的兼容性。
func signedUnsubscribeToken(t *testing.T, email, event string, expires time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"email": email, "event": event, "exp": expires.Unix()})
	require.NoError(t, err)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(testUnsubscribeSecret))
	_, err = mac.Write([]byte(encoded))
	require.NoError(t, err)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func unsubscribeRequest(router *gin.Engine, method, path, contentType, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func requireUnsubscribeSecurityHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "no-referrer", recorder.Header().Get("Referrer-Policy"))
	require.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", recorder.Header().Get("X-Frame-Options"))
	require.Contains(t, recorder.Header().Get("Content-Security-Policy"), "default-src 'none'")
	require.Contains(t, recorder.Header().Get("Content-Security-Policy"), "form-action 'self'")
	require.Contains(t, recorder.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
}

func TestNotificationEmailUnsubscribeGETDoesNotChangePreference(t *testing.T) {
	router, repo, mail := newUnsubscribeTestRouter()
	token := signedUnsubscribeToken(t, "user@example.com", service.NotificationEmailEventTicketStaffReply, time.Now().Add(time.Hour))
	// 模拟邮件客户端与用户重复预取同一链接，必须一直保持订阅状态。
	for range 2 {
		res := unsubscribeRequest(router, http.MethodGet, testUnsubscribePath+"?token="+url.QueryEscape(token), "", "")
		require.Equal(t, http.StatusOK, res.Code)
		require.Contains(t, res.Body.String(), `method="post"`)
		require.Contains(t, res.Body.String(), `name="confirm" value="unsubscribe"`)
		require.Contains(t, res.Body.String(), `name="token" value="`+token+`"`)
		requireUnsubscribeSecurityHeaders(t, res)
	}
	require.Zero(t, repo.writes)
	unsubscribed, err := mail.IsUnsubscribed(context.Background(), "user@example.com", service.NotificationEmailEventTicketStaffReply)
	require.NoError(t, err)
	require.False(t, unsubscribed)

	form := url.Values{"token": {token}, "confirm": {"unsubscribe"}}.Encode()
	res := unsubscribeRequest(router, http.MethodPost, testUnsubscribePath, "application/x-www-form-urlencoded; charset=UTF-8", form)
	require.Equal(t, http.StatusOK, res.Code)
	require.Contains(t, res.Body.String(), "已退订")
	requireUnsubscribeSecurityHeaders(t, res)
	require.Equal(t, 1, repo.writes)
	unsubscribed, err = mail.IsUnsubscribed(context.Background(), "user@example.com", service.NotificationEmailEventTicketStaffReply)
	require.NoError(t, err)
	require.True(t, unsubscribed)

	// 已存在的退订偏好不能因为再次打开邮件而自动恢复。
	res = unsubscribeRequest(router, http.MethodGet, testUnsubscribePath+"?token="+url.QueryEscape(token), "", "")
	require.Equal(t, http.StatusOK, res.Code)
	require.Equal(t, 1, repo.writes)
	unsubscribed, err = mail.IsUnsubscribed(context.Background(), "user@example.com", service.NotificationEmailEventTicketStaffReply)
	require.NoError(t, err)
	require.True(t, unsubscribed)
}

func TestNotificationEmailUnsubscribeRejectsInvalidTokensWithoutWrites(t *testing.T) {
	valid := signedUnsubscribeToken(t, "user@example.com", service.NotificationEmailEventTicketStaffReply, time.Now().Add(time.Hour))
	cases := map[string]string{
		"missing":       "",
		"malformed":     "invalid<script>alert(1)</script>",
		"bad signature": valid + "x",
		"expired":       signedUnsubscribeToken(t, "user@example.com", service.NotificationEmailEventTicketStaffReply, time.Now().Add(-time.Minute)),
		"transactional": signedUnsubscribeToken(t, "user@example.com", service.NotificationEmailEventAuthVerifyCode, time.Now().Add(time.Hour)),
		"too long":      strings.Repeat("x", notificationEmailUnsubscribeTokenLimit+1),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			router, repo, _ := newUnsubscribeTestRouter()
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				path, body := testUnsubscribePath+"?token="+url.QueryEscape(token), ""
				if method == http.MethodPost {
					path = testUnsubscribePath
					body = url.Values{"token": {token}, "confirm": {"unsubscribe"}}.Encode()
				}
				res := unsubscribeRequest(router, method, path, "application/x-www-form-urlencoded", body)
				require.Equal(t, http.StatusBadRequest, res.Code)
				require.NotContains(t, res.Body.String(), "<script>")
				requireUnsubscribeSecurityHeaders(t, res)
			}
			require.Zero(t, repo.writes)
		})
	}
}

func TestNotificationEmailUnsubscribePOSTRequiresBoundedExplicitForm(t *testing.T) {
	token := signedUnsubscribeToken(t, "user@example.com", service.NotificationEmailEventTicketStaffReply, time.Now().Add(time.Hour))
	for _, tc := range []struct {
		name, contentType, query, body string
		status                         int
	}{
		{"missing content type", "", "", "", http.StatusUnsupportedMediaType},
		{"json", "application/json", "", `{"token":"` + token + `","confirm":"unsubscribe"}`, http.StatusUnsupportedMediaType},
		{"multipart", "multipart/form-data; boundary=example", "", "", http.StatusUnsupportedMediaType},
		{"missing confirmation", "application/x-www-form-urlencoded", "", url.Values{"token": {token}}.Encode(), http.StatusBadRequest},
		{"wrong confirmation", "application/x-www-form-urlencoded", "", url.Values{"token": {token}, "confirm": {"yes"}}.Encode(), http.StatusBadRequest},
		{"query token only", "application/x-www-form-urlencoded", "?token=" + url.QueryEscape(token), "confirm=unsubscribe", http.StatusBadRequest},
		{"duplicate token", "application/x-www-form-urlencoded", "", url.Values{"token": {token, token}, "confirm": {"unsubscribe"}}.Encode(), http.StatusBadRequest},
		{"duplicate confirmation", "application/x-www-form-urlencoded", "", url.Values{"token": {token}, "confirm": {"unsubscribe", "unsubscribe"}}.Encode(), http.StatusBadRequest},
		{"bad encoding", "application/x-www-form-urlencoded", "", "token=%zz&confirm=unsubscribe", http.StatusBadRequest},
		{"too large", "application/x-www-form-urlencoded", "", "padding=" + strings.Repeat("x", 8192), http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, repo, _ := newUnsubscribeTestRouter()
			res := unsubscribeRequest(router, http.MethodPost, testUnsubscribePath+tc.query, tc.contentType, tc.body)
			require.Equal(t, tc.status, res.Code)
			require.Zero(t, repo.writes)
			requireUnsubscribeSecurityHeaders(t, res)
		})
	}
}

func TestNotificationEmailUnsubscribeEscapesSignedEmailInHTML(t *testing.T) {
	router, _, _ := newUnsubscribeTestRouter()
	email := `<img src=x onerror="alert(1)">@example.com`
	token := signedUnsubscribeToken(t, email, service.NotificationEmailEventTicketStaffReply, time.Now().Add(time.Hour))
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path, body := testUnsubscribePath+"?token="+url.QueryEscape(token), ""
		if method == http.MethodPost {
			path = testUnsubscribePath
			body = url.Values{"token": {token}, "confirm": {"unsubscribe"}}.Encode()
		}
		res := unsubscribeRequest(router, method, path, "application/x-www-form-urlencoded", body)
		require.Equal(t, http.StatusOK, res.Code)
		require.Contains(t, res.Body.String(), html.EscapeString(email))
		require.NotContains(t, res.Body.String(), "<img")
		require.NotContains(t, res.Body.String(), "<script")
	}
}

func TestNotificationEmailUnsubscribeRestoresTicketsOnlyAfterExplicitPOST(t *testing.T) {
	for _, event := range []string{service.NotificationEmailEventTicketStaffReply, service.NotificationEmailEventTicketCompleted, service.NotificationEmailEventTicketCancelled} {
		t.Run(event, func(t *testing.T) {
			router, repo, mail := newUnsubscribeTestRouter()
			token := signedUnsubscribeToken(t, "user@example.com", event, time.Now().Add(time.Hour))
			_, err := mail.Unsubscribe(context.Background(), token)
			require.NoError(t, err)
			originalWrites := repo.writes
			res := unsubscribeRequest(router, http.MethodGet, testUnsubscribePath+"?token="+url.QueryEscape(token), "", "")
			require.Equal(t, http.StatusOK, res.Code)
			require.Contains(t, res.Body.String(), "恢复工单邮件通知")
			require.Contains(t, res.Body.String(), `name="confirm" value="subscribe"`)
			require.Equal(t, originalWrites, repo.writes)
			stillUnsubscribed, err := mail.IsUnsubscribed(context.Background(), "user@example.com", event)
			require.NoError(t, err)
			require.True(t, stillUnsubscribed)

			form := url.Values{"token": {token}, "confirm": {"subscribe"}}.Encode()
			res = unsubscribeRequest(router, http.MethodPost, testUnsubscribePath, "application/x-www-form-urlencoded", form)
			require.Equal(t, http.StatusOK, res.Code)
			require.Contains(t, res.Body.String(), "已恢复工单邮件通知")
			requireUnsubscribeSecurityHeaders(t, res)
			for _, ticketEvent := range []string{service.NotificationEmailEventTicketStaffReply, service.NotificationEmailEventTicketCompleted, service.NotificationEmailEventTicketCancelled} {
				unsubscribed, err := mail.IsUnsubscribed(context.Background(), "user@example.com", ticketEvent)
				require.NoError(t, err)
				require.False(t, unsubscribed)
			}
		})
	}
}

func TestNotificationEmailUnsubscribeRejectsRestoreForOtherEvents(t *testing.T) {
	router, repo, mail := newUnsubscribeTestRouter()
	token := signedUnsubscribeToken(t, "user@example.com", service.NotificationEmailEventBalanceLow, time.Now().Add(time.Hour))
	_, err := mail.Unsubscribe(context.Background(), token)
	require.NoError(t, err)
	originalWrites := repo.writes
	res := unsubscribeRequest(router, http.MethodGet, testUnsubscribePath+"?token="+url.QueryEscape(token), "", "")
	require.Equal(t, http.StatusOK, res.Code)
	require.NotContains(t, res.Body.String(), `value="subscribe"`)
	form := url.Values{"token": {token}, "confirm": {"subscribe"}}.Encode()
	res = unsubscribeRequest(router, http.MethodPost, testUnsubscribePath, "application/x-www-form-urlencoded", form)
	require.Equal(t, http.StatusBadRequest, res.Code)
	require.Equal(t, originalWrites, repo.writes)
	unsubscribed, err := mail.IsUnsubscribed(context.Background(), "user@example.com", service.NotificationEmailEventBalanceLow)
	require.NoError(t, err)
	require.True(t, unsubscribed)
}

func TestNotificationEmailUnsubscribeGETWithoutSecretNeverCreatesSettings(t *testing.T) {
	router, repo, _ := newUnsubscribeTestRouter()
	delete(repo.values, "notification_email_unsubscribe_secret")
	token := signedUnsubscribeToken(t, "user@example.com", service.NotificationEmailEventTicketStaffReply, time.Now().Add(time.Hour))
	res := unsubscribeRequest(router, http.MethodGet, testUnsubscribePath+"?token="+url.QueryEscape(token), "", "")
	require.Equal(t, http.StatusBadRequest, res.Code)
	require.Empty(t, repo.values)
	require.Zero(t, repo.writes)
}
