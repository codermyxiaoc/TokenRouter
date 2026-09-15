package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 重放回复不进入队列，通知去重使用准确的消息编号。
func TestTicketRuntimeQueuesOnlyNewReplies(t *testing.T) {
	r := NewTicketRuntime(nil, nil, nil, nil)
	defer r.Stop()
	r.NotifyReply(&Ticket{ID: 1, ReplyCreated: false, ReplyMessageID: 9})
	require.Empty(t, r.queue)
	r.NotifyReply(&Ticket{ID: 1, UserID: 2, ReplyCreated: true, ReplyMessageID: 9, Title: "测试"})
	job := <-r.queue
	require.Equal(t, int64(9), job.messageID)
	require.Equal(t, int64(1), job.ticketID)
}

func TestTicketReplyEmailTemplatesEscapeUserInput(t *testing.T) {
	s := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	for _, locale := range []string{"zh", "en"} {
		preview, err := s.PreviewTemplate(context.Background(), NotificationEmailPreviewInput{Event: NotificationEmailEventTicketStaffReply, Locale: locale, Variables: map[string]string{"ticket_id": "42", "ticket_title": "<script>alert(1)</script>", "ticket_url": "https://example.com/tickets/42"}})
		require.NoError(t, err)
		require.Contains(t, preview.Subject, "42")
		require.Contains(t, preview.HTML, "&lt;script&gt;")
		require.NotContains(t, preview.HTML, "<script>")
	}
}

func TestTicketRuntimeQueueRemainsBoundedAndStops(t *testing.T) {
	r := NewTicketRuntime(nil, nil, nil, nil)
	for i := 0; i <= cap(r.queue); i++ {
		r.NotifyReply(&Ticket{ID: 1, UserID: 2, ReplyCreated: true, ReplyMessageID: int64(i + 1)})
	}
	require.Len(t, r.queue, cap(r.queue))
	r.Stop()
	for len(r.queue) > 0 {
		<-r.queue
	}
	r.NotifyReply(&Ticket{ID: 1, UserID: 2, ReplyCreated: true, ReplyMessageID: 1000})
	require.Empty(t, r.queue)
	r.Stop()
}

func TestTicketRuntimeDisabledMailAndWorkerShutdown(t *testing.T) {
	cfg := NewTicketConfigService(&ticketConfigRepoStub{})
	r := NewTicketRuntime(nil, cfg, nil, nil)
	// 默认关闭通知时不能调用未配置的邮件服务。
	require.NotPanics(t, func() { r.send(ticketNotification{ticketID: 1, messageID: 2}) })
	r.Start()
	r.Start()
	r.Stop()
	require.ErrorIs(t, r.ctx.Err(), context.Canceled)
}

// 邮件通知子开关开启时，总开关仍应阻止邮件服务被调用。
func TestTicketRuntimeDisabledModuleSkipsQueuedMail(t *testing.T) {
	cfg := NewTicketConfigService(&ticketConfigRepoStub{values: map[string]string{
		SettingKeyTicketEnabled: "false", SettingKeyTicketNotifyOnStaffReply: "true",
	}})
	r := NewTicketRuntime(nil, cfg, nil, nil)
	defer r.Stop()
	require.NotPanics(t, func() { r.send(ticketNotification{ticketID: 1, messageID: 2}) })
}

func TestTicketRuntimeNotificationURL(t *testing.T) {
	for _, tt := range []struct{ base, want string }{
		{"https://example.com", "https://example.com/tickets/42"},
		{"https://example.com/app/?utm_source=mail#home", "https://example.com/app/tickets/42"},
		{"https://example.com/path%20name/", "https://example.com/path%20name/tickets/42"},
		{"javascript:alert(1)", ""},
		{"https://secret@example.com", ""},
		{"/relative", ""},
	} {
		require.Equal(t, tt.want, ticketNotificationURL(tt.base, 42))
	}
}

// 连续客服回复都走独立消息标识，随后完成或撤销也通过同一开关发对应邮件。
func TestTicketRuntimeSendsEveryReplyAndStaffClosure(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	smtpServer := startNotificationEmailTestSMTPServer(t)
	require.NoError(t, repo.SetMultiple(ctx, smtpServer.settings()))
	require.NoError(t, repo.Set(ctx, SettingKeyTicketNotifyOnStaffReply, "true"))
	mail := NewNotificationEmailService(repo, NewEmailService(repo, nil))
	r := NewTicketRuntime(nil, NewTicketConfigService(repo), mail, nil)
	defer r.Stop()
	ticket := &Ticket{ID: 42, UserID: 7, UserEmail: "ticket-owner@example.com", Title: "连续回复测试", ReplyCreated: true}
	for messageID := int64(101); messageID <= 103; messageID++ {
		ticket.ReplyMessageID = messageID
		r.NotifyReply(ticket)
		job := <-r.queue
		require.Equal(t, NotificationEmailEventTicketStaffReply, job.event)
		r.send(job)
	}
	require.EqualValues(t, 3, smtpServer.messageCount())
	// 即使队列意外重放同一消息，投递幂等仍只跳过这一条，不影响后续事件。
	r.NotifyReply(ticket)
	r.send(<-r.queue)
	require.EqualValues(t, 3, smtpServer.messageCount())
	ticket.ClosureCreated, ticket.ClosedByRole = true, "admin"
	ticket.Status = TicketStatusCompleted
	r.NotifyClosure(ticket)
	job := <-r.queue
	require.Equal(t, NotificationEmailEventTicketCompleted, job.event)
	r.send(job)
	require.EqualValues(t, 4, smtpServer.messageCount())
	require.Contains(t, smtpServer.lastMessageBody(t), "completed")
	ticket.ID, ticket.Status = 43, TicketStatusCancelled
	r.NotifyClosure(ticket)
	job = <-r.queue
	require.Equal(t, NotificationEmailEventTicketCancelled, job.event)
	r.send(job)
	require.EqualValues(t, 5, smtpServer.messageCount())
	require.Contains(t, smtpServer.lastMessageBody(t), "cancelled")
	// 开关热关闭后，尚未发送的客服通知同样被跳过。
	require.NoError(t, repo.Set(ctx, SettingKeyTicketNotifyOnStaffReply, "false"))
	ticket.ID = 44
	r.NotifyClosure(ticket)
	r.send(<-r.queue)
	require.EqualValues(t, 5, smtpServer.messageCount())
}

// 用户自助结束、系统过期以及首次提交之外的重试都不能生成客服结单邮件。
func TestTicketRuntimeQueuesOnlyNewStaffClosures(t *testing.T) {
	r := NewTicketRuntime(nil, nil, nil, nil)
	defer r.Stop()
	for _, ticket := range []*Ticket{
		nil,
		{ID: 1, ClosureCreated: false, ClosedByRole: "admin", Status: TicketStatusCompleted},
		{ID: 1, ClosureCreated: true, ClosedByRole: "user", Status: TicketStatusCompleted},
		{ID: 1, ClosureCreated: true, ClosedByRole: "user", Status: TicketStatusCancelled},
		{ID: 1, ClosureCreated: true, ClosedByRole: "system", Status: TicketStatusExpired},
		{ID: 1, ClosureCreated: true, ClosedByRole: "admin", Status: TicketStatusWaitingUser},
	} {
		r.NotifyClosure(ticket)
		require.Empty(t, r.queue)
	}
}
