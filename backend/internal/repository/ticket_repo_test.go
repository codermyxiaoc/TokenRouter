package repository

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// SQLMock 的严格顺序同时约束行锁、权限和提交边界，防止业务检查退回事务外。
func newTicketRepoMock(t *testing.T) (*ticketRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = db.Close()
	})
	return &ticketRepository{db: db}, mock
}

func expectTicketLock(mock sqlmock.Sqlmock, actor service.TicketActor, status string) {
	rows := sqlmock.NewRows([]string{"status"})
	if status != "" {
		rows.AddRow(status)
	}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT status FROM support_tickets WHERE id = $1 AND ($2 OR user_id = $3) FOR UPDATE`)).WithArgs(int64(10), actor.IsAdmin, actor.UserID).WillReturnRows(rows)
}

func expectTicketDetail(mock sqlmock.Sqlmock, actor service.TicketActor, status string) {
	expectTicketDetailClosure(mock, actor, status, 0, "")
}

func expectTicketDetailClosure(mock sqlmock.Sqlmock, actor service.TicketActor, status string, closedBy int64, role string) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	var closer any
	if closedBy != 0 {
		closer = closedBy
	}
	columns := []string{"id", "user_id", "email", "username", "type", "title", "content", "priority", "status", "order_id", "created_at", "updated_at", "last_staff_reply_at", "closed_at", "closed_by", "closed_by_role", "order.id", "out_trade_no", "amount", "pay_amount", "currency", "order.status"}
	mock.ExpectQuery(`SELECT .*t\.id, t\.user_id`).WithArgs(int64(10), actor.IsAdmin, actor.UserID).WillReturnRows(sqlmock.NewRows(columns).AddRow(10, 1, "user@example.com", "用户甲", "technical", "无法请求", "详情", "high", status, nil, now, now, nil, nil, closer, role, nil, "", 0, 0, "CNY", ""))
	mock.ExpectQuery(`SELECT m\.id, m\.ticket_id`).WithArgs(int64(10)).WillReturnRows(sqlmock.NewRows([]string{"id", "ticket_id", "user_id", "sender_name", "is_staff", "content", "created_at"}).AddRow(100, 10, 1, "用户甲", false, "详情", now))
	mock.ExpectQuery(`SELECT a\.id, a\.message_id`).WithArgs(int64(10)).WillReturnRows(sqlmock.NewRows([]string{"id", "message_id", "filename", "content_type", "size", "created_at"}))
}

func TestTicketCreateLocksUserBeforeCheckingOpenLimit(t *testing.T) {
	r, mock := newTicketRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`)).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM support_tickets WHERE user_id = \$1 AND status IN \('pending', 'waiting_user'\)`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectRollback()
	_, err := r.Create(context.Background(), service.TicketActor{UserID: 1}, &service.CreateTicketInput{}, 3, "hash")
	if !errors.Is(err, service.ErrTicketOpenLimit) {
		t.Fatalf("Create error = %v", err)
	}
}

func TestTicketCreateRejectsForeignFinancialOrder(t *testing.T) {
	r, mock := newTicketRepoMock(t)
	orderID := int64(222)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM users .*FOR UPDATE`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT COUNT`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM payment_orders WHERE id = $1 AND user_id = $2 FOR SHARE`)).WithArgs(orderID, int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()
	_, err := r.Create(context.Background(), service.TicketActor{UserID: 1}, &service.CreateTicketInput{Type: "financial", OrderID: &orderID}, 3, "hash")
	if !errors.Is(err, service.ErrTicketOrderInvalid) {
		t.Fatalf("Create error = %v", err)
	}
}

func TestTicketCreateRollsBackMessageAndTicketWhenAttachmentFails(t *testing.T) {
	r, mock := newTicketRepoMock(t)
	failure := errors.New("attachment write failed")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM users .*FOR UPDATE`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT COUNT`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`INSERT INTO support_tickets`).WithArgs(int64(1), "technical", "标题", "内容", "normal", nil, "", "hash").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(10))
	mock.ExpectQuery(`INSERT INTO support_ticket_messages`).WithArgs(int64(10), int64(1), false, "内容", "", "").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(100))
	mock.ExpectExec(`INSERT INTO support_ticket_attachments`).WithArgs(int64(100), "a.pdf", "application/pdf", 3, []byte("pdf")).WillReturnError(failure)
	mock.ExpectRollback()
	_, err := r.Create(context.Background(), service.TicketActor{UserID: 1}, &service.CreateTicketInput{Type: "technical", Title: "标题", Content: "内容", Priority: "normal", Attachments: []service.TicketAttachmentUpload{{Name: "a.pdf", ContentType: "application/pdf", Data: []byte("pdf")}}}, 3, "hash")
	if !errors.Is(err, failure) {
		t.Fatalf("Create error = %v", err)
	}
}

func TestTicketReplyPermissionsAndTerminalStates(t *testing.T) {
	for _, status := range []string{"", "completed", "cancelled", "expired"} {
		t.Run(status, func(t *testing.T) {
			r, mock := newTicketRepoMock(t)
			actor := service.TicketActor{UserID: 2}
			mock.ExpectBegin()
			expectTicketLock(mock, actor, status)
			mock.ExpectRollback()
			_, err := r.Reply(context.Background(), actor, 10, &service.ReplyTicketInput{Content: "回复"}, "hash")
			want := service.ErrTicketClosed
			if status == "" {
				want = service.ErrTicketNotFound
			}
			if !errors.Is(err, want) {
				t.Fatalf("Reply error = %v, want %v", err, want)
			}
		})
	}
}

func TestTicketReplyTransitionsUnderLock(t *testing.T) {
	for _, isAdmin := range []bool{false, true} {
		t.Run(map[bool]string{false: "user", true: "staff"}[isAdmin], func(t *testing.T) {
			r, mock := newTicketRepoMock(t)
			actor := service.TicketActor{UserID: 1, IsAdmin: isAdmin}
			mock.ExpectBegin()
			expectTicketLock(mock, actor, "pending")
			mock.ExpectQuery(`INSERT INTO support_ticket_messages`).WithArgs(int64(10), int64(1), isAdmin, "回复", "", "hash").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(101))
			status := "pending"
			if isAdmin {
				status = "waiting_user"
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE support_tickets SET status = 'waiting_user', last_staff_reply_at = clock_timestamp(), updated_at = clock_timestamp() WHERE id = $1`)).WithArgs(int64(10)).WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE support_tickets SET status = 'pending', updated_at = clock_timestamp() WHERE id = $1`)).WithArgs(int64(10)).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			expectTicketDetail(mock, actor, status)
			mock.ExpectCommit()
			ticket, err := r.Reply(context.Background(), actor, 10, &service.ReplyTicketInput{Content: "回复"}, "hash")
			if err != nil {
				t.Fatal(err)
			}
			if ticket.Status != status || !ticket.ReplyCreated || ticket.ReplyMessageID != 101 {
				t.Fatalf("unexpected ticket: %+v", ticket)
			}
		})
	}
}

func TestTicketReplyRetrySurvivesClosureWithoutDuplicateNotification(t *testing.T) {
	r, mock := newTicketRepoMock(t)
	actor := service.TicketActor{UserID: 1, IsAdmin: true}
	mock.ExpectBegin()
	expectTicketLock(mock, actor, "completed")
	mock.ExpectQuery(`SELECT request_hash FROM support_ticket_messages`).WithArgs(int64(10), int64(1), "reply-key").WillReturnRows(sqlmock.NewRows([]string{"request_hash"}).AddRow("hash"))
	expectTicketDetail(mock, actor, "completed")
	mock.ExpectCommit()
	ticket, err := r.Reply(context.Background(), actor, 10, &service.ReplyTicketInput{Content: "回复", IdempotencyKey: "reply-key"}, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if ticket.ReplyCreated {
		t.Fatal("retry must not trigger notification again")
	}
}

func TestTicketCreateRetryPrecedesOpenLimit(t *testing.T) {
	r, mock := newTicketRepoMock(t)
	actor := service.TicketActor{UserID: 1}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM users .*FOR UPDATE`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT id, request_hash FROM support_tickets`).WithArgs(int64(1), "create-key").WillReturnRows(sqlmock.NewRows([]string{"id", "request_hash"}).AddRow(10, "hash"))
	expectTicketDetail(mock, actor, "pending")
	mock.ExpectCommit()
	if _, err := r.Create(context.Background(), actor, &service.CreateTicketInput{IdempotencyKey: "create-key"}, 1, "hash"); err != nil {
		t.Fatal(err)
	}
}

func TestTicketCloseIsIdempotentButCannotRewriteTerminalState(t *testing.T) {
	for _, current := range []string{"completed", "cancelled"} {
		t.Run(current, func(t *testing.T) {
			r, mock := newTicketRepoMock(t)
			actor := service.TicketActor{UserID: 1}
			mock.ExpectBegin()
			expectTicketLock(mock, actor, current)
			if current == "completed" {
				expectTicketDetailClosure(mock, actor, current, 42, "admin")
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			closed, err := r.Close(context.Background(), actor, 10, "completed")
			if current == "completed" && err != nil {
				t.Fatal(err)
			}
			if current != "completed" && !errors.Is(err, service.ErrTicketClosed) {
				t.Fatalf("Close error = %v", err)
			}
			if current == "completed" && (closed.ClosedBy == nil || *closed.ClosedBy != 42 || closed.ClosedByRole != "admin") {
				t.Fatalf("重试覆盖原操作者: %+v", closed)
			}
			if current == "completed" && closed.ClosureCreated {
				t.Fatal("终态重试不能再次触发结单邮件")
			}
		})
	}
}

func TestTicketAttachmentQueryEnforcesTicketAndOwner(t *testing.T) {
	r, mock := newTicketRepoMock(t)
	mock.ExpectQuery(`SELECT a\.id.*WHERE a\.id = \$1 AND t\.id = \$2 AND \(\$3 OR t\.user_id = \$4\)`).WithArgs(int64(77), int64(10), false, int64(2)).WillReturnError(sql.ErrNoRows)
	_, err := r.Attachment(context.Background(), service.TicketActor{UserID: 2}, 10, 77)
	if !errors.Is(err, service.ErrTicketNotFound) {
		t.Fatalf("Attachment error = %v", err)
	}
}

func TestTicketExpireOnlyWaitingUserFromLatestStaffReply(t *testing.T) {
	r, mock := newTicketRepoMock(t)
	cutoff := time.Now().Add(-24 * time.Hour)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE support_tickets SET status = 'expired', closed_by = NULL, closed_by_role = 'system', closed_at = clock_timestamp(), updated_at = clock_timestamp()
 WHERE status = 'waiting_user' AND last_staff_reply_at IS NOT NULL AND last_staff_reply_at <= $1`)).WithArgs(cutoff).WillReturnResult(sqlmock.NewResult(0, 4))
	count, err := r.Expire(context.Background(), cutoff)
	if err != nil || count != 4 {
		t.Fatalf("Expire = %d, %v", count, err)
	}
}

// 首次关单的身份和角色只取自鉴权上下文，四类人工操作均在持锁后原子写入。
func TestTicketCloseRecordsAuthenticatedActor(t *testing.T) {
	for _, isAdmin := range []bool{false, true} {
		role := "user"
		if isAdmin {
			role = "admin"
		}
		for _, target := range []string{"completed", "cancelled"} {
			t.Run(role+"/"+target, func(t *testing.T) {
				r, mock := newTicketRepoMock(t)
				actor := service.TicketActor{UserID: 7, IsAdmin: isAdmin}
				mock.ExpectBegin()
				expectTicketLock(mock, actor, "waiting_user")
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE support_tickets SET status = $2, closed_by = $3, closed_by_role = $4, closed_at = clock_timestamp(), updated_at = clock_timestamp() WHERE id = $1`)).WithArgs(int64(10), target, actor.UserID, role).WillReturnResult(sqlmock.NewResult(0, 1))
				expectTicketDetailClosure(mock, actor, target, actor.UserID, role)
				mock.ExpectCommit()
				closed, err := r.Close(context.Background(), actor, 10, target)
				if err != nil || closed.ClosedBy == nil || *closed.ClosedBy != actor.UserID || closed.ClosedByRole != role || !closed.ClosureCreated {
					t.Fatalf("Close = %+v, %v", closed, err)
				}
			})
		}
	}
}

func TestTicketListUserScopeAndAdminPriority(t *testing.T) {
	where, args := ticketConditions(service.TicketActor{UserID: 5}, service.TicketListFilter{Search: "%_"})
	if !strings.Contains(where, "t.user_id = $1") || strings.Contains(where, "u.email") || args[1] != `%\%\_%` {
		t.Fatalf("unsafe user filter: %s, %v", where, args)
	}
	r, mock := newTicketRepoMock(t)
	mock.ExpectQuery(`SELECT COUNT`).WithArgs("financial", "%alice%").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT .*u\.email ILIKE.*ORDER BY CASE t\.priority WHEN 'high' THEN 0 WHEN 'normal' THEN 1 ELSE 2 END, t\.updated_at DESC, t\.id DESC LIMIT \$3 OFFSET \$4`).WithArgs("financial", "%alice%", 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	_, err := r.List(context.Background(), service.TicketActor{UserID: 1, IsAdmin: true}, service.TicketListFilter{Page: 1, PageSize: 20, Type: "financial", Search: "alice"})
	if err != nil {
		t.Fatal(err)
	}
}

// 调整优先级保留事务锁和终态约束，不再创建或修改处理人员信息。
func TestTicketUpdateOnlyPriorityUnderLock(t *testing.T) {
	r, mock := newTicketRepoMock(t)
	actor := service.TicketActor{UserID: 1, IsAdmin: true}
	priority := "high"
	mock.ExpectBegin()
	expectTicketLock(mock, actor, "pending")
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE support_tickets SET priority = $2, updated_at = clock_timestamp() WHERE id = $1`)).WithArgs(int64(10), "high").WillReturnResult(sqlmock.NewResult(0, 1))
	expectTicketDetail(mock, actor, "pending")
	mock.ExpectCommit()
	updated, err := r.Update(context.Background(), actor, 10, &service.UpdateTicketInput{Priority: &priority})
	if err != nil || updated.Priority != "high" {
		t.Fatalf("Update = %+v, %v", updated, err)
	}
}
