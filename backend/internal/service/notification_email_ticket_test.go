package service

import (
	"context"
	"mime"
	"net/mail"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 同一工单的不同客服消息必须分别到达 SMTP；相同消息重试不重复发送。
func TestNotificationEmailTicketRepliesSendEachMessageOnce(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	smtpServer := startNotificationEmailTestSMTPServer(t)
	require.NoError(t, repo.SetMultiple(ctx, smtpServer.settings()))
	svc := NewNotificationEmailService(repo, NewEmailService(repo, nil))
	messageIDs := make(map[string]bool)
	for i, id := range []string{"101", "102", "103"} {
		input := NotificationEmailSendInput{
			Event: NotificationEmailEventTicketStaffReply, RecipientEmail: "user@example.com", UserID: 7,
			SourceType: "support_ticket_message", SourceID: id,
			Variables: map[string]string{"ticket_id": "10", "ticket_title": "连续回复", "ticket_url": "https://example.com/tickets/10"},
		}
		require.NoError(t, svc.Send(ctx, input))
		require.EqualValues(t, i+1, smtpServer.messageCount())
		message, err := mail.ReadMessage(strings.NewReader(smtpServer.lastMessage()))
		require.NoError(t, err)
		messageID := message.Header.Get("Message-ID")
		require.NotEmpty(t, messageID)
		require.False(t, messageIDs[messageID], "不同消息不能复用SMTP Message-ID")
		messageIDs[messageID] = true
		require.NoError(t, svc.Send(ctx, input))
		require.EqualValues(t, i+1, smtpServer.messageCount(), "同消息重试必须去重")
	}
}

// 完成和撤销拥有独立模板及去重事件，同一工单的新结束事件不会被回复邮件去重。
func TestNotificationEmailTicketClosureTemplatesReachSMTP(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	smtpServer := startNotificationEmailTestSMTPServer(t)
	require.NoError(t, repo.SetMultiple(ctx, smtpServer.settings()))
	svc := NewNotificationEmailService(repo, NewEmailService(repo, nil))
	count := int64(0)
	for _, locale := range []string{"en", "zh"} {
		id := "201"
		if locale == "zh" {
			id = "202"
		}
		for _, tc := range []struct{ event, en, zh string }{
			{NotificationEmailEventTicketCompleted, "completed", "已完成"},
			{NotificationEmailEventTicketCancelled, "cancelled", "已撤销"},
		} {
			t.Run(locale+"/"+tc.event, func(t *testing.T) {
				info, _, err := svc.eventInfo(tc.event)
				require.NoError(t, err)
				require.True(t, info.Optional)
				input := NotificationEmailSendInput{
					Event: tc.event, Locale: locale, RecipientEmail: "user@example.com", RecipientName: "用户", UserID: 7,
					SourceType: "support_ticket", SourceID: id,
					Variables: map[string]string{"ticket_id": id, "ticket_title": "订单 <script>alert(1)</script>", "ticket_url": "https://example.com/tickets/" + id},
				}
				require.NoError(t, svc.Send(ctx, input))
				count++
				require.Equal(t, count, smtpServer.messageCount())
				message, err := mail.ReadMessage(strings.NewReader(smtpServer.lastMessage()))
				require.NoError(t, err)
				subject, err := (&mime.WordDecoder{}).DecodeHeader(message.Header.Get("Subject"))
				require.NoError(t, err)
				want := tc.en
				if locale == "zh" {
					want = tc.zh
				}
				require.Contains(t, subject, want)
				body := smtpServer.lastMessageBody(t)
				require.Contains(t, body, want)
				require.Contains(t, body, "https://example.com/tickets/"+id)
				require.Contains(t, body, "&lt;script&gt;")
				require.NotContains(t, body, "<script>")
				require.NotContains(t, body, "{{")
				require.NoError(t, svc.Send(ctx, input))
				require.Equal(t, count, smtpServer.messageCount())
			})
		}
	}
}

// 老工单退订必须覆盖三个事件；从任意新事件退订也统一停止整个工单邮件类别。
func TestNotificationEmailTicketEventsShareUnsubscribePreference(t *testing.T) {
	ctx := context.Background()
	events := []string{NotificationEmailEventTicketStaffReply, NotificationEmailEventTicketCompleted, NotificationEmailEventTicketCancelled}
	for _, preferenceKind := range []string{"legacy", "current", "completed", "cancelled"} {
		t.Run(preferenceKind, func(t *testing.T) {
			repo := newNotificationEmailMemorySettingRepo()
			smtpServer := startNotificationEmailTestSMTPServer(t)
			require.NoError(t, repo.SetMultiple(ctx, smtpServer.settings()))
			svc := NewNotificationEmailService(repo, NewEmailService(repo, nil))
			switch preferenceKind {
			case "legacy":
				require.NoError(t, repo.Set(ctx, legacyNotificationEmailPreferenceKey(NotificationEmailEventTicketStaffReply, "user@example.com"), "unsubscribed"))
			case "current":
				require.NoError(t, repo.Set(ctx, notificationEmailPreferenceKey(NotificationEmailEventTicketStaffReply, "user@example.com"), "unsubscribed"))
			default:
				event := NotificationEmailEventTicketCompleted
				if preferenceKind == "cancelled" {
					event = NotificationEmailEventTicketCancelled
				}
				token, err := svc.createUnsubscribeToken(ctx, "user@example.com", event)
				require.NoError(t, err)
				result, err := svc.Unsubscribe(ctx, token)
				require.NoError(t, err)
				require.True(t, result.Done)
			}
			for _, event := range events {
				unsubscribed, err := svc.IsUnsubscribed(ctx, "USER@example.com", event)
				require.NoError(t, err)
				require.True(t, unsubscribed)
				require.NoError(t, svc.Send(ctx, NotificationEmailSendInput{Event: event, RecipientEmail: "user@example.com", SourceType: "support_ticket", SourceID: "10"}))
			}
			require.Zero(t, smtpServer.messageCount())
			unsubscribed, err := svc.IsUnsubscribed(ctx, "user@example.com", NotificationEmailEventBalanceLow)
			require.NoError(t, err)
			require.False(t, unsubscribed, "工单退订不能影响无关邮件类别")
		})
	}
}

// 预览退订令牌不能产生偏好或密钥写入，避免邮件安全扫描使后续回复被静默拦截。
func TestNotificationEmailPreviewUnsubscribeIsReadOnly(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)
	_, err := svc.PreviewUnsubscribe(ctx, "payload.signature")
	require.Error(t, err)
	settings, err := repo.GetAll(ctx)
	require.NoError(t, err)
	require.Empty(t, settings)
	token, err := svc.createUnsubscribeToken(ctx, "user@example.com", NotificationEmailEventTicketCompleted)
	require.NoError(t, err)
	before, err := repo.GetAll(ctx)
	require.NoError(t, err)
	preview, err := svc.PreviewUnsubscribe(ctx, token)
	require.NoError(t, err)
	require.False(t, preview.Done)
	require.Equal(t, NotificationEmailEventTicketCompleted, preview.Event)
	after, err := repo.GetAll(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
	for _, event := range []string{NotificationEmailEventTicketStaffReply, NotificationEmailEventTicketCompleted, NotificationEmailEventTicketCancelled} {
		unsubscribed, err := svc.IsUnsubscribed(ctx, "user@example.com", event)
		require.NoError(t, err)
		require.False(t, unsubscribed)
	}
}

// 旧邮件的有效令牌可显式恢复整个工单类别，读取确认页和其他通知令牌都不能恢复。
func TestNotificationEmailTicketResubscribeRequiresExplicitTicketAction(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	smtpServer := startNotificationEmailTestSMTPServer(t)
	require.NoError(t, repo.SetMultiple(ctx, smtpServer.settings()))
	svc := NewNotificationEmailService(repo, NewEmailService(repo, nil))
	require.NoError(t, repo.Set(ctx, legacyNotificationEmailPreferenceKey(NotificationEmailEventTicketStaffReply, "user@example.com"), "unsubscribed"))
	token, err := svc.createUnsubscribeToken(ctx, "user@example.com", NotificationEmailEventTicketStaffReply)
	require.NoError(t, err)
	_, err = svc.PreviewUnsubscribe(ctx, token)
	require.NoError(t, err)
	unsubscribed, err := svc.IsUnsubscribed(ctx, "user@example.com", NotificationEmailEventTicketCompleted)
	require.NoError(t, err)
	require.True(t, unsubscribed)
	otherToken, err := svc.createUnsubscribeToken(ctx, "user@example.com", NotificationEmailEventBalanceLow)
	require.NoError(t, err)
	_, err = svc.ResubscribeTicketNotifications(ctx, otherToken)
	require.Error(t, err)
	_, err = svc.ResubscribeTicketNotifications(ctx, "tampered.token")
	require.Error(t, err)
	result, err := svc.ResubscribeTicketNotifications(ctx, token)
	require.NoError(t, err)
	require.True(t, result.Done)
	for i, event := range []string{NotificationEmailEventTicketStaffReply, NotificationEmailEventTicketCompleted, NotificationEmailEventTicketCancelled} {
		unsubscribed, err = svc.IsUnsubscribed(ctx, "user@example.com", event)
		require.NoError(t, err)
		require.False(t, unsubscribed)
		require.NoError(t, svc.Send(ctx, NotificationEmailSendInput{Event: event, RecipientEmail: "user@example.com", SourceType: "support_ticket", SourceID: "10", Variables: map[string]string{"ticket_id": "10", "ticket_title": "恢复测试", "ticket_url": "https://example.com/tickets/10"}}))
		require.EqualValues(t, i+1, smtpServer.messageCount())
	}
}

func TestNotificationEmailTicketPreviewSamplesDoNotLeakIntoDelivery(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	for _, event := range []string{NotificationEmailEventTicketStaffReply, NotificationEmailEventTicketCompleted, NotificationEmailEventTicketCancelled} {
		preview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{Event: event, Locale: "zh"})
		require.NoError(t, err)
		require.Contains(t, preview.HTML, "接口使用咨询")
		variables := svc.runtimeVariables(ctx, event, "zh", NotificationEmailSendInput{RecipientEmail: "user@example.com"})
		for _, key := range []string{"ticket_id", "ticket_title", "ticket_url"} {
			require.Empty(t, variables[key])
		}
	}
}
