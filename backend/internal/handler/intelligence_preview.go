package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

const intelligencePreviewTTL = 5 * time.Minute
const intelligencePreviewCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none'; frame-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox allow-scripts"

type intelligencePreviewAccess struct {
	secret  []byte
	users   service.UserRepository
	enabled func(context.Context) bool
}

// 票据仅授权一份已保存作品，不授予面板会话，也不能用于模型调用。
type intelligencePreviewClaims struct {
	RunID   string `json:"run"`
	UserID  int64  `json:"user"`
	Admin   bool   `json:"admin"`
	Expires int64  `json:"expires"`
}

// ProvideIntelligenceHandler 以既有固定 JWT 密钥做独立域签名，支持多实例和重启。
func ProvideIntelligenceHandler(svc *service.IntelligenceService, cfg *config.Config, users service.UserRepository, settings *service.SettingService) *IntelligenceHandler {
	h := NewIntelligenceHandler(svc)
	if cfg != nil && cfg.JWT.Secret != "" {
		h.preview = &intelligencePreviewAccess{secret: []byte(cfg.JWT.Secret), users: users, enabled: settings.IsIntelligenceEnabled}
	}
	return h
}

func signIntelligencePreview(secret []byte, claims intelligencePreviewClaims) string {
	data, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("intelligence-preview-v1:" + payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func verifyIntelligencePreview(secret []byte, ticket string, now time.Time) (intelligencePreviewClaims, error) {
	var claims intelligencePreviewClaims
	invalid := errors.New("invalid or expired preview ticket")
	if len(secret) == 0 || len(ticket) > 1024 {
		return claims, invalid
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 2 {
		return claims, invalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, invalid
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("intelligence-preview-v1:" + parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return claims, invalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(payload, &claims) != nil || claims.RunID == "" || claims.UserID <= 0 || claims.Expires <= now.Unix() || claims.Expires > now.Add(intelligencePreviewTTL+time.Second).Unix() {
		return intelligencePreviewClaims{}, invalid
	}
	return claims, nil
}

func (h *IntelligenceHandler) UserPreview(c *gin.Context)  { h.issuePreview(c, false) }
func (h *IntelligenceHandler) AdminPreview(c *gin.Context) { h.issuePreview(c, true) }

func (h *IntelligenceHandler) previewRun(ctx context.Context, claims intelligencePreviewClaims) (*service.IntelligenceRun, error) {
	if claims.Admin {
		return h.svc.AdminDetail(ctx, claims.RunID)
	}
	return h.svc.UserDetail(ctx, claims.UserID, claims.RunID)
}

func (h *IntelligenceHandler) issuePreview(c *gin.Context, admin bool) {
	userID, ok := intelligenceActor(c, admin)
	if !ok {
		return
	}
	if h.preview == nil || !h.preview.enabled(c.Request.Context()) {
		response.Forbidden(c, "Intelligence preview is disabled")
		return
	}
	claims := intelligencePreviewClaims{RunID: c.Param("id"), UserID: userID, Admin: admin, Expires: time.Now().Add(intelligencePreviewTTL).Unix()}
	run, err := h.previewRun(c.Request.Context(), claims)
	if response.ErrorFrom(c, err) {
		return
	}
	if run == nil || run.Benchmark != "drawing" || run.HTML == "" {
		response.NotFound(c, "Drawing is unavailable")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, gin.H{"url": "/api/v1/intelligence-tests/preview-content/" + signIntelligencePreview(h.preview.secret, claims), "expires_at": time.Unix(claims.Expires, 0).UTC()})
}

// PreviewContent 使用独立响应策略；不继承主站 nonce 限制，也不给模型作品同源权限。
// @project-doc docs/domains/intelligence_tests.md#intelligence_preview
func (h *IntelligenceHandler) PreviewContent(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	if h.preview == nil || h.preview.users == nil || !h.preview.enabled(c.Request.Context()) {
		response.NotFound(c, "Preview unavailable")
		return
	}
	claims, err := verifyIntelligencePreview(h.preview.secret, c.Param("ticket"), time.Now())
	if err != nil {
		response.NotFound(c, "Preview unavailable")
		return
	}
	// 失效账号、降级管理员或撤销分组授权后，未到期票据也立即失效。
	user, err := h.preview.users.GetByID(c.Request.Context(), claims.UserID)
	if err != nil || user == nil || !user.IsActive() || (claims.Admin && !user.IsAdmin()) {
		response.NotFound(c, "Preview unavailable")
		return
	}
	run, err := h.previewRun(c.Request.Context(), claims)
	if err != nil || run == nil || run.Benchmark != "drawing" || run.HTML == "" {
		response.NotFound(c, "Preview unavailable")
		return
	}
	c.Header("Content-Security-Policy", intelligencePreviewCSP)
	c.Header("X-Frame-Options", "SAMEORIGIN")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", "inline")
	c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(run.HTML))
}
