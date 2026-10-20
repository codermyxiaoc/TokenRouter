//go:build unit

package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 新任务开关只控制受理，不得停止旧图片到期清理；关闭进程必须等待删除调用退出。
func TestImageTaskRetentionStartsImmediatelyWhenGenerationDisabledAndStopWaits(t *testing.T) {
	manager, queue, storage := cleanupTestManager(t)
	cfg := cleanupTestConfig()
	cfg.Enabled = false
	expiry := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	plain, err := json.Marshal(imageObjectCleanupSnapshot{Storage: cfg, ObjectKey: cleanupTestObjectKey, ExpiresAt: expiry})
	require.NoError(t, err)
	encrypted, err := manager.encryptor.Encrypt(string(plain))
	require.NoError(t, err)
	id := imageObjectCleanupID(&cfg, cleanupTestObjectKey)
	queue.records[id] = ImageObjectCleanupRecord{
		ID: id, ObjectKey: cleanupTestObjectKey, StorageCiphertext: encrypted, ExpiresAt: expiry,
	}

	entered, cancelled, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	storage.onDelete = func(ctx context.Context, _ string) error {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return ctx.Err()
	}
	taskStore := &imageTaskRuntimeStore{maintained: make(chan struct{}, 2)}
	svc := NewImageTaskServiceWithResolver(taskStore, func() (*ImageResultUploader, bool) { return nil, false }, 0, 0)
	svc.objectRetention = manager
	t.Cleanup(func() {
		unblock()
		svc.Stop()
	})
	require.False(t, svc.Enabled())
	svc.Start()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("禁用新生成时，启动后仍应立即清理到期对象")
	}
	go func() {
		svc.Stop()
		close(stopped)
	}()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Stop 没有取消正在执行的对象清理")
	}
	select {
	case <-stopped:
		t.Fatal("Stop 在存储删除尚未退出时提前返回")
	default:
	}
	unblock()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("清理退出后 Stop 未完成")
	}
	require.Equal(t, []string{cleanupTestObjectKey}, storage.deleted)
	require.Len(t, queue.saved(), 1, "取消删除时保留记录，供下次启动继续清理")
	svc.Stop()
}
