package repository

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 保留旧 Redis-only 仓储的兼容边界；正式装配已使用 PostgreSQL 持久仓储，恢复由其集成测试覆盖。
func TestImageTaskDurabilityRestartKeepsProcessingUntilRedisExpiry(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	owner := service.ImageTaskOwner{UserID: 7, APIKeyID: 9}
	before := service.NewImageTaskServiceWithUploader(NewImageTaskStore(rdb), nil, 24*time.Hour, time.Minute)
	task, err := before.Create(context.Background(), owner)
	require.NoError(t, err)

	after := service.NewImageTaskServiceWithUploader(NewImageTaskStore(rdb), nil, 24*time.Hour, time.Minute)
	mr.FastForward(23 * time.Hour)
	for i := 0; i < 5; i++ {
		got, err := after.Get(context.Background(), owner, task.ID)
		require.NoError(t, err)
		require.Equal(t, service.ImageTaskStatusProcessing, got.Status)
		require.Nil(t, got.CompletedAt)
	}
	require.Equal(t, time.Hour, mr.TTL(imageTaskKey(task.ID)), "重复查询不续租任务")
	mr.FastForward(time.Hour + time.Second)
	_, err = after.Get(context.Background(), owner, task.ID)
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
}

func TestImageTaskDurabilityCompletedResultSurvivesServiceRecreationAndHasFreshTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	owner := service.ImageTaskOwner{UserID: 7, APIKeyID: 9}
	before := service.NewImageTaskServiceWithUploader(NewImageTaskStore(rdb), nil, 24*time.Hour, time.Minute)
	task, err := before.Create(context.Background(), owner)
	require.NoError(t, err)
	mr.FastForward(20 * time.Hour)
	result := json.RawMessage(`{"data":[{"url":"https://example.test/image.png"}],"usage":{"output_tokens":100}}`)
	require.NoError(t, before.Complete(context.Background(), task.ID, http.StatusOK, result))
	require.Equal(t, 24*time.Hour, mr.TTL(imageTaskKey(task.ID)), "写终态重新计算结果留存期")
	after := service.NewImageTaskService(NewImageTaskStore(rdb))
	require.False(t, after.Enabled())
	got, err := after.Get(context.Background(), owner, task.ID)
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStatusCompleted, got.Status)
	require.JSONEq(t, string(result), string(got.Result))
	// Redis 数据丢失不会从安全元数据投影中重建图片结果或重新执行生成。
	mr.FlushAll()
	_, err = after.Get(context.Background(), owner, task.ID)
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
}
