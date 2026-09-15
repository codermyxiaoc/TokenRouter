//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

// 人工关单保留首次认证身份，即使另一个角色重试或尝试改写终态也不能覆盖。
func TestTicketIntegrationClosureActorAndIdempotency(t *testing.T) {
	ctx := context.Background()
	user, admin := ticketIntegrationActors(t)
	repo := NewTicketRepository(integrationDB)
	for _, role := range []string{"user", "admin"} {
		for _, target := range []string{service.TicketStatusCompleted, service.TicketStatusCancelled} {
			t.Run(role+"/"+target, func(t *testing.T) {
				closer, repeater := user, admin
				if role == "admin" {
					closer, repeater = admin, user
				}
				created, err := repo.Create(ctx, user, &service.CreateTicketInput{Type: "technical", Title: "结束身份", Content: "问题", Priority: "normal"}, 5, "")
				require.NoError(t, err)
				require.Equal(t, service.TicketStatusPending, created.Status)
				require.Nil(t, created.ClosedBy)
				require.Empty(t, created.ClosedByRole)
				closed, err := repo.Close(ctx, closer, created.ID, target)
				require.NoError(t, err)
				require.NotNil(t, closed.ClosedBy)
				require.True(t, closed.ClosureCreated)
				require.Equal(t, closer.UserID, *closed.ClosedBy)
				require.Equal(t, role, closed.ClosedByRole)
				require.NotNil(t, closed.ClosedAt)
				repeated, err := repo.Close(ctx, repeater, created.ID, target)
				require.NoError(t, err)
				require.False(t, repeated.ClosureCreated)
				require.Equal(t, closed.ClosedBy, repeated.ClosedBy)
				require.Equal(t, role, repeated.ClosedByRole)
				require.True(t, closed.ClosedAt.Equal(*repeated.ClosedAt))
				otherTarget := service.TicketStatusCompleted
				if target == otherTarget {
					otherTarget = service.TicketStatusCancelled
				}
				_, err = repo.Close(ctx, repeater, created.ID, otherTarget)
				require.ErrorIs(t, err, service.ErrTicketClosed)
				after, err := repo.Get(ctx, user, created.ID)
				require.NoError(t, err)
				require.Equal(t, target, after.Status)
				require.Equal(t, closed.ClosedBy, after.ClosedBy)
				require.Equal(t, role, after.ClosedByRole)
				list, err := repo.List(ctx, user, service.TicketListFilter{Page: 1, PageSize: 20})
				require.NoError(t, err)
				found := false
				for _, item := range list.Items {
					if item.ID == created.ID {
						found = true
						require.Equal(t, closed.ClosedBy, item.ClosedBy)
						require.Equal(t, role, item.ClosedByRole)
					}
				}
				require.True(t, found)
			})
		}
	}
}

// 用户和客服同时提交相同关单操作时，仅获胜事务产生通知标记，其他请求复用原结果。
func TestTicketIntegrationConcurrentClosureCreatesOneNotification(t *testing.T) {
	ctx := context.Background()
	user, admin := ticketIntegrationActors(t)
	repo := NewTicketRepository(integrationDB)
	created, err := repo.Create(ctx, user, &service.CreateTicketInput{Type: "technical", Title: "并发结单", Content: "详情", Priority: "normal"}, 5, "")
	require.NoError(t, err)
	type closeResult struct {
		ticket *service.Ticket
		err    error
	}
	results := make(chan closeResult, 2)
	start := make(chan struct{})
	for _, actor := range []service.TicketActor{user, admin} {
		go func(actor service.TicketActor) {
			<-start
			ticket, err := repo.Close(ctx, actor, created.ID, service.TicketStatusCompleted)
			results <- closeResult{ticket: ticket, err: err}
		}(actor)
	}
	close(start)
	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.NotEqual(t, first.ticket.ClosureCreated, second.ticket.ClosureCreated)
	require.Equal(t, first.ticket.ClosedBy, second.ticket.ClosedBy)
	require.Equal(t, first.ticket.ClosedByRole, second.ticket.ClosedByRole)
	require.True(t, first.ticket.ClosedAt.Equal(*second.ticket.ClosedAt))
}

// 等待对象随回复方切换，自动过期只记录系统，不会把最后一位客服当作关闭人。
func TestTicketIntegrationReplyWaitingPartyAndSystemClosure(t *testing.T) {
	ctx := context.Background()
	user, admin := ticketIntegrationActors(t)
	repo := NewTicketRepository(integrationDB)
	created, err := repo.Create(ctx, user, &service.CreateTicketInput{Type: "consultation", Title: "回复状态", Content: "提问", Priority: "normal"}, 5, "")
	require.NoError(t, err)
	require.Equal(t, service.TicketStatusPending, created.Status)
	staffReplied, err := repo.Reply(ctx, admin, created.ID, &service.ReplyTicketInput{Content: "请补充信息"}, "")
	require.NoError(t, err)
	require.Equal(t, service.TicketStatusWaitingUser, staffReplied.Status)
	require.Empty(t, staffReplied.ClosedByRole)
	userReplied, err := repo.Reply(ctx, user, created.ID, &service.ReplyTicketInput{Content: "补充信息"}, "")
	require.NoError(t, err)
	require.Equal(t, service.TicketStatusPending, userReplied.Status)
	require.Nil(t, userReplied.ClosedBy)
	// 人工把最近客服时间置旧，待客服回复的状态也不得被自动过期。
	_, err = integrationDB.ExecContext(ctx, `UPDATE support_tickets SET last_staff_reply_at=NOW()-INTERVAL '2 days' WHERE id=$1`, created.ID)
	require.NoError(t, err)
	count, err := repo.Expire(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.Zero(t, count)
	_, err = repo.Reply(ctx, admin, created.ID, &service.ReplyTicketInput{Content: "再次等待用户"}, "")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE support_tickets SET last_staff_reply_at=NOW()-INTERVAL '2 days' WHERE id=$1`, created.ID)
	require.NoError(t, err)
	count, err = repo.Expire(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	expired, err := repo.Get(ctx, user, created.ID)
	require.NoError(t, err)
	require.Equal(t, service.TicketStatusExpired, expired.Status)
	require.Equal(t, "system", expired.ClosedByRole)
	require.Nil(t, expired.ClosedBy)
	require.NotNil(t, expired.ClosedAt)
	_, err = repo.Close(ctx, admin, created.ID, service.TicketStatusCompleted)
	require.ErrorIs(t, err, service.ErrTicketClosed)
	repeated, err := repo.Get(ctx, admin, created.ID)
	require.NoError(t, err)
	require.Equal(t, "system", repeated.ClosedByRole)
	require.Nil(t, repeated.ClosedBy)
}

// 追加列不会从历史回复推测操作人，重复应用迁移也保留已记录的新操作人。
func TestTicketIntegrationClosureActorMigrationPreservesHistory(t *testing.T) {
	ctx := context.Background()
	user, admin := ticketIntegrationActors(t)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `ALTER TABLE support_tickets DROP COLUMN closed_by, DROP COLUMN closed_by_role`)
	require.NoError(t, err)
	ids := make([]int64, 0, 3)
	when := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, status := range []string{"completed", "cancelled", "expired"} {
		var id int64
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO support_tickets(user_id,type,title,content,status,closed_at,request_hash) VALUES($1,'technical','历史标题','历史正文',$2,$3,'old-hash') RETURNING id`, user.UserID, status, when).Scan(&id))
		ids = append(ids, id)
		_, err = tx.ExecContext(ctx, `INSERT INTO support_ticket_messages(ticket_id,user_id,is_staff,content) VALUES($1,$2,true,'旧客服回复不能推断关闭人')`, id, admin.UserID)
		require.NoError(t, err)
	}
	migration, err := migrations.FS.ReadFile("276_support_ticket_closure_actor.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	for _, id := range ids {
		var actor sql.NullInt64
		var role, title, content, hash string
		var closedAt time.Time
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT closed_by,closed_by_role,title,content,request_hash,closed_at FROM support_tickets WHERE id=$1`, id).Scan(&actor, &role, &title, &content, &hash, &closedAt))
		require.False(t, actor.Valid)
		require.Empty(t, role)
		require.Equal(t, "历史标题", title)
		require.Equal(t, "历史正文", content)
		require.Equal(t, "old-hash", hash)
		require.True(t, when.Equal(closedAt))
	}
	// 模拟新版本记录，随后重复执行迁移不能清空身份快照。
	_, err = tx.ExecContext(ctx, `UPDATE support_tickets SET closed_by=$2,closed_by_role='admin' WHERE id=$1`, ids[0], admin.UserID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var closer int64
	var role string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT closed_by,closed_by_role FROM support_tickets WHERE id=$1`, ids[0]).Scan(&closer, &role))
	require.Equal(t, admin.UserID, closer)
	require.Equal(t, "admin", role)
	_, err = tx.ExecContext(ctx, `UPDATE support_tickets SET closed_by_role='system' WHERE id=$1`, ids[0])
	require.Error(t, err, "系统操作不能伪造具体用户")
}
