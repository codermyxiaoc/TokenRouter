package repository

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func newLocalImageStorageForTest(t *testing.T, maxTotal int64) (*LocalImageStorage, string) {
	t.Helper()
	dir := t.TempDir()
	storage, err := NewLocalImageStorage(&config.ImageStorageConfig{
		Backend: config.ImageStorageBackendLocal, LocalDir: dir, Prefix: "images/",
		PresignExpiry: 24, RetentionHours: 24, LocalMaxTotalBytes: maxTotal,
	}, "test-secret", "https://site.example.com/")
	require.NoError(t, err)
	return storage, dir
}

func parseLocalImageURL(t *testing.T, raw string) (string, int64, string) {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(parsed.Path, service.LocalImageFileRoutePrefix))
	expires, err := strconv.ParseInt(parsed.Query().Get("exp"), 10, 64)
	require.NoError(t, err)
	return strings.TrimPrefix(parsed.Path, service.LocalImageFileRoutePrefix), expires, parsed.Query().Get("sig")
}

// 写入、签名下载、删除的完整闭环；签名被篡改或过期时统一拒绝。
func TestLocalImageStorageSaveOpenDelete(t *testing.T) {
	storage, dir := newLocalImageStorageForTest(t, 0)
	ctx := context.Background()
	require.NoError(t, storage.CheckReadiness(ctx))

	key := "images/imgtask_0123456789abcdef0123456789abcdef-0.png"
	deadline := time.Now().Add(time.Hour)
	link, err := storage.SaveUntil(ctx, key, "image/png", imageStorageProbePNG, deadline)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(link, "https://site.example.com"+service.LocalImageFileRoutePrefix))
	_, err = os.Stat(filepath.Join(dir, "images", "imgtask_0123456789abcdef0123456789abcdef-0.png"))
	require.NoError(t, err)

	gotKey, expires, sig := parseLocalImageURL(t, link)
	require.Equal(t, key, gotKey)
	require.LessOrEqual(t, expires, deadline.Unix(), "链接不能晚于登记的到期时间")
	file, info, err := storage.OpenLocalImage(gotKey, expires, sig)
	require.NoError(t, err)
	require.Equal(t, int64(len(imageStorageProbePNG)), info.Size())
	_ = file.Close()

	_, _, err = storage.OpenLocalImage(gotKey, expires, strings.Repeat("0", len(sig)))
	require.ErrorIs(t, err, service.ErrLocalImageNotFound)
	_, _, err = storage.OpenLocalImage(gotKey, time.Now().Add(-time.Second).Unix(), sig)
	require.ErrorIs(t, err, service.ErrLocalImageNotFound)

	require.NoError(t, storage.Delete(ctx, key))
	require.NoError(t, storage.Delete(ctx, key), "已不存在视为成功")
	_, _, err = storage.OpenLocalImage(gotKey, expires, sig)
	require.ErrorIs(t, err, service.ErrLocalImageNotFound)
}

// 任何跳出根目录或平台相关的路径写法都被拒绝。
func TestLocalImageStorageRejectsUnsafeKeys(t *testing.T) {
	storage, _ := newLocalImageStorageForTest(t, 0)
	for _, key := range []string{"", "../x.png", "images/../../x.png", "/etc/passwd", "images\\x.png", "C:/x.png", "images//x.png", "images/./x.png"} {
		_, err := storage.Save(context.Background(), key, "image/png", imageStorageProbePNG)
		require.Error(t, err, key)
	}
}

// 配置容量上限后，超额写入失败且不会留下文件。
func TestLocalImageStorageCapacityLimit(t *testing.T) {
	storage, dir := newLocalImageStorageForTest(t, int64(len(imageStorageProbePNG)))
	ctx := context.Background()
	_, err := storage.Save(ctx, "images/a.png", "image/png", imageStorageProbePNG)
	require.NoError(t, err)
	_, err = storage.Save(ctx, "images/b.png", "image/png", imageStorageProbePNG)
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(dir, "images", "b.png"))
	require.True(t, os.IsNotExist(statErr))
}

// P2-03：两个实例共享同一目录时不能越过容量上限（上限 100，各写 60，第二次必须失败）。
func TestLocalImageStorageSharedDirectoryCapacity(t *testing.T) {
	dir := t.TempDir()
	newInstance := func() *LocalImageStorage {
		storage, err := NewLocalImageStorage(&config.ImageStorageConfig{Backend: config.ImageStorageBackendLocal, LocalDir: dir, Prefix: "images/",
			PresignExpiry: 24, RetentionHours: 24, LocalMaxTotalBytes: 100}, "test-secret", "https://site.example.com")
		require.NoError(t, err)
		return storage
	}
	first, second := newInstance(), newInstance()
	payload := make([]byte, 60)
	_, err := first.Save(context.Background(), "images/a.png", "image/png", payload)
	require.NoError(t, err)
	_, err = second.Save(context.Background(), "images/b.png", "image/png", payload)
	require.Error(t, err, "第二个实例必须看到第一个实例的占用")
	used, err := directorySize(dir)
	require.NoError(t, err)
	require.LessOrEqual(t, used, int64(100))
}

// P2-03 并发版：多个实例同时写入，最终占用不超过上限。
func TestLocalImageStorageConcurrentInstancesRespectCapacity(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			storage, err := NewLocalImageStorage(&config.ImageStorageConfig{Backend: config.ImageStorageBackendLocal, LocalDir: dir, Prefix: "images/",
				PresignExpiry: 24, RetentionHours: 24, LocalMaxTotalBytes: 300}, "test-secret", "https://site.example.com")
			if err != nil {
				return
			}
			_, _ = storage.Save(context.Background(), fmt.Sprintf("images/%d.png", i), "image/png", make([]byte, 60))
		}(i)
	}
	wg.Wait()
	used, err := directorySize(dir)
	require.NoError(t, err)
	require.LessOrEqual(t, used, int64(300))
}

// P2-04：重复覆盖同一对象只计增量，不能虚报存储已满（上限 180，同键 60 字节反复覆盖都应成功）。
func TestLocalImageStorageOverwriteDoesNotDoubleCount(t *testing.T) {
	storage, dir := newLocalImageStorageForTest(t, 180)
	payload := make([]byte, 60)
	for i := range 6 {
		_, err := storage.Save(context.Background(), "images/same.png", "image/png", payload)
		require.NoError(t, err, "第 %d 次覆盖", i+1)
	}
	used, err := directorySize(dir)
	require.NoError(t, err)
	require.Equal(t, int64(60), used)
	// 覆盖为更大的内容只计增量：60 -> 170 在上限内，-> 190 超限。
	_, err = storage.Save(context.Background(), "images/same.png", "image/png", make([]byte, 170))
	require.NoError(t, err)
	_, err = storage.Save(context.Background(), "images/same.png", "image/png", make([]byte, 190))
	require.Error(t, err)
}

// 崩溃遗留的过期锁会被接管，不会让写入永久阻塞。
func TestLocalImageStorageRecoversStaleCapacityLock(t *testing.T) {
	storage, dir := newLocalImageStorageForTest(t, 1000)
	lock := filepath.Join(dir, localImageCapacityLockName)
	require.NoError(t, os.WriteFile(lock, nil, 0o600))
	old := time.Now().Add(-time.Minute)
	require.NoError(t, os.Chtimes(lock, old, old))
	_, err := storage.Save(context.Background(), "images/a.png", "image/png", imageStorageProbePNG)
	require.NoError(t, err)
	_, statErr := os.Stat(lock)
	require.True(t, os.IsNotExist(statErr), "写入完成后释放锁")
}

// 没有可用站点地址时拒绝就绪，避免生成后才发现无法给出下载链接。
func TestLocalImageStorageRequiresBaseURL(t *testing.T) {
	storage, err := NewLocalImageStorage(&config.ImageStorageConfig{Backend: config.ImageStorageBackendLocal, LocalDir: t.TempDir(), Prefix: "images/"}, "seed", "")
	require.NoError(t, err)
	require.Error(t, storage.CheckReadiness(context.Background()))
	_, err = NewLocalImageStorage(&config.ImageStorageConfig{Backend: config.ImageStorageBackendLocal, LocalDir: t.TempDir()}, "", "https://x")
	require.Error(t, err, "缺少签名密钥时不能构造")
}

// R9：锁被接管后，旧持有者解锁不能删除新持有者的锁。
func TestLocalImageLockOwnershipAfterTakeover(t *testing.T) {
	lock := filepath.Join(t.TempDir(), localImageCapacityLockName)
	ctx := context.Background()
	unlockOld, err := acquireLocalImageLock(ctx, lock)
	require.NoError(t, err)
	old := time.Now().Add(-time.Minute)
	require.NoError(t, os.Chtimes(lock, old, old)) // 模拟旧持有者慢到超过期限
	unlockNew, err := acquireLocalImageLock(ctx, lock)
	require.NoError(t, err, "过期锁可被接管")
	unlockOld() // 旧持有者迟到的解锁
	_, statErr := os.Stat(lock)
	require.NoError(t, statErr, "旧持有者不能删除新持有者的锁")
	third, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err = acquireLocalImageLock(third, lock)
	require.Error(t, err, "新持有者仍在持有期间第三者不能进入")
	unlockNew()
	_, statErr = os.Stat(lock)
	require.True(t, os.IsNotExist(statErr))
}

// 本地目录重签：对象仍在时签发可下载的新链接，期限不超过请求截止；对象已删除或截止已过时报错，调用方保留原链接。
func TestLocalImageStorageSignObjectURL(t *testing.T) {
	storage, _ := newLocalImageStorageForTest(t, 0)
	ctx := context.Background()
	key := "images/imgtask_0123456789abcdef0123456789abcdef-0.png"
	_, err := storage.SaveUntil(ctx, key, "image/png", imageStorageProbePNG, time.Now().Add(time.Hour))
	require.NoError(t, err)

	deadline := time.Now().Add(30 * time.Minute)
	link, err := storage.SignObjectURL(ctx, key, deadline)
	require.NoError(t, err)
	gotKey, expires, sig := parseLocalImageURL(t, link)
	require.Equal(t, key, gotKey)
	require.LessOrEqual(t, expires, deadline.Unix())
	file, _, err := storage.OpenLocalImage(gotKey, expires, sig)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	_, err = storage.SignObjectURL(ctx, key, time.Now().Add(-time.Second))
	require.Error(t, err, "截止已过不签发")
	require.NoError(t, storage.Delete(ctx, key))
	_, err = storage.SignObjectURL(ctx, key, deadline)
	require.Error(t, err, "对象已删除不签发")
}
