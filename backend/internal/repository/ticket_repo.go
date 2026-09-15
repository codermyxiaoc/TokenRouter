package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

type ticketRepository struct{ db *sql.DB }

func NewTicketRepository(db *sql.DB) service.TicketRepository { return &ticketRepository{db: db} }

const ticketColumns = `t.id, t.user_id, u.email, COALESCE(u.username, ''), t.type, t.title, t.content,
 t.priority, t.status, t.order_id,
 t.created_at, t.updated_at, t.last_staff_reply_at, t.closed_at, t.closed_by, t.closed_by_role,
 o.id, COALESCE(o.out_trade_no, ''), COALESCE(o.amount, 0), COALESCE(o.pay_amount, 0),
 COALESCE(NULLIF(o.provider_snapshot->>'currency', ''), 'CNY'), COALESCE(o.status, '')`
const ticketJoins = ` FROM support_tickets t JOIN users u ON u.id = t.user_id
 LEFT JOIN payment_orders o ON o.id = t.order_id`

type ticketQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type ticketScanner interface{ Scan(...any) error }

func scanTicket(row ticketScanner) (*service.Ticket, error) {
	ticket := &service.Ticket{}
	order := &service.TicketOrder{}
	var orderID sql.NullInt64
	err := row.Scan(&ticket.ID, &ticket.UserID, &ticket.UserEmail, &ticket.UserName, &ticket.Type, &ticket.Title, &ticket.Content,
		&ticket.Priority, &ticket.Status, &ticket.OrderID,
		&ticket.CreatedAt, &ticket.UpdatedAt, &ticket.LastStaffReplyAt, &ticket.ClosedAt, &ticket.ClosedBy, &ticket.ClosedByRole,
		&orderID, &order.OutTradeNo, &order.Amount, &order.PayAmount, &order.Currency, &order.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrTicketNotFound
	}
	if err != nil {
		return nil, err
	}
	if orderID.Valid {
		order.ID = orderID.Int64
		ticket.Order = order
	}
	return ticket, nil
}

func ticketConditions(actor service.TicketActor, filter service.TicketListFilter) (string, []any) {
	conditions := []string{"1=1"}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(clause, len(args)))
	}
	if !actor.IsAdmin {
		add("t.user_id = $%d", actor.UserID)
	}
	if filter.Status != "" {
		add("t.status = $%d", filter.Status)
	}
	if filter.Type != "" {
		add("t.type = $%d", filter.Type)
	}
	if filter.Priority != "" {
		add("t.priority = $%d", filter.Priority)
	}
	if filter.Search != "" {
		// 将 LIKE 元字符作为普通文本处理，同时始终通过参数绑定隔离 SQL。
		search := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(filter.Search)
		args = append(args, "%"+search+"%")
		n := len(args)
		clause := fmt.Sprintf("(t.title ILIKE $%d OR CAST(t.id AS TEXT) ILIKE $%d", n, n)
		if actor.IsAdmin {
			clause += fmt.Sprintf(" OR u.email ILIKE $%d OR u.username ILIKE $%d", n, n)
		}
		conditions = append(conditions, clause+")")
	}
	return strings.Join(conditions, " AND "), args
}

func (r *ticketRepository) List(ctx context.Context, actor service.TicketActor, filter service.TicketListFilter) (*service.TicketListResult, error) {
	where, args := ticketConditions(actor, filter)
	result := &service.TicketListResult{Items: []service.Ticket{}, Page: filter.Page, PageSize: filter.PageSize}
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*)"+ticketJoins+" WHERE "+where, args...).Scan(&result.Total); err != nil {
		return nil, err
	}
	result.Pages = int((result.Total + int64(filter.PageSize) - 1) / int64(filter.PageSize))
	order := "t.updated_at DESC, t.id DESC"
	if actor.IsAdmin {
		order = "CASE t.priority WHEN 'high' THEN 0 WHEN 'normal' THEN 1 ELSE 2 END, " + order
	}
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := r.db.QueryContext(ctx, "SELECT "+ticketColumns+ticketJoins+" WHERE "+where+" ORDER BY "+order+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		ticket, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, *ticket)
	}
	return result, rows.Err()
}

func (r *ticketRepository) Get(ctx context.Context, actor service.TicketActor, id int64) (*service.Ticket, error) {
	// 同一快照读取正文和附件元数据，防止并发回复产生缺少消息的详情响应。
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ticket, err := getTicket(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ticket, nil
}

func getTicket(ctx context.Context, db ticketQuerier, actor service.TicketActor, id int64) (*service.Ticket, error) {
	ticket, err := scanTicket(db.QueryRowContext(ctx, "SELECT "+ticketColumns+ticketJoins+" WHERE t.id = $1 AND ($2 OR t.user_id = $3)", id, actor.IsAdmin, actor.UserID))
	if err != nil {
		return nil, err
	}
	ticket.Messages = []service.TicketMessage{}
	rows, err := db.QueryContext(ctx, `SELECT m.id, m.ticket_id, m.user_id, COALESCE(NULLIF(u.username, ''), CASE WHEN m.is_staff THEN '客服' ELSE '用户' END), m.is_staff, m.content, m.created_at
 FROM support_ticket_messages m JOIN users u ON u.id = m.user_id WHERE m.ticket_id = $1 ORDER BY m.id`, id)
	if err != nil {
		return nil, err
	}
	indices := map[int64]int{}
	for rows.Next() {
		var msg service.TicketMessage
		msg.Attachments = []service.TicketAttachment{}
		if err := rows.Scan(&msg.ID, &msg.TicketID, &msg.UserID, &msg.SenderName, &msg.IsStaff, &msg.Content, &msg.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		indices[msg.ID] = len(ticket.Messages)
		ticket.Messages = append(ticket.Messages, msg)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT a.id, a.message_id, a.filename, a.content_type, a.size, a.created_at
 FROM support_ticket_attachments a JOIN support_ticket_messages m ON m.id = a.message_id WHERE m.ticket_id = $1 ORDER BY a.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a service.TicketAttachment
		if err := rows.Scan(&a.ID, &a.MessageID, &a.Filename, &a.ContentType, &a.Size, &a.CreatedAt); err != nil {
			return nil, err
		}
		if i, ok := indices[a.MessageID]; ok {
			ticket.Messages[i].Attachments = append(ticket.Messages[i].Attachments, a)
		}
	}
	return ticket, rows.Err()
}

func (r *ticketRepository) Create(ctx context.Context, actor service.TicketActor, input *service.CreateTicketInput, maxOpen int, requestHash string) (*service.Ticket, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// 用户行锁覆盖计数与插入，使不同实例的并发创建也不能突破进行中工单上限。
	var userID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, actor.UserID).Scan(&userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrTicketNotFound
		}
		return nil, err
	}
	if input.IdempotencyKey != "" {
		var id int64
		var hash string
		err := tx.QueryRowContext(ctx, `SELECT id, request_hash FROM support_tickets WHERE user_id = $1 AND request_key = $2`, actor.UserID, input.IdempotencyKey).Scan(&id, &hash)
		if err == nil {
			if hash != requestHash {
				return nil, service.ErrTicketIdempotencyConflict
			}
			return finishTicket(ctx, tx, actor, id)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM support_tickets WHERE user_id = $1 AND status IN ('pending', 'waiting_user')`, actor.UserID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= maxOpen {
		return nil, service.ErrTicketOpenLimit
	}
	if input.OrderID != nil {
		var orderID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM payment_orders WHERE id = $1 AND user_id = $2 FOR SHARE`, *input.OrderID, actor.UserID).Scan(&orderID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, service.ErrTicketOrderInvalid
			}
			return nil, err
		}
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO support_tickets (user_id, type, title, content, priority, order_id, request_key, request_hash)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, actor.UserID, input.Type, input.Title, input.Content, input.Priority, input.OrderID, input.IdempotencyKey, requestHash).Scan(&id)
	if err != nil {
		return nil, err
	}
	// 管理员以用户身份新建的首帖仍是提问，不触发待用户回复计时。
	if _, err := insertTicketMessage(ctx, tx, id, actor.UserID, false, input.Content, input.Attachments, "", ""); err != nil {
		return nil, err
	}
	return finishTicket(ctx, tx, actor, id)
}

func lockTicket(ctx context.Context, tx *sql.Tx, actor service.TicketActor, id int64) (string, error) {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM support_tickets WHERE id = $1 AND ($2 OR user_id = $3) FOR UPDATE`, id, actor.IsAdmin, actor.UserID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", service.ErrTicketNotFound
	}
	return status, err
}

func insertTicketMessage(ctx context.Context, tx *sql.Tx, ticketID, userID int64, isStaff bool, content string, files []service.TicketAttachmentUpload, key, hash string) (int64, error) {
	var messageID int64
	err := tx.QueryRowContext(ctx, `INSERT INTO support_ticket_messages (ticket_id, user_id, is_staff, content, request_key, request_hash)
 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, ticketID, userID, isStaff, content, key, hash).Scan(&messageID)
	if err != nil {
		return 0, err
	}
	for _, file := range files {
		if _, err := tx.ExecContext(ctx, `INSERT INTO support_ticket_attachments (message_id, filename, content_type, size, data) VALUES ($1,$2,$3,$4,$5)`, messageID, file.Name, file.ContentType, len(file.Data), file.Data); err != nil {
			return 0, err
		}
	}
	return messageID, nil
}

func finishTicket(ctx context.Context, tx *sql.Tx, actor service.TicketActor, id int64) (*service.Ticket, error) {
	ticket, err := getTicket(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ticket, nil
}

func (r *ticketRepository) Reply(ctx context.Context, actor service.TicketActor, id int64, input *service.ReplyTicketInput, requestHash string) (*service.Ticket, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	status, err := lockTicket(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	if input.IdempotencyKey != "" {
		var hash string
		err := tx.QueryRowContext(ctx, `SELECT request_hash FROM support_ticket_messages WHERE ticket_id = $1 AND user_id = $2 AND request_key = $3`, id, actor.UserID, input.IdempotencyKey).Scan(&hash)
		if err == nil {
			if hash != requestHash {
				return nil, service.ErrTicketIdempotencyConflict
			}
			return finishTicket(ctx, tx, actor, id)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if !service.IsTicketOpen(status) {
		return nil, service.ErrTicketClosed
	}
	messageID, err := insertTicketMessage(ctx, tx, id, actor.UserID, actor.IsAdmin, input.Content, input.Attachments, input.IdempotencyKey, requestHash)
	if err != nil {
		return nil, err
	}
	if actor.IsAdmin {
		_, err = tx.ExecContext(ctx, `UPDATE support_tickets SET status = 'waiting_user', last_staff_reply_at = clock_timestamp(), updated_at = clock_timestamp() WHERE id = $1`, id)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE support_tickets SET status = 'pending', updated_at = clock_timestamp() WHERE id = $1`, id)
	}
	if err != nil {
		return nil, err
	}
	ticket, err := finishTicket(ctx, tx, actor, id)
	if err == nil {
		ticket.ReplyCreated = true
		ticket.ReplyMessageID = messageID
	}
	return ticket, err
}

func (r *ticketRepository) Close(ctx context.Context, actor service.TicketActor, id int64, target string) (*service.Ticket, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	status, err := lockTicket(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	// 相同结束操作可安全重试，保留首次操作人；其余终态不能互相覆盖或恢复对话。
	if status == target {
		return finishTicket(ctx, tx, actor, id)
	}
	if !service.IsTicketOpen(status) {
		return nil, service.ErrTicketClosed
	}
	// 角色由已鉴权的调用上下文确定，不读取请求正文或用户当前可变角色。
	role := "user"
	if actor.IsAdmin {
		role = "admin"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE support_tickets SET status = $2, closed_by = $3, closed_by_role = $4, closed_at = clock_timestamp(), updated_at = clock_timestamp() WHERE id = $1`, id, target, actor.UserID, role); err != nil {
		return nil, err
	}
	ticket, err := finishTicket(ctx, tx, actor, id)
	if err == nil {
		ticket.ClosureCreated = true
	}
	return ticket, err
}

func (r *ticketRepository) Update(ctx context.Context, actor service.TicketActor, id int64, input *service.UpdateTicketInput) (*service.Ticket, error) {
	if !actor.IsAdmin {
		return nil, service.ErrTicketNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	status, err := lockTicket(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	if !service.IsTicketOpen(status) {
		return nil, service.ErrTicketClosed
	}
	if _, err := tx.ExecContext(ctx, `UPDATE support_tickets SET priority = $2, updated_at = clock_timestamp() WHERE id = $1`, id, input.Priority); err != nil {
		return nil, err
	}
	return finishTicket(ctx, tx, actor, id)
}

func (r *ticketRepository) Attachment(ctx context.Context, actor service.TicketActor, ticketID, attachmentID int64) (*service.TicketAttachment, error) {
	a := &service.TicketAttachment{}
	err := r.db.QueryRowContext(ctx, `SELECT a.id, a.message_id, a.filename, a.content_type, a.size, a.created_at, a.data
 FROM support_ticket_attachments a JOIN support_ticket_messages m ON m.id = a.message_id JOIN support_tickets t ON t.id = m.ticket_id
 WHERE a.id = $1 AND t.id = $2 AND ($3 OR t.user_id = $4)`, attachmentID, ticketID, actor.IsAdmin, actor.UserID).Scan(&a.ID, &a.MessageID, &a.Filename, &a.ContentType, &a.Size, &a.CreatedAt, &a.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrTicketNotFound
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (r *ticketRepository) Expire(ctx context.Context, cutoff time.Time) (int64, error) {
	// UPDATE 会锁行并在等待并发回复结束后重新检查条件，只有确实仍等待用户的工单过期。
	result, err := r.db.ExecContext(ctx, `UPDATE support_tickets SET status = 'expired', closed_by = NULL, closed_by_role = 'system', closed_at = clock_timestamp(), updated_at = clock_timestamp()
 WHERE status = 'waiting_user' AND last_staff_reply_at IS NOT NULL AND last_staff_reply_at <= $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
