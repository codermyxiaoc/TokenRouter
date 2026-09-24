package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 记录租约调用并注入一次终态失败，验证运行时不会把存储恢复变成二次生成。
type imageTaskRuntimeStore struct {
	mu               sync.Mutex
	record           *ImageTaskRecord
	terminalAttempts int
	terminalFailures int
	heartbeats       chan string
	maintained       chan struct{}
}

func (s *imageTaskRuntimeStore) Save(_ context.Context, task *ImageTaskRecord, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task.Status != ImageTaskStatusProcessing {
		s.terminalAttempts++
		if s.terminalAttempts <= s.terminalFailures {
			return errors.New("temporary database error")
		}
	}
	copy := *task
	s.record = &copy
	return nil
}

func (s *imageTaskRuntimeStore) Get(context.Context, string) (*ImageTaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil {
		return nil, ErrImageTaskNotFound
	}
	copy := *s.record
	return &copy, nil
}

func (s *imageTaskRuntimeStore) Heartbeat(_ context.Context, id, executionID string, leaseExpiresAt int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record != nil && s.record.ID == id && s.record.ExecutionID == executionID {
		s.record.LeaseExpiresAt = leaseExpiresAt
	}
	select {
	case s.heartbeats <- executionID:
	default:
	}
	return nil
}

func (s *imageTaskRuntimeStore) Maintain(context.Context, time.Time) error {
	select {
	case s.maintained <- struct{}{}:
	default:
	}
	return nil
}

func TestImageTaskRuntimeLeaseAndIdempotentStop(t *testing.T) {
	store := &imageTaskRuntimeStore{heartbeats: make(chan string, 5), maintained: make(chan struct{}, 5)}
	svc := NewImageTaskService(store)
	t.Cleanup(svc.Stop)
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}
	observation := &MediaTaskObservation{Platform: PlatformOpenAI, Model: "gpt-image-2", RequestID: "client:test"}
	task, err := svc.CreateObserved(context.Background(), owner, observation)
	require.NoError(t, err)
	svc.Start()
	svc.Start()
	select {
	case <-store.maintained:
	case <-time.After(time.Second):
		t.Fatal("补偿未启动")
	}
	stopLease := svc.KeepAlive(task.ID)
	select {
	case executionID := <-store.heartbeats:
		require.Equal(t, svc.runtime.executionID, executionID)
	case <-time.After(time.Second):
		t.Fatal("租约未续期")
	}
	stopLease()
	stopLease()
	record, err := store.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Greater(t, record.LeaseExpiresAt, time.Now().Unix())
	require.Greater(t, record.ExecutionDeadline, record.LeaseExpiresAt)
	require.Equal(t, task.ID, record.Observation.TaskID)
	require.Equal(t, "client:test", record.Observation.RequestID)
	require.Empty(t, observation.TaskID, "受理时复制元数据，不修改调用方对象")
	public, err := json.Marshal(task)
	require.NoError(t, err)
	require.NotContains(t, string(public), "execution_id")
	require.NotContains(t, string(public), "observation")
	svc.Stop()
	svc.Stop()
	svc.Start()
	svc.KeepAlive(task.ID)()
}

func TestImageTaskTerminalWriteRetriesWithoutReupload(t *testing.T) {
	store := &imageTaskRuntimeStore{terminalFailures: 1}
	svc := NewImageTaskService(store)
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}
	task, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)
	// 使用已经转存的结果，重试保存不触发上游、对象下载或扣费。
	result := json.RawMessage(`{"data":[{"url":"https://example.test/image.png"}]}`)
	require.NoError(t, svc.Complete(context.Background(), task.ID, http.StatusOK, result))
	require.Equal(t, 2, store.terminalAttempts)
	got, err := svc.Get(context.Background(), owner, task.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusCompleted, got.Status)
	require.JSONEq(t, string(result), string(got.Result))
}

func TestImageTaskTerminalRetryHonorsCancellation(t *testing.T) {
	store := &imageTaskRuntimeStore{terminalFailures: 100}
	svc := NewImageTaskService(store)
	task, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 9})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = svc.Fail(ctx, task.ID, 503, imageTaskErrorJSON("upstream_error", "failed"))
	require.ErrorIs(t, err, ErrImageTaskUnavailable)
	require.LessOrEqual(t, store.terminalAttempts, 1)
}
