package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"go.uber.org/zap"
)

// ImageObjectCleanupRecord 只记录本服务上传的对象和加密存储快照，不保存图片或用户请求。
// @project-doc docs/domains/media_tasks.md#async_image_object_retention
type ImageObjectCleanupRecord struct {
	ID                string
	ObjectKey         string
	StorageCiphertext string
	ExpiresAt         time.Time
	LeaseToken        string
	Attempts          int
}

var ErrImageObjectCleanupLeaseLost = errors.New("image object cleanup lease lost")

// ImageObjectCleanupStore 在上传前登记到期清理，重启和多实例通过数据库租约继续执行。
type ImageObjectCleanupStore interface {
	Check(context.Context) error
	Schedule(context.Context, ImageObjectCleanupRecord) (time.Time, error)
	ClaimDue(context.Context, time.Time, int, time.Duration) ([]ImageObjectCleanupRecord, error)
	Complete(context.Context, string, string) error
	Retry(context.Context, string, string, time.Time, string) error
}

// ImageObjectDeleter 仅删除确切对象键，清理程序不枚举或清空存储桶。
type ImageObjectDeleter interface {
	Delete(context.Context, string) error
}

// imageStorageWithDeadline 防止上传重试把签名链接延长到对象删除期限之后。
type imageStorageWithDeadline interface {
	SaveUntil(context.Context, string, string, []byte, time.Time) (string, error)
}

// ImageObjectRetention 固定上传时的加密存储快照，切换桶或关闭生成后仍能清理旧对象。
type ImageObjectRetention struct {
	store            ImageObjectCleanupStore
	encryptor        SecretEncryptor
	factory          ImageStorageFactory
	stableEncryption bool
}

type imageObjectCleanupSnapshot struct {
	Storage   config.ImageStorageConfig `json:"storage"`
	ObjectKey string                    `json:"object_key"`
	ExpiresAt time.Time                 `json:"expires_at"`
}

type retainedImageStorage struct {
	ImageStorage
	manager *ImageObjectRetention
	config  config.ImageStorageConfig
}

var generatedImageObjectName = regexp.MustCompile(`^imgtask_[a-f0-9]{32}-[0-9]+\.(png|jpg|webp|gif)$`)

func NewImageObjectRetention(store ImageObjectCleanupStore, encryptor SecretEncryptor, factory ImageStorageFactory, stableEncryption bool) *ImageObjectRetention {
	return &ImageObjectRetention{store: store, encryptor: encryptor, factory: factory, stableEncryption: stableEncryption}
}

// Wrap 不改变原厂商接口；仅真实生成对象进入持久清理，连接探针继续由原存储自行删除。
func (m *ImageObjectRetention) Wrap(cfg *config.ImageStorageConfig, storage ImageStorage) ImageStorage {
	return &retainedImageStorage{ImageStorage: storage, manager: m, config: *cfg}
}

// imageCleanupPlainPrefix 标记不含任何凭据的本地存储清理快照：明文保存，不依赖加密密钥，
// 重启后自动生成的临时密钥变化也不影响清理。
const imageCleanupPlainPrefix = "plain:"

// ready 检查清理队列可用；requireStableKey 为真（S3 快照含凭据需要加密）时还要求固定加密密钥。
func (m *ImageObjectRetention) ready(ctx context.Context, requireStableKey bool) error {
	if m == nil || m.store == nil || m.encryptor == nil || m.factory == nil {
		return errors.New("image object retention is unavailable")
	}
	if requireStableKey && !m.stableEncryption {
		// 自动生成的临时密钥不能恢复清理快照，必须在调用付费生成之前拒绝。
		return ErrSecretEncryptionKeyNotConfigured
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return m.store.Check(checkCtx)
}

func (s *retainedImageStorage) CheckReadiness(ctx context.Context) error {
	if err := s.manager.ready(ctx, !s.config.IsLocal()); err != nil {
		return err
	}
	if _, ok := s.ImageStorage.(ImageObjectDeleter); !ok {
		return errors.New("image storage does not support object deletion")
	}
	if _, ok := s.ImageStorage.(imageStorageWithDeadline); !ok {
		return errors.New("image storage does not support an absolute retention deadline")
	}
	checker, ok := s.ImageStorage.(interface{ CheckReadiness(context.Context) error })
	if !ok {
		return errors.New("image storage does not support readiness checks")
	}
	return checker.CheckReadiness(ctx)
}

func (s *retainedImageStorage) Save(ctx context.Context, key, contentType string, data []byte) (string, error) {
	if err := s.manager.ready(ctx, !s.config.IsLocal()); err != nil {
		return "", err
	}
	if err := s.config.ValidateRetentionHours(); err != nil {
		return "", err
	}
	if !validGeneratedImageObjectKey(s.config.Prefix, key) {
		return "", errors.New("image retention rejected an unowned object key")
	}
	// 先登记再上传：进程在 PutObject 响应前中断，仍能在到期后清理可能存在的对象。
	// 重试使用同一对象标识；仓储保留首次登记的到期时间，不延长文件寿命。
	expiresAt := time.Now().UTC().Truncate(time.Second).Add(time.Duration(s.config.EffectiveRetentionHours()) * time.Hour)
	snapshot := imageObjectCleanupSnapshot{Storage: s.config, ObjectKey: key, ExpiresAt: expiresAt}
	if s.config.IsLocal() {
		// 本地存储不需要对象存储凭据；清除可能来自 YAML 回落的 S3 字段，快照即可明文保存。
		snapshot.Storage.AccessKeyID, snapshot.Storage.SecretAccessKey = "", ""
	}
	plain, err := json.Marshal(snapshot)
	if err != nil {
		return "", errors.New("image cleanup snapshot encoding failed")
	}
	var ciphertext string
	if s.config.IsLocal() {
		ciphertext = imageCleanupPlainPrefix + string(plain)
	} else if ciphertext, err = s.manager.encryptor.Encrypt(string(plain)); err != nil {
		return "", errors.New("image cleanup snapshot encryption failed")
	}
	record := ImageObjectCleanupRecord{ID: imageObjectCleanupID(&s.config, key), ObjectKey: key, StorageCiphertext: ciphertext, ExpiresAt: expiresAt}
	registerCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	registeredExpiry, err := s.manager.store.Schedule(registerCtx, record)
	cancel()
	if err != nil {
		// 登记失败时不写入对象，防止生成无法跟踪的存储垃圾。
		return "", errors.New("image cleanup registration failed")
	}
	if !registeredExpiry.After(time.Now()) {
		return "", errors.New("image retention deadline has passed")
	}
	if storage, ok := s.ImageStorage.(imageStorageWithDeadline); ok {
		return storage.SaveUntil(ctx, key, contentType, data, registeredExpiry)
	}
	return "", errors.New("image storage does not support an absolute retention deadline")
}

// validGeneratedImageObjectKey 限定本服务生成的单个文件，拒绝任意目录、备份或手工上传对象。
func validGeneratedImageObjectKey(prefix, key string) bool {
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	return generatedImageObjectName.MatchString(strings.TrimPrefix(key, prefix))
}

func imageObjectCleanupID(cfg *config.ImageStorageConfig, key string) string {
	// 身份只使用目标位置，不包含凭据；同桶密钥轮换或传输重试不产生重复清理条目。
	target, _ := json.Marshal([]string{cfg.Endpoint, cfg.Bucket, key})
	if cfg.IsLocal() {
		// 本地目录与 S3 位置分开命名，S3 的既有登记 ID 保持不变。
		target, _ = json.Marshal([]string{config.ImageStorageBackendLocal, cfg.LocalDir, key})
	}
	sum := sha256.Sum256(target)
	return hex.EncodeToString(sum[:])
}

// Sweep 每轮有界领取，不在对象删除成功之前删除清理证据；不会调用生成或账务。
func (m *ImageObjectRetention) Sweep(ctx context.Context, now time.Time) error {
	if m == nil || m.store == nil {
		return errors.New("image object cleanup store is unavailable")
	}
	// 一次仅领取一个对象，整轮有界，避免慢存储占满租约或阻塞关闭服务。
	ctx, stop := context.WithTimeout(ctx, 45*time.Second)
	defer stop()
	started := time.Now()
	for i := 0; i < 100 && ctx.Err() == nil; i++ {
		current := now.Add(time.Since(started))
		records, err := m.store.ClaimDue(ctx, current, 1, 2*time.Minute)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}
		record := records[0]
		code := m.deleteObject(ctx, current, record)
		if code == "" {
			if err := m.store.Complete(ctx, record.ID, record.LeaseToken); err == nil {
				continue
			} else if errors.Is(err, ErrImageObjectCleanupLeaseLost) {
				continue
			}
			code = "cleanup_complete_failed"
		}
		// 清理失败的日志只含对象登记 ID 和固定分类，不输出对象访问链接或加密快照。
		logger.L().Warn("image_object.cleanup_failed", zap.String("object_id", record.ID), zap.String("code", code))
		retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		err = m.store.Retry(retryCtx, record.ID, record.LeaseToken, now.Add(time.Since(started)).Add(imageObjectCleanupBackoff(record.Attempts)), code)
		cancel()
		if err != nil && !errors.Is(err, ErrImageObjectCleanupLeaseLost) {
			return errors.New("image object cleanup retry persistence failed")
		}
	}
	return ctx.Err()
}

func (m *ImageObjectRetention) deleteObject(ctx context.Context, now time.Time, record ImageObjectCleanupRecord) string {
	var plain string
	localSnapshot := strings.HasPrefix(record.StorageCiphertext, imageCleanupPlainPrefix)
	if localSnapshot {
		plain = strings.TrimPrefix(record.StorageCiphertext, imageCleanupPlainPrefix)
	} else {
		if m.encryptor == nil || m.factory == nil || !m.stableEncryption {
			return "cleanup_storage_unavailable"
		}
		decrypted, err := m.encryptor.Decrypt(record.StorageCiphertext)
		if err != nil {
			return "cleanup_decrypt_failed"
		}
		plain = decrypted
	}
	if m.factory == nil {
		return "cleanup_storage_unavailable"
	}
	var snapshot imageObjectCleanupSnapshot
	// 明文快照只允许描述本地存储，不能借此绕过加密指向带凭据的 S3 目标。
	if json.Unmarshal([]byte(plain), &snapshot) != nil || (localSnapshot && !snapshot.Storage.IsLocal()) ||
		snapshot.ObjectKey != record.ObjectKey || !snapshot.ExpiresAt.Equal(record.ExpiresAt) ||
		record.ExpiresAt.After(now) || snapshot.ExpiresAt.IsZero() ||
		!validGeneratedImageObjectKey(snapshot.Storage.Prefix, record.ObjectKey) ||
		imageObjectCleanupID(&snapshot.Storage, record.ObjectKey) != record.ID {
		return "cleanup_snapshot_invalid"
	}
	deleteCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	storage, err := m.factory(deleteCtx, &snapshot.Storage)
	if err != nil {
		return "cleanup_storage_unavailable"
	}
	deleter, ok := storage.(ImageObjectDeleter)
	if !ok {
		return "cleanup_storage_unavailable"
	}
	if err := deleter.Delete(deleteCtx, record.ObjectKey); err != nil {
		_, code, _ := ImageStorageFailureDetails(err)
		switch code {
		case imageStorageAccessDenied:
			return "cleanup_access_denied"
		case imageStorageTimeout:
			return "cleanup_timeout"
		default:
			return "cleanup_delete_failed"
		}
	}
	return ""
}

func imageObjectCleanupBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 6 {
		return time.Hour
	}
	return time.Minute * time.Duration(1<<(attempts-1))
}

// 保留编译期接口校验，防止后续存储装饰器绕过上传登记。
var _ ImageStorage = (*retainedImageStorage)(nil)
