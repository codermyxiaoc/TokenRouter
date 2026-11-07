package handler

import (
	"context"
	"net/http"
	"net/url"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// oauthLoginTotpChallenge 在第三方登录（OAuth、OIDC、Google One Tap 等）即将交付凭证前检查两步验证。
// 用户已开启 TOTP 时作废刚签发的凭证并创建 2FA 临时会话，由客户端提交验证码后经 /auth/login/2fa 换取凭证；
// 未开启 TOTP 的用户返回 nil，原有登录流程不变。
func (h *AuthHandler) oauthLoginTotpChallenge(ctx context.Context, user *service.User, issued *service.TokenPair) (*TotpLoginResponse, error) {
	if h == nil || h.totpService == nil || h.settingSvc == nil || user == nil || !user.TotpEnabled || !h.settingSvc.IsTotpEnabled(ctx) {
		return nil, nil
	}
	if issued != nil && h.authService != nil {
		h.authService.DiscardTokenPair(ctx, issued)
	}
	tempToken, err := h.totpService.CreateLoginSession(ctx, user.ID, user.Email)
	if err != nil {
		return nil, err
	}
	return &TotpLoginResponse{Requires2FA: true, TempToken: tempToken, UserEmailMasked: service.MaskEmail(user.Email)}, nil
}

// redirectOAuthTotpChallenge 以 URL 片段把 2FA 临时会话交给前端回调页（片段不会发送到服务器）。
func redirectOAuthTotpChallenge(c *gin.Context, frontendCallback string, challenge *TotpLoginResponse, redirectTo string) {
	fragment := url.Values{}
	fragment.Set("requires_2fa", "1")
	fragment.Set("temp_token", challenge.TempToken)
	fragment.Set("user_email_masked", challenge.UserEmailMasked)
	if redirectTo != "" {
		fragment.Set("redirect", redirectTo)
	}
	redirectWithFragment(c, frontendCallback, fragment)
}

// writeOAuthTotpChallenge 以与令牌响应相同的裸 JSON 形态返回 2FA 挑战。
func writeOAuthTotpChallenge(c *gin.Context, challenge *TotpLoginResponse) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, challenge)
}
