//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

// 临时 PostgreSQL 内重放优先级迁移，保证历史内容和旧幂等摘要不会被归类修改。
func TestTicketIntegrationThreePrioritiesMigrationPreservesHistory(t *testing.T) {
	actor, staff := ticketIntegrationActors(t)
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `DROP TRIGGER support_ticket_normalize_priority ON support_tickets;
ALTER TABLE support_tickets DROP CONSTRAINT support_tickets_priority_check;
ALTER TABLE support_tickets ADD CONSTRAINT support_tickets_priority_check CHECK(priority IN ('low','normal','high','urgent'));`)
	require.NoError(t, err)
	timestamp := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	ids := make(map[string]int64, 4)
	for _, priority := range []string{"low", "normal", "high", "urgent"} {
		var id int64
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO support_tickets(user_id,type,title,content,priority,assigned_to,request_key,request_hash,created_at,updated_at)
VALUES($1,'technical','旧标题','旧内容',$2,$3,$2,'legacy-priority-hash',$4,$4) RETURNING id`, actor.UserID, priority, staff.UserID, timestamp).Scan(&id))
		ids[priority] = id
	}
	var messageID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO support_ticket_messages(ticket_id,user_id,is_staff,content,request_hash) VALUES($1,$2,true,'历史客服消息','message-hash') RETURNING id`, ids["urgent"], staff.UserID).Scan(&messageID))
	_, err = tx.ExecContext(ctx, `INSERT INTO support_ticket_attachments(message_id,filename,content_type,size,data) VALUES($1,'历史.png','image/png',3,$2)`, messageID, []byte{1, 2, 3})
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("275_support_ticket_three_priorities.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	for prior, id := range ids {
		var priority, title, content, hash string
		var assigned int64
		var createdAt, updatedAt time.Time
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT priority,title,content,request_hash,assigned_to,created_at,updated_at FROM support_tickets WHERE id=$1`, id).Scan(&priority, &title, &content, &hash, &assigned, &createdAt, &updatedAt))
		want := prior
		if want == "urgent" {
			want = "high"
		}
		require.Equal(t, want, priority)
		require.Equal(t, "旧标题", title)
		require.Equal(t, "旧内容", content)
		require.Equal(t, "legacy-priority-hash", hash)
		require.Equal(t, staff.UserID, assigned)
		require.True(t, timestamp.Equal(createdAt))
		require.True(t, timestamp.Equal(updatedAt))
	}
	var content, hash string
	var senderID int64
	var isStaff bool
	var data []byte
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT m.content,m.request_hash,m.user_id,m.is_staff,a.data FROM support_ticket_messages m JOIN support_ticket_attachments a ON a.message_id=m.id WHERE m.id=$1`, messageID).Scan(&content, &hash, &senderID, &isStaff, &data))
	require.Equal(t, "历史客服消息", content)
	require.Equal(t, "message-hash", hash)
	require.Equal(t, staff.UserID, senderID)
	require.True(t, isStaff)
	require.Equal(t, []byte{1, 2, 3}, data)
	// 旧实例的新增与修改都由触发器兼容，新约束仍拒绝其他未知级别。
	var actual string
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO support_tickets(user_id,type,title,content,priority) VALUES($1,'technical','兼容','旧实例','urgent') RETURNING priority`, actor.UserID).Scan(&actual))
	require.Equal(t, "high", actual)
	require.NoError(t, tx.QueryRowContext(ctx, `UPDATE support_tickets SET priority='urgent' WHERE id=$1 RETURNING priority`, ids["low"]).Scan(&actual))
	require.Equal(t, "high", actual)
	_, err = tx.ExecContext(ctx, `INSERT INTO support_tickets(user_id,type,title,content,priority) VALUES($1,'technical','非法','非法','critical')`, actor.UserID)
	require.Error(t, err)
}
