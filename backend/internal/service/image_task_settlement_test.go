package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 任务完成 ⇔ 扣费成功：扣费在转存之后、标记完成之前执行；扣费失败时任务失败且不返回结果链接。
func TestImageTaskCompleteDeliveredSettlesBeforeCompletion(t *testing.T) {
	newTask := func(t *testing.T) (*ImageTaskService, *fakeImageStorage, ImageTaskOwner, string) {
		t.Helper()
		store := &imageTaskMemoryStore{}
		storage := &fakeImageStorage{}
		svc := NewImageTaskServiceWithUploader(store, NewImageResultUploader(storage, "images/", 0, nil), time.Hour, time.Minute)
		owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
		created, err := svc.Create(context.Background(), owner)
		require.NoError(t, err)
		return svc, storage, owner, created.ID
	}
	result := json.RawMessage(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(pngBytes) + `"}]}`)

	t.Run("settled", func(t *testing.T) {
		svc, storage, owner, id := newTask(t)
		var statusDuringSettle string
		delivered, err := svc.CompleteDelivered(context.Background(), id, http.StatusOK, result, nil, func(context.Context) error {
			task, getErr := svc.Get(context.Background(), owner, id)
			require.NoError(t, getErr)
			statusDuringSettle = task.Status
			return nil
		})
		require.NoError(t, err)
		require.True(t, delivered)
		require.Equal(t, ImageTaskStatusProcessing, statusDuringSettle, "扣费时任务尚未对外完成")
		require.Empty(t, storage.saved, "未传 uploader 时不转存")
		got, err := svc.Get(context.Background(), owner, id)
		require.NoError(t, err)
		require.Equal(t, ImageTaskStatusCompleted, got.Status)
	})

	t.Run("settle_failure_not_delivered", func(t *testing.T) {
		svc, storage, owner, id := newTask(t)
		uploader := NewImageResultUploader(storage, "images/", 0, nil)
		delivered, err := svc.CompleteDelivered(context.Background(), id, http.StatusOK, result, uploader, func(context.Context) error {
			return errors.New("ledger unavailable")
		})
		require.ErrorIs(t, err, ErrImageTaskBillingUnavailable)
		require.False(t, delivered)
		got, err := svc.Get(context.Background(), owner, id)
		require.NoError(t, err)
		require.Equal(t, ImageTaskStatusFailed, got.Status)
		require.Equal(t, http.StatusServiceUnavailable, got.HTTPStatus)
		require.Empty(t, got.ImageURL, "扣费失败不能把结果交给用户")
		require.Contains(t, string(got.Error), "image_billing_unavailable")
	})

	t.Run("storage_failure_never_settles", func(t *testing.T) {
		svc, _, owner, id := newTask(t)
		failing := NewImageResultUploader(&fakeImageStorage{err: errors.New("bucket unreachable")}, "images/", 0, nil)
		settled := false
		delivered, err := svc.CompleteDelivered(context.Background(), id, http.StatusOK, result, failing, func(context.Context) error {
			settled = true
			return nil
		})
		require.NoError(t, err)
		require.False(t, delivered)
		require.False(t, settled, "转存失败不能扣费")
		got, err := svc.Get(context.Background(), owner, id)
		require.NoError(t, err)
		require.Equal(t, ImageTaskStatusFailed, got.Status)
	})
}

// flakyTerminalStore 在前 N 次终态写入时失败，模拟扣费成功后数据库/Redis 短暂不可写。
type flakyTerminalStore struct {
	imageTaskMemoryStore
	failTerminal int
}

func (s *flakyTerminalStore) Save(ctx context.Context, task *ImageTaskRecord, ttl time.Duration) error {
	if task.Status != ImageTaskStatusProcessing && s.failTerminal > 0 {
		s.failTerminal--
		return errors.New("terminal save unavailable")
	}
	return s.imageTaskMemoryStore.Save(ctx, task, ttl)
}

// R5：已扣费后结果保存连续失败（超过单次 finish 的 4 次重试）仍要继续重试，最终交付结果，而不是丢失。
func TestImageTaskDeliveredResultPersistRetriesAfterCapture(t *testing.T) {
	old := deliveredResultPersistDelays
	deliveredResultPersistDelays = []time.Duration{0, time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { deliveredResultPersistDelays = old })

	store := &flakyTerminalStore{}
	svc := NewImageTaskServiceWithOptions(store, time.Hour, time.Minute)
	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
	created, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)
	store.failTerminal = 6 // 超过单次 finish 的 4 次重试
	captures := 0
	delivered, err := svc.CompleteDelivered(context.Background(), created.ID, http.StatusOK,
		json.RawMessage(`{"data":[{"url":"https://cdn.test/a.png"}]}`), nil, func(context.Context) error { captures++; return nil })
	require.NoError(t, err)
	require.True(t, delivered)
	require.Equal(t, 1, captures, "落库重试不能重复扣费")
	got, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusCompleted, got.Status)
	require.NotEmpty(t, got.Result)
}
