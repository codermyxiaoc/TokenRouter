//go:build integration

package repository

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// createVideoRepositoryRecord 使用真实 Create/Save 接口保存原生归属及非 JSON 响应。
func createVideoRepositoryRecord(t *testing.T, f *videoBillingFixture, keyID, accountID int64, endpoint service.VideoEndpoint, upstreamID string) *service.VideoTaskRecord {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := &service.VideoTaskRecord{
		ID: "vid_" + uuid.NewString(), UserID: f.userID, APIKeyID: keyID, GroupID: f.groupID, AccountID: accountID,
		PayloadHash: uuid.NewString(), Status: "prepared", BillingStatus: "pending", CreatedAt: now, NextPollAt: now,
		LeaseToken: uuid.NewString(), LeaseUntil: now.Add(time.Minute),
		Target: service.VideoUpstreamTarget{Version: 1, Endpoint: endpoint, AccountID: accountID},
	}
	_, created, err := f.tasks.Create(context.Background(), record, 1000)
	require.NoError(t, err)
	require.True(t, created)
	record.UpstreamTaskID = upstreamID
	record.ResponseStatus, record.CreateResponseStatus = http.StatusBadGateway, http.StatusBadGateway
	record.RawResponse = []byte("<html>upstream unavailable</html>\x00\xff")
	record.CreateResponse = append([]byte(nil), record.RawResponse...)
	require.NoError(t, f.tasks.Save(context.Background(), record, false))
	return record
}

func TestVideoTaskRepositoryUnifiedFindUsesLocalIDAndOwner(t *testing.T) {
	f := newVideoBillingFixture(t)
	record := createVideoRepositoryRecord(t, f, f.keyID, f.accountID, service.VideoEndpointSeedance, "provider-task-id")
	// 使用实际路由解析结果，防止客户端 unified 与仓储 compat 约定再次错位。
	route, ok := service.MatchVideoGatewayRoute(http.MethodGet, "/v1/video/generations/"+record.ID)
	require.True(t, ok)
	require.Equal(t, "unified", route.Protocol)
	for _, protocol := range []string{route.Protocol, "compat", "openai_videos", ""} {
		found, err := f.tasks.Find(context.Background(), f.keyID, f.userID, record.ID, protocol)
		require.NoError(t, err)
		require.Equal(t, record.ID, found.ID)
		require.Equal(t, record.UpstreamTaskID, found.UpstreamTaskID)
		require.Equal(t, record.RawResponse, found.RawResponse)
		require.Equal(t, record.CreateResponse, found.CreateResponse)
		for _, scope := range []struct{ key, user int64 }{{f.keyID + 1, f.userID}, {f.keyID, f.userID + 1}} {
			_, err = f.tasks.Find(context.Background(), scope.key, scope.user, record.ID, protocol)
			require.ErrorIs(t, err, service.ErrVideoTaskNotFound)
		}
		_, err = f.tasks.Find(context.Background(), f.keyID, f.userID, record.UpstreamTaskID, protocol)
		require.ErrorIs(t, err, service.ErrVideoTaskNotFound)
	}
	for _, scope := range []struct{ key, user int64 }{{f.keyID + 1, f.userID}, {f.keyID, f.userID + 1}} {
		_, err := f.tasks.Find(context.Background(), scope.key, scope.user, record.ID, route.Protocol)
		require.ErrorIs(t, err, service.ErrVideoTaskNotFound)
	}
	_, err := f.tasks.Find(context.Background(), f.keyID, f.userID, record.UpstreamTaskID, route.Protocol)
	require.ErrorIs(t, err, service.ErrVideoTaskNotFound)
	_, err = f.tasks.Find(context.Background(), f.keyID, f.userID, record.ID, string(service.VideoEndpointSeedance))
	require.ErrorIs(t, err, service.ErrVideoTaskNotFound)
	// 原生入口仍按供应商 ID 和协议查找，不与统一本地 ID 混用。
	for _, path := range []string{"/v3/contents/generations/tasks/", "/api/v3/contents/generations/tasks/"} {
		native, ok := service.MatchVideoGatewayRoute(http.MethodGet, path+record.UpstreamTaskID)
		require.True(t, ok)
		found, err := f.tasks.Find(context.Background(), f.keyID, f.userID, native.TaskID, native.Protocol)
		require.NoError(t, err)
		require.Equal(t, record.ID, found.ID)
	}
}

func TestVideoTaskRepositoryNativeCollisionAndProtocolScope(t *testing.T) {
	f := newVideoBillingFixture(t)
	ctx := context.Background()
	const upstreamID = "same-native-task-id"
	first := createVideoRepositoryRecord(t, f, f.keyID, f.accountID, service.VideoEndpointSeedance, upstreamID)
	wan := createVideoRepositoryRecord(t, f, f.keyID, f.accountID, service.VideoEndpointWan, upstreamID)
	for _, record := range []*service.VideoTaskRecord{first, wan} {
		found, err := f.tasks.Find(ctx, f.keyID, f.userID, upstreamID, string(record.Target.Endpoint))
		require.NoError(t, err)
		require.Equal(t, record.ID, found.ID)
	}
	secondAccount := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "video-collision-" + uuid.NewString(), Platform: service.PlatformVideo, Type: service.AccountTypeAPIKey})
	second := createVideoRepositoryRecord(t, f, f.keyID, secondAccount.ID, service.VideoEndpointSeedance, upstreamID)
	_, err := f.tasks.Find(ctx, f.keyID, f.userID, upstreamID, string(service.VideoEndpointSeedance))
	require.ErrorIs(t, err, service.ErrVideoTaskConflict)
	// 同协议跨账号碰撞时仅原生查询拒绝；两个本地 ID 仍能精确读取各自归属。
	for _, record := range []*service.VideoTaskRecord{first, second} {
		found, err := f.tasks.Find(ctx, f.keyID, f.userID, record.ID, "unified")
		require.NoError(t, err)
		require.Equal(t, record.AccountID, found.AccountID)
	}
	otherKey := mustCreateApiKey(t, integrationEntClient, &service.APIKey{UserID: f.userID, Key: "sk-video-scope-" + uuid.NewString(), Name: "scope", GroupID: &f.groupID})
	other := createVideoRepositoryRecord(t, f, otherKey.ID, secondAccount.ID, service.VideoEndpointSeedance, upstreamID)
	found, err := f.tasks.Find(ctx, otherKey.ID, f.userID, upstreamID, string(service.VideoEndpointSeedance))
	require.NoError(t, err)
	require.Equal(t, other.ID, found.ID)
	_, err = f.tasks.Find(ctx, otherKey.ID, f.userID, first.ID, "unified")
	require.ErrorIs(t, err, service.ErrVideoTaskNotFound)
	_, err = f.tasks.Find(ctx, otherKey.ID, f.userID+1, upstreamID, string(service.VideoEndpointSeedance))
	require.ErrorIs(t, err, service.ErrVideoTaskNotFound)
}

func TestVideoTaskRepositoryClaimReadyAndExpiredSave(t *testing.T) {
	f := newVideoBillingFixture(t)
	ctx := context.Background()
	readyA, readyB := f.task(0, 1, 1), f.task(0, 1, 1)
	live, future, done := f.task(0, 1, 1), f.task(0, 1, 1), f.task(0, 1, 1)
	_, err := integrationDB.ExecContext(ctx, `UPDATE video_tasks SET lease_until=NOW()-INTERVAL '1 second' WHERE id IN ($1,$2,$3,$4)`, readyA.ID, readyB.ID, future.ID, done.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET next_poll_at=NOW()+INTERVAL '1 hour' WHERE id=$1`, future.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE video_tasks SET effects_done=TRUE WHERE id=$1`, done.ID)
	require.NoError(t, err)
	// 两个真实领取事务各拿一条，SKIP LOCKED 不应重复领取或选中未到期、已完成任务。
	var wg sync.WaitGroup
	results := make([][]*service.VideoTaskRecord, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errs[i] = f.tasks.ClaimReady(ctx, 1, time.Minute) }(i)
	}
	wg.Wait()
	ids := map[string]bool{}
	for i := range results {
		require.NoError(t, errs[i])
		require.Len(t, results[i], 1)
		claimed := results[i][0]
		require.NotEmpty(t, claimed.LeaseToken)
		require.True(t, claimed.LeaseUntil.After(time.Now()))
		require.NotEqual(t, live.ID, claimed.ID)
		ids[claimed.ID] = true
	}
	require.Equal(t, map[string]bool{readyA.ID: true, readyB.ID: true}, ids)
	none, err := f.tasks.ClaimReady(ctx, 8, time.Minute)
	require.NoError(t, err)
	require.Empty(t, none)
	old := results[0][0]
	_, err = integrationDB.ExecContext(ctx, "UPDATE video_tasks SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1", old.ID)
	require.NoError(t, err)
	old.Status = "processing"
	require.ErrorIs(t, f.tasks.Save(ctx, old, false), service.ErrVideoTaskConflict)
	require.ErrorIs(t, f.tasks.Save(ctx, old, true), service.ErrVideoTaskConflict)
	claimed, err := f.tasks.Claim(ctx, old.ID, time.Minute)
	require.NoError(t, err)
	require.NotEqual(t, old.LeaseToken, claimed.LeaseToken)
	require.Equal(t, "prepared", claimed.Status)
	require.ErrorIs(t, f.tasks.Save(ctx, old, false), service.ErrVideoTaskConflict)
	// 新持有人可以续租并写入；释放后同 token 不能再次复活任务租约。
	claimed.Status, claimed.NextPollAt = "processing", time.Now().Add(time.Hour)
	require.NoError(t, f.tasks.Save(ctx, claimed, false))
	fresh, err := f.tasks.Get(ctx, claimed.ID)
	require.NoError(t, err)
	require.True(t, fresh.LeaseUntil.After(time.Now().Add(2*time.Minute)))
	require.NoError(t, f.tasks.Save(ctx, fresh, true))
	require.ErrorIs(t, f.tasks.Save(ctx, fresh, false), service.ErrVideoTaskConflict)
}
