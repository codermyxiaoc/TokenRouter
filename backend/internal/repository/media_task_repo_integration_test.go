//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMediaTaskIntegrationConcurrentProjectionOwnershipAndTerminal(t *testing.T) {
	// 所有数据在测试容器中创建，清理沿用用户根表的级联机制。
	_ = testEntClient(t)
	ctx := context.Background()
	var userID, keyID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES('media-test@example.com','test') RETURNING id`).Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,'media-test-key','test') RETURNING id`, userID).Scan(&keyID))
	// 账本不随用户级联清理，测试重置自增 ID 后可能复用旧 Key ID，因此核对操作前后的增量。
	var billingCountBefore int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1", keyID).Scan(&billingCountBefore))
	repo := NewMediaTaskRepository(integrationDB)
	s := service.NewMediaTaskService(repo)
	created := time.Now().UTC().Add(-time.Hour)
	expires := time.Now().UTC().Add(time.Hour)
	o := service.MediaTaskObservation{Source: "async_image", TaskID: "imgtask_concurrent", MediaType: "image", Platform: "openai", Model: "gpt-image-2", Status: "processing", UserID: userID, APIKeyID: keyID, CreatedAt: created, ExpiresAt: &expires}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.ObserveMediaTask(ctx, o) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	actor := service.MediaTaskActor{UserID: userID}
	list, err := s.List(ctx, actor, service.MediaTaskFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 1, list.Total, "并发观测不能新增重复任务")
	require.Nil(t, list.Items[0].ActualCost)
	o.Status, o.HTTPStatus, o.RequestID = "completed", 200, "client:media-task-test"
	require.NoError(t, s.ObserveMediaTask(ctx, o))
	o.Status, o.HTTPStatus, o.RequestID = "processing", 202, ""
	require.NoError(t, s.ObserveMediaTask(ctx, o))
	got, err := s.Get(ctx, actor, list.Items[0].ID)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status)
	require.Equal(t, 200, got.HTTPStatus)
	require.Equal(t, "client:media-task-test", got.RequestID)
	require.WithinDuration(t, created, got.CreatedAt, time.Microsecond)
	_, err = s.Get(ctx, service.MediaTaskActor{UserID: userID + 1000}, got.ID)
	require.ErrorIs(t, err, service.ErrMediaTaskNotFound)
	other, err := s.List(ctx, service.MediaTaskActor{UserID: userID + 1000}, service.MediaTaskFilter{UserID: userID})
	require.NoError(t, err)
	require.Zero(t, other.Total)
	_, err = s.Get(ctx, service.MediaTaskActor{UserID: userID + 1000, IsAdmin: true}, got.ID)
	require.NoError(t, err)
	// 同一上游ID在不同来源独立；未完成且已过期的任务仅改变展示，不产生收费。
	past := time.Now().Add(-time.Minute)
	o.Source, o.MediaType, o.ExpiresAt = "seedance_video", "video", &past
	require.NoError(t, s.ObserveMediaTask(ctx, o))
	expired, err := s.List(ctx, actor, service.MediaTaskFilter{Status: "expired"})
	require.NoError(t, err)
	require.EqualValues(t, 1, expired.Total)
	require.Equal(t, "expired", expired.Items[0].Status)
	// 模型过滤按字面值处理，SQL与LIKE特殊字符不应匹配到其它任务。
	filtered, err := s.List(ctx, actor, service.MediaTaskFilter{Model: "%' OR 1=1 --"})
	require.NoError(t, err)
	require.Zero(t, filtered.Total)
	var billingCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1", keyID).Scan(&billingCount))
	require.Equal(t, billingCountBefore, billingCount, "列表与观测不得自行扣费")
	t.Log(fmt.Sprintf("用户%d：并发去重、跨用户隔离、终态防回退、过期筛选已验证", userID))
}

func TestMediaTaskIntegrationUsageCostAndFinalRoutingGroup(t *testing.T) {
	_ = testEntClient(t)
	ctx := context.Background()
	var userID, keyID, otherKeyID, accountID, firstGroupID, finalGroupID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES('media-cost@example.com','test') RETURNING id`).Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,'media-cost-key','test') RETURNING id`, userID).Scan(&keyID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,'media-other-key','test') RETURNING id`, userID).Scan(&otherKeyID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type) VALUES('media-cost-account','grok','apikey') RETURNING id`).Scan(&accountID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform) VALUES('media-first-group','openai') RETURNING id`).Scan(&firstGroupID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform) VALUES('media-final-group','grok') RETURNING id`).Scan(&finalGroupID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, accountID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id IN ($1,$2)`, firstGroupID, finalGroupID)
	})
	s := service.NewMediaTaskService(NewMediaTaskRepository(integrationDB))
	actor := service.MediaTaskActor{UserID: userID}
	o := service.MediaTaskObservation{Source: "async_image", TaskID: "imgtask_routed", MediaType: "image", Platform: "openai", Model: "requested-image", Status: "completed", UserID: userID, APIKeyID: keyID, GroupID: &firstGroupID, HTTPStatus: 200, RequestID: "client:media-cost-join"}
	require.NoError(t, s.ObserveMediaTask(ctx, o))
	list, err := s.List(ctx, actor, service.MediaTaskFilter{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	id := list.Items[0].ID
	// 其它 Key 的同名请求不能填充本任务费用或改写最终分组。
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,model,request_id,actual_cost,billing_mode,group_id) VALUES($1,$2,$3,'upstream-image',$4,99,'image',$5)`, userID, otherKeyID, accountID, o.RequestID, finalGroupID)
	require.NoError(t, err)
	got, err := s.Get(ctx, actor, id)
	require.NoError(t, err)
	require.Nil(t, got.ActualCost)
	require.Equal(t, firstGroupID, *got.GroupID)
	// 使用记录稍后落库后，读取列表即可关联 Token 费用和智能路由最终分组。
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,model,request_id,actual_cost,billing_mode,group_id) VALUES($1,$2,$3,'upstream-image',$4,0.02815,'token',$5)`, userID, keyID, accountID, o.RequestID, finalGroupID)
	require.NoError(t, err)
	got, err = s.Get(ctx, actor, id)
	require.NoError(t, err)
	require.InDelta(t, 0.02815, *got.ActualCost, 1e-10)
	require.Equal(t, "token", *got.BillingMode)
	require.Equal(t, finalGroupID, *got.GroupID)
	require.Equal(t, "media-final-group", *got.GroupName)
	require.Equal(t, "grok", got.Platform)
	require.Equal(t, "requested-image", got.Model)
	require.Nil(t, got.AccountID, "用户接口不展示上游账号 ID")
	admin, err := s.Get(ctx, service.MediaTaskActor{UserID: userID, IsAdmin: true}, id)
	require.NoError(t, err)
	require.Equal(t, accountID, *admin.AccountID)
	_, err = integrationDB.ExecContext(ctx, `UPDATE usage_logs SET actual_cost=0 WHERE request_id=$1 AND api_key_id=$2`, o.RequestID, keyID)
	require.NoError(t, err)
	got, err = s.Get(ctx, actor, id)
	require.NoError(t, err)
	require.NotNil(t, got.ActualCost, "明确零费用不能变成待确认")
	require.Zero(t, *got.ActualCost)
}

func TestMediaTaskIntegrationAdminCurrentUserIdentityAndPrivacy(t *testing.T) {
	_ = testEntClient(t)
	ctx := context.Background()
	var userID, keyID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,username) VALUES('task-owner@example.test','never-expose-password','任务用户') RETURNING id`).Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,'task-user-identity-key','test') RETURNING id`, userID).Scan(&keyID))
	s := service.NewMediaTaskService(NewMediaTaskRepository(integrationDB))
	require.NoError(t, s.ObserveMediaTask(ctx, service.MediaTaskObservation{Source: "async_image", TaskID: "imgtask_user_identity", MediaType: "image", Platform: "openai", Model: "gpt-image-2", Status: "completed", UserID: userID, APIKeyID: keyID, HTTPStatus: 200}))
	admin := service.MediaTaskActor{UserID: userID + 1000, IsAdmin: true}
	list, err := s.List(ctx, admin, service.MediaTaskFilter{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	taskID := list.Items[0].ID
	require.Equal(t, &service.MediaTaskUser{ID: userID, Email: "task-owner@example.test", Username: "任务用户"}, list.Items[0].User)
	owner := service.MediaTaskActor{UserID: userID}
	personal, err := s.List(ctx, owner, service.MediaTaskFilter{UserID: userID + 1000})
	require.NoError(t, err)
	require.Len(t, personal.Items, 1)
	require.Nil(t, personal.Items[0].User, "个人列表不返回管理端身份对象")
	personalDetail, err := s.Get(ctx, owner, taskID)
	require.NoError(t, err)
	require.Nil(t, personalDetail.User)
	// 查询读取用户当前资料，不在任务创建时额外复制个人信息快照。
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET username='',email='updated-owner@example.test' WHERE id=$1`, userID)
	require.NoError(t, err)
	detail, err := s.Get(ctx, admin, taskID)
	require.NoError(t, err)
	require.Equal(t, "", detail.User.Username)
	require.Equal(t, "updated-owner@example.test", detail.User.Email, "空用户名由界面按邮箱回退")
	deleted := time.Now().UTC().Truncate(time.Microsecond)
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET deleted_at=$2 WHERE id=$1`, userID, deleted)
	require.NoError(t, err)
	detail, err = s.Get(ctx, admin, taskID)
	require.NoError(t, err)
	require.NotNil(t, detail.User.DeletedAt, "软删除用户仍保留管理员可追溯的历史身份")
	require.WithinDuration(t, deleted, *detail.User.DeletedAt, time.Microsecond)
	list, err = s.List(ctx, admin, service.MediaTaskFilter{UserID: userID})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.NotNil(t, list.Items[0].User.DeletedAt)
	// 正常外键会级联硬删除；用空关联模拟遗留缺失用户，验证 LEFT JOIN 的安全扫描与 #ID 回退。
	missingJoin := strings.Replace(mediaTaskJoins, "task_user.id=t.user_id", "FALSE", 1)
	missing, err := scanMediaTask(integrationDB.QueryRowContext(ctx, "SELECT "+mediaTaskColumns+missingJoin+" WHERE t.id=$1", taskID))
	require.NoError(t, err)
	require.Nil(t, missing.User)
	require.Equal(t, userID, missing.UserID)
	_, err = s.Get(ctx, service.MediaTaskActor{UserID: userID + 1000}, taskID)
	require.ErrorIs(t, err, service.ErrMediaTaskNotFound, "新增身份关联不能放宽原任务归属")
}
