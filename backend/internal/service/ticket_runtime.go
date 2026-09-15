package service

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TicketRuntime 托管到期扫描和有界通知队列，随应用关闭取消后台工作。
// @project-doc docs/domains/support_tickets.md#ticket_configuration
type TicketRuntime struct {
	tickets   *TicketService
	config    *TicketConfigService
	mail      *NotificationEmailService
	settings  *SettingService
	queue     chan ticketNotification
	ctx       context.Context
	cancel    context.CancelFunc
	startOnce sync.Once
	wg        sync.WaitGroup
}

type ticketNotification struct {
	ticketID, messageID, userID int64
	title, email, name          string
	event                       string
}

func NewTicketRuntime(tickets *TicketService, config *TicketConfigService, mail *NotificationEmailService, settings *SettingService) *TicketRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &TicketRuntime{tickets: tickets, config: config, mail: mail, settings: settings, queue: make(chan ticketNotification, 100), ctx: ctx, cancel: cancel}
}

// Start 各自运行扫描和邮件，SMTP延迟不阻塞工单回复或过期扫描。
func (r *TicketRuntime) Start() {
	r.startOnce.Do(func() {
		r.wg.Add(2)
		go func() {
			defer r.wg.Done()
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-r.ctx.Done():
					return
				case <-ticker.C:
					r.expire()
				}
			}
		}()
		go func() {
			defer r.wg.Done()
			for {
				select {
				case <-r.ctx.Done():
					return
				case job := <-r.queue:
					if r.ctx.Err() != nil {
						return
					}
					r.send(job)
				}
			}
		}()
	})
}

func (r *TicketRuntime) Stop() {
	if r != nil {
		r.cancel()
		r.wg.Wait()
	}
}

func (r *TicketRuntime) expire() {
	ctx, cancel := context.WithTimeout(r.ctx, 20*time.Second)
	defer cancel()
	count, err := r.tickets.Expire(ctx)
	if err != nil && r.ctx.Err() == nil {
		slog.Error("ticket expiry scan failed", "error", err)
	} else if count > 0 {
		slog.Info("waiting tickets expired", "count", count)
	}
}

// NotifyReply 只排入本次新提交的回复；队列满或关闭也不能回滚已保存的消息。
func (r *TicketRuntime) NotifyReply(ticket *Ticket) {
	if ticket == nil || !ticket.ReplyCreated || ticket.ReplyMessageID <= 0 {
		return
	}
	r.enqueue(ticketNotification{ticketID: ticket.ID, messageID: ticket.ReplyMessageID, userID: ticket.UserID, title: ticket.Title, email: ticket.UserEmail, name: ticket.UserName, event: NotificationEmailEventTicketStaffReply})
}

// NotifyClosure 复用工单邮件开关，只通知客服本次新提交的完成或撤销操作。
func (r *TicketRuntime) NotifyClosure(ticket *Ticket) {
	if ticket == nil || !ticket.ClosureCreated || ticket.ClosedByRole != "admin" {
		return
	}
	var event string
	switch ticket.Status {
	case TicketStatusCompleted:
		event = NotificationEmailEventTicketCompleted
	case TicketStatusCancelled:
		event = NotificationEmailEventTicketCancelled
	default:
		return
	}
	r.enqueue(ticketNotification{ticketID: ticket.ID, userID: ticket.UserID, title: ticket.Title, email: ticket.UserEmail, name: ticket.UserName, event: event})
}

// enqueue 不阻塞已提交的工单操作，记录足够的事件信息以排查通知丢弃。
func (r *TicketRuntime) enqueue(job ticketNotification) {
	select {
	case <-r.ctx.Done():
		slog.Warn("ticket notification skipped during shutdown", "ticket_id", job.ticketID, "message_id", job.messageID, "event", job.event)
		return
	default:
	}
	select {
	case r.queue <- job:
	default:
		slog.Error("ticket notification queue full", "ticket_id", job.ticketID, "message_id", job.messageID, "event", job.event)
	}
}

func (r *TicketRuntime) send(job ticketNotification) {
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	cfg, err := r.config.Get(ctx)
	if err != nil {
		slog.Error("ticket notification settings failed", "ticket_id", job.ticketID, "message_id", job.messageID, "event", job.event, "error", err)
		return
	}
	// 总开关关闭后丢弃尚未发送的通知，避免发送无法访问的工单链接。
	if !cfg.Enabled || !cfg.NotifyOnStaffReply {
		slog.Info("ticket notification disabled by settings", "ticket_id", job.ticketID, "message_id", job.messageID, "event", job.event)
		return
	}
	// 回复逐条消息去重，结单按工单和事件去重，两者不能共用消息或工单级发送记录。
	sourceType, sourceID := "support_ticket", strconv.FormatInt(job.ticketID, 10)
	if job.event == NotificationEmailEventTicketStaffReply {
		sourceType, sourceID = "support_ticket_message", strconv.FormatInt(job.messageID, 10)
	}
	link := ""
	if r.settings != nil {
		link = ticketNotificationURL(r.settings.GetFrontendURL(ctx), job.ticketID)
	}
	// 未配置站点地址仍可发送编号通知，用户从站点工单入口查看。
	err = r.mail.Send(ctx, NotificationEmailSendInput{Event: job.event, RecipientEmail: job.email, RecipientName: job.name, UserID: job.userID, SourceType: sourceType, SourceID: sourceID, Variables: map[string]string{"ticket_id": strconv.FormatInt(job.ticketID, 10), "ticket_title": job.title, "ticket_url": link}})
	if err != nil {
		slog.Error("ticket notification failed", "ticket_id", job.ticketID, "message_id", job.messageID, "event", job.event, "error", err)
	}
}

// ticketNotificationURL 按 URL 路径拼接工单地址，避免站点的查询参数或锚点吞掉目标路由。
func ticketNotificationURL(base string, ticketID int64) string {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/tickets/" + strconv.FormatInt(ticketID, 10)
	parsed.RawPath, parsed.RawQuery, parsed.Fragment, parsed.RawFragment = "", "", "", ""
	parsed.ForceQuery = false
	return parsed.String()
}
