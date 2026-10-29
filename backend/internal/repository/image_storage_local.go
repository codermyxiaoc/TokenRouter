package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// LocalImageStorage 把异步生图结果写入本机目录，实现与 S3 相同的保存、到期、删除和就绪探测语义。
// 路径统一经 filepath 处理，Windows 与 Linux 行为一致；写入先落临时文件再原子改名，
// 进程中断不会留下半张图片。下载通过本服务签名链接完成，不直接暴露目录。
// @project-doc docs/domains/media_tasks.md#async_image_local_storage
type LocalImageStorage struct {
	root     string
	baseURL  string
	secret   []byte
	expiry   time.Duration
	maxTotal int64

	readinessMu        sync.Mutex
	readinessExpiresAt time.Time
}

var (
	_ service.ImageStorage         = (*LocalImageStorage)(nil)
	_ service.ImageObjectDeleter   = (*LocalImageStorage)(nil)
	_ service.LocalImageFileOpener = (*LocalImageStorage)(nil)
)

const (
	// localImageCapacityLockName 是容量检查与写入的跨进程锁文件；多实例共享目录时共同遵守。
	localImageCapacityLockName = ".capacity.lock"
	// localImageCapacityLockStale 之后的锁视为持有者已崩溃，可被接管；单次写入远小于该时长。
	localImageCapacityLockStale = 30 * time.Second
)

// NewLocalImageStorage 依据配置构造本地存储；signingSeed 用于派生链接签名密钥，
// fallbackBaseURL 在未配置 public_base_url 时作为站点地址生成下载链接。
func NewLocalImageStorage(cfg *config.ImageStorageConfig, signingSeed, fallbackBaseURL string) (*LocalImageStorage, error) {
	if err := cfg.ValidatePresignExpiry(); err != nil {
		return nil, err
	}
	if err := cfg.ValidateRetentionHours(); err != nil {
		return nil, err
	}
	dir := strings.TrimSpace(cfg.LocalDir)
	if dir == "" {
		return nil, errors.New("image_storage.local_dir is required for local image storage")
	}
	root, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return nil, fmt.Errorf("resolve local image directory: %w", err)
	}
	if strings.TrimSpace(signingSeed) == "" {
		return nil, errors.New("local image storage requires a stable signing secret (jwt.secret)")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(strings.TrimSpace(fallbackBaseURL), "/")
	}
	expiryHours := cfg.PresignExpiry
	if expiryHours <= 0 {
		expiryHours = config.ImageStorageDefaultPresignExpiryHours
	}
	// 与 S3 一致：链接期限不得长于对象保存期限。
	if retentionHours := cfg.EffectiveRetentionHours(); expiryHours > retentionHours {
		expiryHours = retentionHours
	}
	return &LocalImageStorage{
		root:     root,
		baseURL:  baseURL,
		secret:   service.LocalImageSigningKey(signingSeed),
		expiry:   time.Duration(expiryHours) * time.Hour,
		maxTotal: max(int64(0), cfg.LocalMaxTotalBytes),
	}, nil
}

// resolve 把对象键映射到根目录内的绝对路径，拒绝绝对路径、反斜杠和任何跳出根目录的写法。
func (s *LocalImageStorage) resolve(key string) (string, error) {
	if key == "" || strings.ContainsAny(key, "\\\x00:") || strings.HasPrefix(key, "/") || path.Clean(key) != key || strings.Contains(key, "..") {
		return "", errors.New("invalid local image key")
	}
	full := filepath.Join(s.root, filepath.FromSlash(key))
	rel, err := filepath.Rel(s.root, full)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", errors.New("local image key escapes storage directory")
	}
	return full, nil
}

// Save 写入图片并返回默认期限的签名链接。
func (s *LocalImageStorage) Save(ctx context.Context, key, contentType string, data []byte) (string, error) {
	return s.save(ctx, key, data, time.Now().Add(s.expiry))
}

// SaveUntil 使用首次登记的绝对到期时间，签名链接不会晚于文件删除时间。
func (s *LocalImageStorage) SaveUntil(ctx context.Context, key, contentType string, data []byte, expiresAt time.Time) (string, error) {
	now := time.Now()
	if !expiresAt.After(now) {
		return "", errors.New("image object retention deadline has passed")
	}
	if configured := now.Add(s.expiry); configured.Before(expiresAt) {
		expiresAt = configured
	}
	return s.save(ctx, key, data, expiresAt)
}

func (s *LocalImageStorage) save(ctx context.Context, key string, data []byte, expiresAt time.Time) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.baseURL == "" {
		return "", errors.New("local image storage requires image_storage.public_base_url or server.frontend_url to build download links")
	}
	full, err := s.resolve(key)
	if err != nil {
		return "", err
	}
	if err := s.writeWithinCapacity(ctx, full, data); err != nil {
		return "", err
	}
	return service.SignLocalImageURL(s.baseURL, s.secret, key, expiresAt), nil
}

// writeWithinCapacity 在配置了容量上限时，于跨进程锁内按目录实际占用检查并写入：
// 覆盖同一对象只计增量，不会重复累计；多实例共享目录也不会越过上限。未配置上限时直接写入。
func (s *LocalImageStorage) writeWithinCapacity(ctx context.Context, full string, data []byte) error {
	if s.maxTotal <= 0 {
		if err := writeFileAtomic(full, data); err != nil {
			return fmt.Errorf("local image write: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(s.root, 0o750); err != nil {
		return fmt.Errorf("local image directory: %w", err)
	}
	unlock, err := acquireLocalImageLock(ctx, filepath.Join(s.root, localImageCapacityLockName))
	if err != nil {
		return fmt.Errorf("local image capacity lock: %w", err)
	}
	defer unlock()
	used, err := directorySize(s.root)
	if err != nil {
		return fmt.Errorf("measure local image directory: %w", err)
	}
	var existing int64
	if info, statErr := os.Stat(full); statErr == nil && info.Mode().IsRegular() {
		existing = info.Size()
	}
	// 按写入完成后的占用判断：覆盖同一对象只计增量（新 - 旧）。原子改名期间临时文件与旧文件
	// 会短暂并存，瞬时最多多出旧文件大小，写完即回落，不计入上限判断以免误报存储已满。
	if used-existing+int64(len(data)) > s.maxTotal {
		return fmt.Errorf("local image storage is full (%d of %d bytes used)", used, s.maxTotal)
	}
	if err := writeFileAtomic(full, data); err != nil {
		return fmt.Errorf("local image write: %w", err)
	}
	return nil
}

// acquireLocalImageLock 用 O_CREATE|O_EXCL 创建锁文件实现跨进程互斥，Windows 与 Linux 行为一致。
// 锁文件写入持有者唯一令牌：持有期间定期刷新修改时间，存活的慢持有者不会被判为过期；
// 释放时只删除仍属于自己的锁，被接管后旧持有者不会误删新持有者的锁。
// 超过 localImageCapacityLockStale 未刷新的锁视为崩溃遗留并被接管。
func acquireLocalImageLock(ctx context.Context, lockPath string) (func(), error) {
	token := uuid.NewString()
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, writeErr := file.WriteString(token)
			_ = file.Close()
			if writeErr != nil {
				_ = os.Remove(lockPath)
				return nil, writeErr
			}
			return startLocalImageLockHeartbeat(lockPath, token), nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > localImageCapacityLockStale {
			// 接管只删除观察到的那一份过期锁：若期间已被他人刷新或替换，令牌不同则放弃本次接管。
			if stale, readErr := os.ReadFile(lockPath); readErr == nil {
				if current, recheckErr := os.Stat(lockPath); recheckErr == nil && time.Since(current.ModTime()) > localImageCapacityLockStale {
					if again, againErr := os.ReadFile(lockPath); againErr == nil && string(again) == string(stale) {
						_ = os.Remove(lockPath)
					}
				}
			}
			continue
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// startLocalImageLockHeartbeat 在持有期间刷新锁文件时间，返回只释放自身令牌的解锁函数。
func startLocalImageLockHeartbeat(lockPath, token string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(localImageCapacityLockStale / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if owned, err := os.ReadFile(lockPath); err == nil && string(owned) == token {
					now := time.Now()
					_ = os.Chtimes(lockPath, now, now)
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
			if owned, err := os.ReadFile(lockPath); err == nil && string(owned) == token {
				_ = os.Remove(lockPath)
			}
		})
	}
}

// writeFileAtomic 先写同目录临时文件并刷盘，再改名覆盖目标；Windows 上 os.Rename 同样会替换已有文件。
func writeFileAtomic(full string, data []byte) (err error) {
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, full)
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		// 容量锁文件只存持有者令牌，不属于图片占用。
		if entry.Type().IsRegular() && entry.Name() != localImageCapacityLockName {
			info, infoErr := entry.Info()
			if infoErr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}

// Delete 只删除确切文件；文件已不存在视为成功。
func (s *LocalImageStorage) Delete(_ context.Context, key string) error {
	full, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("local image delete: %w", err)
	}
	return nil
}

// OpenLocalImage 校验签名与到期时间后只读打开普通文件，供下载路由流式返回。
func (s *LocalImageStorage) OpenLocalImage(key string, expiresUnix int64, signature string) (*os.File, os.FileInfo, error) {
	if !service.VerifyLocalImageSignature(s.secret, key, expiresUnix, signature, time.Now()) {
		return nil, nil, service.ErrLocalImageNotFound
	}
	full, err := s.resolve(key)
	if err != nil {
		return nil, nil, service.ErrLocalImageNotFound
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, nil, service.ErrLocalImageNotFound
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, service.ErrLocalImageNotFound
	}
	return file, info, nil
}

// TestConnection 真实验证目录可写、可读、可删，并确认能生成下载链接。
func (s *LocalImageStorage) TestConnection(ctx context.Context) error {
	return s.testReadWrite(ctx)
}

// CheckReadiness 短暂缓存成功结果，避免每个任务都写探针文件。
func (s *LocalImageStorage) CheckReadiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.readinessMu.Lock()
	defer s.readinessMu.Unlock()
	if time.Now().Before(s.readinessExpiresAt) {
		return nil
	}
	if err := s.testReadWrite(ctx); err != nil {
		return err
	}
	s.readinessExpiresAt = time.Now().Add(imageStorageReadinessTTL)
	return nil
}

func (s *LocalImageStorage) testReadWrite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.baseURL == "" {
		return errors.New("local image storage requires image_storage.public_base_url or server.frontend_url to build download links")
	}
	probe := filepath.Join(s.root, ".readiness-"+uuid.NewString()+".png")
	if err := writeFileAtomic(probe, imageStorageProbePNG); err != nil {
		return fmt.Errorf("local image readiness write: %w", err)
	}
	defer func() { _ = os.Remove(probe) }()
	file, err := os.Open(probe)
	if err != nil {
		return fmt.Errorf("local image readiness read: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, int64(len(imageStorageProbePNG)+1)))
	if err != nil {
		return fmt.Errorf("local image readiness read body: %w", err)
	}
	if !bytes.Equal(data, imageStorageProbePNG) {
		return errors.New("local image readiness read returned unexpected content")
	}
	return nil
}
