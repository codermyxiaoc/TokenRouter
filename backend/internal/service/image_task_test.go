package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 后台任务已有独立生命周期，必须保留截止时间，同时继续携带请求关联值。
func TestImageTaskUpstreamPreservesExecutionDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(WithAsyncImageExecutionContext(context.Background()), time.Minute)
	defer cancel()
	upstream, release := detachUpstreamContext(ctx)
	defer release()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	actual, ok := upstream.Deadline()
	require.True(t, ok)
	require.Equal(t, deadline, actual)
	cancel()
	require.ErrorIs(t, upstream.Err(), context.Canceled)
}

// 没有实际图片的 2xx 不能登记为成功，也不能借异常扩展字段把大正文写入 Redis。
func TestImageTaskRejectsEmptyOrOversizedResult(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":[]}`, `{"data":[{}]}`, `{"data":[{"url":"https://example.test/a.png"}],"extra":"` + strings.Repeat("a", maxImageTaskStoredResultBytes) + `"}`} {
		store := &imageTaskMemoryStore{}
		svc := NewImageTaskServiceWithOptions(store, time.Hour, time.Minute)
		owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}
		task, err := svc.Create(context.Background(), owner)
		require.NoError(t, err)
		require.NoError(t, svc.Complete(context.Background(), task.ID, http.StatusOK, json.RawMessage(body)))
		got, err := svc.Get(context.Background(), owner, task.ID)
		require.NoError(t, err)
		require.Equal(t, ImageTaskStatusFailed, got.Status)
		require.Empty(t, got.Result)
	}
}

type imageTaskMemoryStore struct {
	task    *ImageTaskRecord
	ttl     time.Duration
	saveErr error
	getErr  error
}

func (s *imageTaskMemoryStore) Save(_ context.Context, task *ImageTaskRecord, ttl time.Duration) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	copy := *task
	s.task = &copy
	s.ttl = ttl
	return nil
}

func (s *imageTaskMemoryStore) Get(_ context.Context, _ string) (*ImageTaskRecord, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.task == nil {
		return nil, ErrImageTaskNotFound
	}
	copy := *s.task
	return &copy, nil
}

func TestImageTaskServiceLifecycleAndOwnership(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskServiceWithOptions(store, time.Hour, 10*time.Minute)
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}

	created, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusProcessing, created.Status)
	require.Equal(t, created.ID, created.TaskID)
	require.Equal(t, "image.generation.task", created.Object)
	require.Equal(t, time.Hour, store.ttl)
	require.Equal(t, owner.UserID, store.task.UserID)
	require.Equal(t, owner.APIKeyID, store.task.APIKeyID)

	_, err = svc.Get(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 10}, created.ID)
	require.ErrorIs(t, err, ErrImageTaskNotFound)

	result := json.RawMessage(`{"created":123,"data":[{"url":"https://example.test/image.png"}]}`)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, result))

	completed, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusCompleted, completed.Status)
	require.Equal(t, http.StatusOK, completed.HTTPStatus)
	require.Equal(t, "https://example.test/image.png", completed.ImageURL)
	require.JSONEq(t, string(result), string(completed.Result))
	require.NotNil(t, completed.CompletedAt)
}

func TestImageTaskServiceInvalidResultBecomesFailed(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskServiceWithOptions(store, time.Hour, time.Minute)
	created, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.NoError(t, err)

	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, json.RawMessage(`not-json`)))
	got, err := svc.Get(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2}, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusFailed, got.Status)
	require.Equal(t, http.StatusBadGateway, got.HTTPStatus)
	require.Contains(t, string(got.Error), "non-JSON")
}

func TestImageTaskServiceMapsStoreFailures(t *testing.T) {
	store := &imageTaskMemoryStore{saveErr: errors.New("redis down")}
	svc := NewImageTaskService(store)

	_, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.ErrorIs(t, err, ErrImageTaskUnavailable)
}
