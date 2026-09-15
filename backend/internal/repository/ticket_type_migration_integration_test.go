//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

// 在临时数据库事务内模拟273旧数据，保证升级后历史对话、附件和幂等摘要原样保留。
func TestTicketIntegrationConsultationMigrationPreservesHistory(t *testing.T) {
	actor, _ := ticketIntegrationActors(t)
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `DROP TRIGGER support_ticket_normalize_type ON support_tickets;
ALTER TABLE support_tickets DROP CONSTRAINT support_tickets_type_check;
ALTER TABLE support_tickets ADD CONSTRAINT support_tickets_type_check CHECK(type IN ('presales','aftersales','technical','financial','other'));`)
	require.NoError(t, err)
	ids := make([]int64, 0, 3)
	for _, kind := range []string{"presales", "aftersales", "other"} {
		var id int64
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO support_tickets(user_id,type,title,content,request_key,request_hash) VALUES($1,$2,'旧标题','旧内容',$2,'unchanged-hash') RETURNING id`, actor.UserID, kind).Scan(&id))
		ids = append(ids, id)
	}
	var messageID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO support_ticket_messages(ticket_id,user_id,content) VALUES($1,$2,'历史对话') RETURNING id`, ids[0], actor.UserID).Scan(&messageID))
	_, err = tx.ExecContext(ctx, `INSERT INTO support_ticket_attachments(message_id,filename,content_type,size,data) VALUES($1,'a.png','image/png',3,$2)`, messageID, []byte{1, 2, 3})
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("274_support_ticket_consultation_type.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	for _, id := range ids {
		var kind, title, content, hash string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT type,title,content,request_hash FROM support_tickets WHERE id=$1`, id).Scan(&kind, &title, &content, &hash))
		require.Equal(t, "consultation", kind)
		require.Equal(t, "旧标题", title)
		require.Equal(t, "旧内容", content)
		require.Equal(t, "unchanged-hash", hash)
	}
	var content string
	var data []byte
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT m.content,a.data FROM support_ticket_messages m JOIN support_ticket_attachments a ON a.message_id=m.id WHERE m.id=$1`, messageID).Scan(&content, &data))
	require.Equal(t, "历史对话", content)
	require.Equal(t, []byte{1, 2, 3}, data)
	// 升级窗口旧实例仍可提交旧类型，数据库将其归入咨询。
	var actual string
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO support_tickets(user_id,type,title,content) VALUES($1,'presales','兼容','旧实例') RETURNING type`, actor.UserID).Scan(&actual))
	require.Equal(t, "consultation", actual)
	_, err = tx.ExecContext(ctx, `INSERT INTO support_tickets(user_id,type,title,content) VALUES($1,'unknown','非法','非法')`, actor.UserID)
	require.Error(t, err)
}
