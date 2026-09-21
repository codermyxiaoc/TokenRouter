package handler

import (
	"errors"
	"html"
	"mime"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/handler/dto"
	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/service"

	"github.com/gin-gonic/gin"
)

// SettingHandler 公开设置处理器（无需认证）
type SettingHandler struct {
	settingService           *service.SettingService
	notificationEmailService *service.NotificationEmailService
	version                  string
}

// NewSettingHandler 创建公开设置处理器
func NewSettingHandler(settingService *service.SettingService, version string) *SettingHandler {
	return &SettingHandler{
		settingService: settingService,
		version:        version,
	}
}

// SetNotificationEmailService 注入公开退订入口需要的通知邮件服务，并保持既有构造函数签名不变。
func (h *SettingHandler) SetNotificationEmailService(notificationEmailService *service.NotificationEmailService) {
	h.notificationEmailService = notificationEmailService
}

// GetPublicSettings 获取公开设置
// GET /api/v1/settings/public
func (h *SettingHandler) GetPublicSettings(c *gin.Context) {
	settings, err := h.settingService.GetPublicSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.PublicSettings{
		RegistrationEnabled:                 settings.RegistrationEnabled,
		EmailVerifyEnabled:                  settings.EmailVerifyEnabled,
		ForceEmailOnThirdPartySignup:        settings.ForceEmailOnThirdPartySignup,
		RegistrationEmailSuffixWhitelist:    settings.RegistrationEmailSuffixWhitelist,
		RegistrationEmailDomainQuotaEnabled: settings.RegistrationEmailDomainQuotaEnabled,
		UserEmailChangeEnabled:              settings.UserEmailChangeEnabled,
		PromoCodeEnabled:                    settings.PromoCodeEnabled,
		PasswordResetEnabled:                settings.PasswordResetEnabled,
		InvitationCodeEnabled:               settings.InvitationCodeEnabled,
		TotpEnabled:                         settings.TotpEnabled,
		PasskeyEnabled:                      settings.PasskeyEnabled,
		LoginAgreementEnabled:               settings.LoginAgreementEnabled,
		LoginAgreementMode:                  settings.LoginAgreementMode,
		LoginAgreementUpdatedAt:             settings.LoginAgreementUpdatedAt,
		LoginAgreementRevision:              settings.LoginAgreementRevision,
		LoginAgreementDocuments:             publicLoginAgreementDocumentsToDTO(settings.LoginAgreementDocuments),
		TurnstileEnabled:                    settings.TurnstileEnabled,
		TurnstileSiteKey:                    settings.TurnstileSiteKey,
		TencentCaptchaEnabled:               settings.TencentCaptchaEnabled,
		TencentCaptchaAppID:                 settings.TencentCaptchaAppID,
		TencentCaptchaRegion:                settings.TencentCaptchaRegion,
		AliyunCaptchaEnabled:                settings.AliyunCaptchaEnabled,
		AliyunCaptchaSceneID:                settings.AliyunCaptchaSceneID,
		AliyunCaptchaPrefix:                 settings.AliyunCaptchaPrefix,
		AliyunCaptchaRegion:                 settings.AliyunCaptchaRegion,
		SiteName:                            settings.SiteName,
		SiteLogo:                            settings.SiteLogo,
		SiteSubtitle:                        settings.SiteSubtitle,
		SiteNameZh:                          settings.SiteNameZh,
		SiteNameEn:                          settings.SiteNameEn,
		SiteTitleZh:                         settings.SiteTitleZh,
		SiteTitleEn:                         settings.SiteTitleEn,
		SiteSubtitleZh:                      settings.SiteSubtitleZh,
		SiteSubtitleEn:                      settings.SiteSubtitleEn,
		APIBaseURL:                          settings.APIBaseURL,
		ContactInfo:                         settings.ContactInfo,
		DocURL:                              settings.DocURL,
		HomeContent:                         settings.HomeContent,
		HideCcsImportButton:                 settings.HideCcsImportButton,
		PurchaseSubscriptionEnabled:         settings.PurchaseSubscriptionEnabled,
		PurchaseSubscriptionURL:             settings.PurchaseSubscriptionURL,
		TableDefaultPageSize:                settings.TableDefaultPageSize,
		TablePageSizeOptions:                settings.TablePageSizeOptions,
		UsageRankingLimit:                   settings.UsageRankingLimit,
		UsageRankingEnabled:                 settings.UsageRankingEnabled,
		UsageRankingSortBy:                  settings.UsageRankingSortBy,
		UsageRankingShowTotalTokens:         settings.UsageRankingShowTotalTokens,
		UsageRankingShowRequests:            settings.UsageRankingShowRequests,
		UsageRankingShowActualCost:          settings.UsageRankingShowActualCost,
		PluginManagementEnabled:             settings.PluginManagementEnabled,
		CustomMenuItems:                     dto.ParseUserVisibleMenuItems(settings.CustomMenuItems),
		CustomEndpoints:                     dto.ParseCustomEndpoints(settings.CustomEndpoints),
		FooterLinks:                         dto.ParseFooterLinks(settings.FooterLinks),
		FooterText:                          settings.FooterText,
		HomeFeaturedModels:                  dto.ParseHomeFeaturedModels(settings.HomeFeaturedModels),
		DingTalkOAuthEnabled:                settings.DingTalkOAuthEnabled,
		LinuxDoOAuthEnabled:                 settings.LinuxDoOAuthEnabled,
		WeChatOAuthEnabled:                  settings.WeChatOAuthEnabled,
		WeChatOAuthOpenEnabled:              settings.WeChatOAuthOpenEnabled,
		WeChatOAuthMPEnabled:                settings.WeChatOAuthMPEnabled,
		WeChatOAuthMobileEnabled:            settings.WeChatOAuthMobileEnabled,
		OIDCOAuthEnabled:                    settings.OIDCOAuthEnabled,
		OIDCOAuthProviderName:               settings.OIDCOAuthProviderName,
		GitHubOAuthEnabled:                  settings.GitHubOAuthEnabled,
		GoogleOAuthEnabled:                  settings.GoogleOAuthEnabled,
		GoogleOneTapEnabled:                 settings.GoogleOneTapEnabled,
		GoogleOAuthClientID:                 settings.GoogleOAuthClientID,
		BackendModeEnabled:                  settings.BackendModeEnabled,
		PaymentEnabled:                      settings.PaymentEnabled,
		TeamEnabled:                         settings.TeamEnabled,
		TeamSelfServiceEnabled:              settings.TeamSelfServiceEnabled,
		CreativeEnabled:                     settings.CreativeEnabled,
		TicketEnabled:                       settings.TicketEnabled,
		Version:                             h.version,
		ServerTimezone:                      timezone.Name(),
		ServerUTCOffset:                     timezone.UTCOffset(),
		BalanceUnitName:                     settings.BalanceUnitName,
		BalanceUnitSymbol:                   settings.BalanceUnitSymbol,
		BalanceIconSVG:                      settings.BalanceIconSVG,
		BalanceLowNotifyEnabled:             settings.BalanceLowNotifyEnabled,
		AccountQuotaNotifyEnabled:           settings.AccountQuotaNotifyEnabled,
		RiskControlEnabled:                  settings.RiskControlEnabled,
		AffiliateEnabled:                    settings.AffiliateEnabled,
		BalanceLowNotifyThreshold:           settings.BalanceLowNotifyThreshold,
		BalanceLowNotifyRechargeURL:         settings.BalanceLowNotifyRechargeURL,
		AllowUserViewErrorRequests:          settings.AllowUserViewErrorRequests,
	})
}

const notificationEmailUnsubscribeTokenLimit = 4096

// UnsubscribeNotificationEmail 只展示确认页，邮件客户端预取链接不能改变通知偏好。
// GET /api/v1/settings/email-unsubscribe?token=...
func (h *SettingHandler) UnsubscribeNotificationEmail(c *gin.Context) {
	setNotificationEmailUnsubscribeHeaders(c)
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	token := strings.TrimSpace(c.Query("token"))
	if token == "" || len(token) > notificationEmailUnsubscribeTokenLimit || len(c.Request.URL.Query()["token"]) != 1 {
		response.BadRequest(c, "a valid unsubscribe token is required")
		return
	}
	result, err := h.notificationEmailService.PreviewUnsubscribe(c.Request.Context(), token)
	if err != nil {
		response.BadRequest(c, "unsubscribe link is invalid or expired")
		return
	}
	unsubscribed, err := h.notificationEmailService.IsUnsubscribed(c.Request.Context(), result.Email, result.Event)
	if err != nil {
		response.InternalError(c, "notification email preference is unavailable")
		return
	}
	if unsubscribed {
		body := "<h1>已退订 / Unsubscribed</h1><p><strong>" + html.EscapeString(result.Email) + "</strong> 已停止接收此类邮件。These email notifications are currently disabled.</p>"
		if isTicketNotificationEvent(result.Event) {
			// 旧链接可用于主动恢复工单通知，读取链接本身始终不改变用户选择。
			body += "<p>如需继续接收客服回复、完成和撤销工单的邮件，请点击下方按钮。Restore emails about staff replies and ticket closure by confirming below.</p>" + notificationEmailPreferenceForm(token, "subscribe", "恢复工单邮件通知 / Restore ticket emails")
		}
		writeNotificationEmailUnsubscribePage(c, "邮件通知设置 / Email preferences", body)
		return
	}
	// 保留旧邮件的签名令牌；只由用户点击按钮提交正文，不在后续表单 URL 中传播令牌。
	body := "<h1>确认退订 / Confirm unsubscribe</h1><p>当前尚未退订。点击下方按钮后才会停止此类邮件通知。</p><p>Confirm that <strong>" + html.EscapeString(result.Email) + "</strong> should stop receiving <strong>" + html.EscapeString(notificationEmailPreferenceLabel(result.Event)) + "</strong>.</p>" +
		notificationEmailPreferenceForm(token, "unsubscribe", "确认退订 / Unsubscribe")
	writeNotificationEmailUnsubscribePage(c, "确认退订 / Confirm unsubscribe", body)
}

// ConfirmNotificationEmailUnsubscribe 仅接受有大小限制的显式确认表单，不从查询参数读取令牌。
// POST /api/v1/settings/email-unsubscribe
func (h *SettingHandler) ConfirmNotificationEmailUnsubscribe(c *gin.Context) {
	setNotificationEmailUnsubscribeHeaders(c)
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		response.Error(c, http.StatusUnsupportedMediaType, "unsubscribe confirmation requires a form submission")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8*1024)
	if err := c.Request.ParseForm(); err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			response.Error(c, http.StatusRequestEntityTooLarge, "unsubscribe confirmation is too large")
		} else {
			response.BadRequest(c, "invalid unsubscribe confirmation form")
		}
		return
	}
	form := c.Request.PostForm
	token := strings.TrimSpace(form.Get("token"))
	confirmation := form.Get("confirm")
	if len(form["token"]) != 1 || token == "" || len(token) > notificationEmailUnsubscribeTokenLimit || len(form["confirm"]) != 1 || (confirmation != "unsubscribe" && confirmation != "subscribe") {
		response.BadRequest(c, "an explicit unsubscribe confirmation is required")
		return
	}
	var result service.NotificationEmailUnsubscribeResult
	if confirmation == "subscribe" {
		result, err = h.notificationEmailService.ResubscribeTicketNotifications(c.Request.Context(), token)
	} else {
		result, err = h.notificationEmailService.Unsubscribe(c.Request.Context(), token)
	}
	if err != nil {
		response.BadRequest(c, "unsubscribe link is invalid or expired")
		return
	}
	if confirmation == "subscribe" {
		body := "<h1>已恢复工单邮件通知 / Ticket emails restored</h1><p><strong>" + html.EscapeString(result.Email) + "</strong> 将按站点工单通知设置接收后续客服回复及结单邮件。Future staff replies and ticket closure emails will follow the site's notification settings.</p>"
		writeNotificationEmailUnsubscribePage(c, "已恢复工单邮件通知 / Ticket emails restored", body)
		return
	}
	body := "<h1>已退订 / Unsubscribed</h1><p>已停止此类邮件通知。You have unsubscribed <strong>" + html.EscapeString(result.Email) + "</strong> from <strong>" + html.EscapeString(notificationEmailPreferenceLabel(result.Event)) + "</strong>.</p>"
	writeNotificationEmailUnsubscribePage(c, "已退订 / Unsubscribed", body)
}

// isTicketNotificationEvent 将公开恢复入口限定为工单通知，不扩展其他通知类别的权限。
func isTicketNotificationEvent(event string) bool {
	switch event {
	case service.NotificationEmailEventTicketStaffReply, service.NotificationEmailEventTicketCompleted, service.NotificationEmailEventTicketCancelled:
		return true
	default:
		return false
	}
}

// notificationEmailPreferenceLabel 明确工单共享偏好的范围，避免误认为只退订某一种结单邮件。
func notificationEmailPreferenceLabel(event string) string {
	if isTicketNotificationEvent(event) {
		return "工单邮件通知（客服回复、完成与撤销） / Ticket emails (staff replies and closure)"
	}
	return event + " emails"
}

// notificationEmailPreferenceForm 使用相对路径兼容反向代理前缀，并将令牌限制在表单正文。
func notificationEmailPreferenceForm(token, confirmation, label string) string {
	return `<form method="post" action="email-unsubscribe"><input type="hidden" name="token" value="` + html.EscapeString(token) + `"><button type="submit" name="confirm" value="` + html.EscapeString(confirmation) + `">` + html.EscapeString(label) + `</button></form>`
}

// setNotificationEmailUnsubscribeHeaders 防止令牌页面被缓存、嵌入或通过来源头泄漏。
func setNotificationEmailUnsubscribeHeaders(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
}

// writeNotificationEmailUnsubscribePage 仅接收固定结构和已转义文本，不加载外部资源或脚本。
func writeNotificationEmailUnsubscribePage(c *gin.Context, title, body string) {
	page := `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>` + html.EscapeString(title) + `</title></head><body style="font-family:system-ui,sans-serif;padding:24px;overflow-wrap:anywhere">` + body + "</body></html>"
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(page))
}

func publicLoginAgreementDocumentsToDTO(items []service.LoginAgreementDocument) []dto.LoginAgreementDocument {
	result := make([]dto.LoginAgreementDocument, 0, len(items))
	for _, item := range items {
		result = append(result, dto.LoginAgreementDocument{
			ID:        item.ID,
			Title:     item.Title,
			ContentMD: item.ContentMD,
		})
	}
	return result
}
