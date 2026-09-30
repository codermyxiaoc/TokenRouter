package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestMediaTaskModelsReadAllMatchingRowsAndBindOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := NewMediaTaskRepository(db)
	// SQL 明确不含分页；个人入口忽略其他用户，管理员可按选中用户筛选。
	query := "SELECT DISTINCT t.model FROM media_tasks t WHERE 1=1 AND t.user_id=$1 AND t.media_type=$2 AND t.model<>'' ORDER BY t.model"
	for _, actor := range []service.MediaTaskActor{{UserID: 7}, {UserID: 1, IsAdmin: true}} {
		owner := int64(7)
		if actor.IsAdmin {
			owner = 9
		}
		mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(owner, "image").WillReturnRows(sqlmock.NewRows([]string{"model"}).AddRow("image-a").AddRow("image-b"))
		models, err := repo.ListModels(context.Background(), actor, service.MediaTaskFilter{UserID: 9, MediaType: "image", Model: "image-a", Page: 100, PageSize: 1})
		require.NoError(t, err)
		require.Equal(t, []string{"image-a", "image-b"}, models)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

// 精确选择与旧的文本搜索必须并存，避免选择 model 时意外同时显示 model-pro。
func TestMediaTaskModelExactDiffersFromLegacySubstring(t *testing.T) {
	actor := service.MediaTaskActor{UserID: 7}
	where, args := mediaTaskConditions(actor, service.MediaTaskFilter{Model: "model", ModelExact: true})
	require.Contains(t, where, "t.model=$2")
	require.NotContains(t, where, "ILIKE")
	require.Equal(t, []any{int64(7), "model"}, args)
	where, args = mediaTaskConditions(actor, service.MediaTaskFilter{Model: "model"})
	require.Contains(t, where, "t.model ILIKE $2")
	require.Equal(t, []any{int64(7), "%model%"}, args)
	// model-pro 必须保留完整绑定值，不能切成公共前缀或规范化成 model。
	_, args = mediaTaskConditions(actor, service.MediaTaskFilter{Model: "model-pro", ModelExact: true})
	require.Equal(t, []any{int64(7), "model-pro"}, args)
}

func TestMediaTaskImageResultUsesReadOnlyOwnerKeyLookup(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT record FROM image_tasks WHERE id=$1 AND user_id=$2 AND api_key_id=$3")).WithArgs("image-1", int64(7), int64(9)).WillReturnRows(sqlmock.NewRows([]string{"record"}).AddRow(`{"id":"image-1","user_id":7,"api_key_id":9,"status":"completed"}`))
	record, err := NewMediaTaskRepository(db).GetImageResult(context.Background(), &service.MediaTask{TaskID: "image-1", UserID: 7, APIKeyID: 9})
	require.NoError(t, err)
	require.Equal(t, "completed", record.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMediaTaskPreviewCacheIdentityAndTicketExpiry(t *testing.T) {
	rdb := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: rdb.Addr()})
	defer client.Close()
	cache := NewMediaTaskPreviewCache(client)
	ctx := context.Background()
	identity := service.MediaTaskPreviewIdentity{ID: 1, Source: "grok_video", TaskID: "same-task", UserID: 7, APIKeyID: 9}
	require.NoError(t, cache.SaveVideo(ctx, &service.MediaTaskVideoPreviewRecord{Identity: identity, Media: service.MediaTaskVideoSnapshot{URL: "https://cdn.example/a.mp4"}}, time.Hour))
	_, err := cache.GetVideo(ctx, identity)
	require.NoError(t, err)
	for _, changed := range []service.MediaTaskPreviewIdentity{{Source: "seedance_video", TaskID: identity.TaskID, UserID: 7, APIKeyID: 9}, {Source: identity.Source, TaskID: identity.TaskID, UserID: 8, APIKeyID: 9}, {Source: identity.Source, TaskID: identity.TaskID, UserID: 7, APIKeyID: 10}} {
		_, err = cache.GetVideo(ctx, changed)
		require.Error(t, err)
	}
	require.NoError(t, cache.SaveTicket(ctx, "private-ticket", &service.MediaTaskPreviewTicket{Identity: identity}, 10*time.Minute))
	for _, key := range rdb.Keys() {
		require.NotContains(t, key, "private-ticket")
		require.NotContains(t, key, "same-task")
	}
	rdb.FastForward(11 * time.Minute)
	_, err = cache.GetTicket(ctx, "private-ticket")
	require.Error(t, err)
	_, err = cache.GetVideo(ctx, identity)
	require.NoError(t, err)
}
