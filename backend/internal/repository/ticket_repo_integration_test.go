//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

func ticketIntegrationActors(t *testing.T) (service.TicketActor, service.TicketActor) {
	t.Helper()
	// 复用测试容器清理钩子，所有提交和并发校验仅作用于临时 PostgreSQL。
	_ = testEntClient(t)
	var userID, staffID int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `INSERT INTO users (email,password_hash) VALUES ('ticket-user@example.com','test') RETURNING id`).Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `INSERT INTO users (email,password_hash,role) VALUES ('ticket-staff@example.com','test','admin') RETURNING id`).Scan(&staffID))
	return service.TicketActor{UserID: userID}, service.TicketActor{UserID: staffID, IsAdmin: true}
}

func TestTicketIntegrationMigrationAndConcurrentCreateLimit(t *testing.T) {
	ctx := context.Background()
	actor, _ := ticketIntegrationActors(t)
	// 重放新迁移必须保留已存在结构，并由数据库约束独立保护附件字节数。
	contents, err := migrations.FS.ReadFile("273_support_tickets.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(contents))
	require.NoError(t, err)
	repo := NewTicketRepository(integrationDB)
	const requests = 8
	results := make([]error, requests)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = repo.Create(ctx, actor, &service.CreateTicketInput{Type: "technical", Title: fmt.Sprintf("工单%d", i), Content: "详情", Priority: "normal"}, 2, "")
		}(i)
	}
	close(start)
	wg.Wait()
	success := 0
	for _, err := range results {
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, service.ErrTicketOpenLimit)
		}
	}
	require.Equal(t, 2, success)
	list, err := repo.List(ctx, actor, service.TicketListFilter{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 2, list.Total)
	var messageID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT id FROM support_ticket_messages LIMIT 1`).Scan(&messageID))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO support_ticket_attachments (message_id,filename,content_type,size,data) VALUES ($1,'x.pdf','application/pdf',2,$2)`, messageID, []byte{1})
	require.Error(t, err)
}

func TestTicketIntegrationLifecycleAttachmentAndIdempotency(t *testing.T) {
	ctx := context.Background()
	actor, staff := ticketIntegrationActors(t)
	repo := NewTicketRepository(integrationDB)
	created, err := repo.Create(ctx, actor, &service.CreateTicketInput{Type: "technical", Title: "标题", Content: "详情", Priority: "high", IdempotencyKey: "create-key", Attachments: []service.TicketAttachmentUpload{{Name: "a.pdf", ContentType: "application/pdf", Data: []byte("%PDF-test")}}}, 1, "hash")
	require.NoError(t, err)
	repeated, err := repo.Create(ctx, actor, &service.CreateTicketInput{IdempotencyKey: "create-key"}, 1, "hash")
	require.NoError(t, err)
	require.Equal(t, created.ID, repeated.ID)
	_, err = repo.Create(ctx, actor, &service.CreateTicketInput{IdempotencyKey: "create-key"}, 1, "other")
	require.ErrorIs(t, err, service.ErrTicketIdempotencyConflict)
	require.Len(t, created.Messages, 1)
	require.Len(t, created.Messages[0].Attachments, 1)
	fileID := created.Messages[0].Attachments[0].ID
	_, err = repo.Get(ctx, service.TicketActor{UserID: staff.UserID}, created.ID)
	require.ErrorIs(t, err, service.ErrTicketNotFound)
	_, err = repo.Attachment(ctx, service.TicketActor{UserID: staff.UserID}, created.ID, fileID)
	require.ErrorIs(t, err, service.ErrTicketNotFound)
	file, err := repo.Attachment(ctx, staff, created.ID, fileID)
	require.NoError(t, err)
	require.Equal(t, []byte("%PDF-test"), file.Data)
	replied, err := repo.Reply(ctx, staff, created.ID, &service.ReplyTicketInput{Content: "请补充", IdempotencyKey: "reply-key"}, "reply-hash")
	require.NoError(t, err)
	require.Equal(t, "waiting_user", replied.Status)
	// 客服消息仍保留真实发送人，但回复不再自动写入旧处理人员列。
	require.Equal(t, staff.UserID, replied.Messages[1].UserID)
	require.True(t, replied.Messages[1].IsStaff)
	var assigned sql.NullInt64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT assigned_to FROM support_tickets WHERE id=$1`, created.ID).Scan(&assigned))
	require.False(t, assigned.Valid)
	require.NotNil(t, replied.LastStaffReplyAt)
	require.True(t, replied.ReplyCreated)
	require.NotZero(t, replied.ReplyMessageID)
	_, err = repo.Close(ctx, actor, created.ID, "completed")
	require.NoError(t, err)
	repeated, err = repo.Reply(ctx, staff, created.ID, &service.ReplyTicketInput{Content: "请补充", IdempotencyKey: "reply-key"}, "reply-hash")
	require.NoError(t, err)
	require.False(t, repeated.ReplyCreated)
	require.Len(t, repeated.Messages, 2)
	_, err = repo.Reply(ctx, actor, created.ID, &service.ReplyTicketInput{Content: "迟到回复"}, "")
	require.ErrorIs(t, err, service.ErrTicketClosed)
	_, err = repo.Close(ctx, actor, created.ID, "completed")
	require.NoError(t, err)
	_, err = repo.Close(ctx, actor, created.ID, "cancelled")
	require.ErrorIs(t, err, service.ErrTicketClosed)
}

func TestTicketIntegrationExpirationCannotOverwriteConcurrentUserReply(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	actor, staff := ticketIntegrationActors(t)
	repo := NewTicketRepository(integrationDB)
	created, err := repo.Create(ctx, actor, &service.CreateTicketInput{Type: "other", Title: "标题", Content: "详情", Priority: "normal"}, 3, "")
	require.NoError(t, err)
	_, err = repo.Reply(ctx, staff, created.ID, &service.ReplyTicketInput{Content: "请补充"}, "")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE support_tickets SET last_staff_reply_at = NOW() - INTERVAL '2 days' WHERE id = $1`, created.ID)
	require.NoError(t, err)
	// 显式持有与真实回复相同的行锁，让过期 UPDATE 与已开始的用户状态变更发生竞争。
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	var id int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT id FROM support_tickets WHERE id = $1 FOR UPDATE`, created.ID).Scan(&id))
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		count, err := repo.Expire(ctx, time.Now().Add(-24*time.Hour))
		if err == nil && count != 0 {
			err = errors.New("expiration overwrote user reply")
		}
		result <- err
	}()
	<-started
	_, err = tx.ExecContext(ctx, `UPDATE support_tickets SET status = 'pending' WHERE id = $1`, created.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-result)
	after, err := repo.Get(ctx, actor, created.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", after.Status)
	_, err = repo.Reply(ctx, staff, created.ID, &service.ReplyTicketInput{Content: "新的工作人员回复"}, "")
	require.NoError(t, err)
	count, err := repo.Expire(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.Zero(t, count, "最新工作人员回复应重新开始计时")
}
