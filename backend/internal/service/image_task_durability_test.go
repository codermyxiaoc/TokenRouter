package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 严格校验调用上下文，防止测试内存存储掩盖取消后的 Redis 写入失败。
type imageTaskContextCheckingStore struct{ imageTaskMemoryStore }

func (s *imageTaskContextCheckingStore) Save(ctx context.Context, task *ImageTaskRecord, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.imageTaskMemoryStore.Save(ctx, task, ttl)
}

func (s *imageTaskContextCheckingStore) Get(ctx context.Context, id string) (*ImageTaskRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.imageTaskMemoryStore.Get(ctx, id)
}

type imageTaskCancellingStorage struct{ cancel context.CancelFunc }

func (s imageTaskCancellingStorage) Save(ctx context.Context, _, _ string, _ []byte) (string, error) {
	s.cancel()
	return "", ctx.Err()
}

// 转存超时或取消后，终态使用独立上下文写回，而不是永久停留在处理中。
func TestImageTaskDurabilityStorageCancellationUsesIndependentFailureContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	uploader := NewImageResultUploader(imageTaskCancellingStorage{cancel: cancel}, "images", 0, nil)
	store := &imageTaskContextCheckingStore{}
	tasks := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}
	task, err := tasks.Create(ctx, owner)
	require.NoError(t, err)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	result := json.RawMessage(`{"data":[{"b64_json":"` + b64 + `"}]}`)
	require.NoError(t, tasks.Complete(ctx, task.ID, http.StatusOK, result))
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	got, err := tasks.Get(context.Background(), owner, task.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusFailed, got.Status)
	require.Equal(t, http.StatusBadGateway, got.HTTPStatus)
	require.Contains(t, string(got.Error), "generation usage may already have been billed")
}
