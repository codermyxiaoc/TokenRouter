package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/servertiming"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

// S3ImageStorage 用 S3 兼容对象存储实现 service.ImageStorage。
type S3ImageStorage struct {
	client        *s3.Client
	bucket        string
	publicBaseURL string
	presignExpiry time.Duration
	prefix        string

	// 探测结果只属于此配置快照；上传失败增加代次，阻止旧探测重新填入成功缓存。
	readinessMu         sync.Mutex
	readinessExpiresAt  time.Time
	readinessGeneration uint64
	readinessPending    *imageStorageReadinessCheck
}

var _ service.ImageStorage = (*S3ImageStorage)(nil)
var _ service.ImageObjectDeleter = (*S3ImageStorage)(nil)

const (
	imageStorageReadinessTTL     = 30 * time.Second
	imageStorageReadinessTimeout = 15 * time.Second
	imageStorageCleanupTimeout   = 5 * time.Second
)

type imageStorageReadinessCheck struct {
	done chan struct{}
	err  error
}

// 探针使用有效的一像素 PNG，不上传用户内容，也不覆盖任何已有生成对象。
var imageStorageProbePNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x04\x00\x00\x00\xb5\x1c\x0c\x02\x00\x00\x00\x0bIDAT\x78\xda\x63\xfc\xff\x1f\x00\x03\x03\x02\x00\xef\xbf\x69\xdb\x00\x00\x00\x00IEND\xae\x42\x60\x82")

// TestConnection 始终真实验证目标前缀的写入、读取、签名生成与清理，不以 HeadBucket 代替可写性。
func (s *S3ImageStorage) TestConnection(ctx context.Context) error {
	testCtx, cancel := context.WithTimeout(ctx, imageStorageReadinessTimeout)
	defer cancel()
	err := s.testReadWrite(testCtx)
	if err != nil {
		s.invalidateReadiness()
	}
	return err
}

// CheckReadiness 合并并发探测并短暂缓存成功；取消的请求立即停止等待，不取消其他请求共用的检查。
func (s *S3ImageStorage) CheckReadiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.readinessMu.Lock()
	if time.Now().Before(s.readinessExpiresAt) {
		s.readinessMu.Unlock()
		return ctx.Err()
	}
	check := s.readinessPending
	if check == nil {
		check = &imageStorageReadinessCheck{done: make(chan struct{})}
		s.readinessPending = check
		generation := s.readinessGeneration
		go func() {
			testCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), imageStorageReadinessTimeout)
			defer cancel()
			err := s.testReadWrite(testCtx)
			s.readinessMu.Lock()
			if err == nil && generation != s.readinessGeneration {
				err = errors.New("S3 readiness invalidated during verification")
			}
			check.err = err
			if err == nil {
				s.readinessExpiresAt = time.Now().Add(imageStorageReadinessTTL)
			}
			s.readinessPending = nil
			close(check.done)
			s.readinessMu.Unlock()
		}()
	}
	s.readinessMu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-check.done:
		// 同时收到完成与取消时，仍遵守本调用者的截止时间。
		if err := ctx.Err(); err != nil {
			return err
		}
		return check.err
	}
}

func (s *S3ImageStorage) invalidateReadiness() {
	s.readinessMu.Lock()
	s.readinessExpiresAt = time.Time{}
	s.readinessGeneration++
	s.readinessMu.Unlock()
}

// testReadWrite 只读取新建探针对象；清理使用独立预算，主请求超时也不会直接遗留探针。
func (s *S3ImageStorage) testReadWrite(ctx context.Context) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := s.prefix + ".readiness-" + uuid.NewString() + ".png"
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), imageStorageCleanupTimeout)
		defer cancel()
		_, cleanupErr := s.client.DeleteObject(cleanupCtx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
		if err == nil && cleanupErr != nil {
			err = fmt.Errorf("S3 readiness cleanup: %w", cleanupErr)
		}
	}()
	if _, err = s.Save(ctx, key, "image/png", imageStorageProbePNG); err != nil {
		return fmt.Errorf("S3 readiness write: %w", err)
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return fmt.Errorf("S3 readiness read: %w", err)
	}
	defer func() { _ = result.Body.Close() }()
	// 读取严格有界，错误端点返回大正文也不能扩大探测内存占用。
	data, err := io.ReadAll(io.LimitReader(result.Body, int64(len(imageStorageProbePNG)+1)))
	if err != nil {
		return fmt.Errorf("S3 readiness read body: %w", err)
	}
	if !bytes.Equal(data, imageStorageProbePNG) {
		return errors.New("S3 readiness read returned unexpected content")
	}
	return nil
}

// NewS3ImageStorage 依据配置构造 S3 图片存储（调用方应先确认 cfg.Active()）。
func NewS3ImageStorage(ctx context.Context, cfg *config.ImageStorageConfig) (*S3ImageStorage, error) {
	if err := cfg.ValidatePresignExpiry(); err != nil {
		return nil, err
	}
	if err := cfg.ValidateRetentionHours(); err != nil {
		return nil, err
	}
	store, err := NewS3BackupStoreFactory()(ctx, &service.BackupS3Config{
		Endpoint:        cfg.Endpoint,
		Region:          cfg.Region,
		AccessKeyID:     cfg.AccessKeyID,
		SecretAccessKey: cfg.SecretAccessKey,
		ForcePathStyle:  cfg.ForcePathStyle,
	})
	if err != nil {
		return nil, err
	}

	expiryHours := cfg.PresignExpiry
	// 先归一化旧配置的非正数，避免极小负数转换为 Duration 时溢出为正。
	if expiryHours <= 0 {
		expiryHours = config.ImageStorageDefaultPresignExpiryHours
	}
	// 链接期限不得长于新上传对象承诺的保存时间；公开直链仍由对象删除控制访问。
	if retentionHours := cfg.EffectiveRetentionHours(); expiryHours > retentionHours {
		expiryHours = retentionHours
	}
	expiry := time.Duration(expiryHours) * time.Hour

	return &S3ImageStorage{
		client:        store.(*S3BackupStore).client,
		bucket:        cfg.Bucket,
		publicBaseURL: strings.TrimRight(cfg.PublicBaseURL, "/"),
		presignExpiry: expiry,
		prefix:        cfg.Prefix,
	}, nil
}

// Save 上传图片字节，返回可访问 URL：配了 public_base_url 则返回公开直链，否则返回 presigned 临时链接。
func (s *S3ImageStorage) Save(ctx context.Context, key, contentType string, data []byte) (string, error) {
	return s.save(ctx, key, contentType, data, time.Time{})
}

// SaveUntil 使用首次登记的绝对到期时间，上传和重试耗时不能延长图片链接寿命。
func (s *S3ImageStorage) SaveUntil(ctx context.Context, key, contentType string, data []byte, expiresAt time.Time) (string, error) {
	now := time.Now()
	if !expiresAt.After(now) {
		return "", errors.New("image object retention deadline has passed")
	}
	// 构造时已将签名期限制为配置签名期与保存期的较小值，此处再限制绝对截止时间。
	if configuredDeadline := now.Add(s.presignExpiry); configuredDeadline.Before(expiresAt) {
		expiresAt = configuredDeadline
	}
	if !expiresAt.After(now) {
		return "", errors.New("image object signing deadline has passed")
	}
	return s.save(ctx, key, contentType, data, expiresAt)
}

func (s *S3ImageStorage) save(ctx context.Context, key, contentType string, data []byte, expiresAt time.Time) (string, error) {
	finish := servertiming.ObserveDependency(ctx, "s3")
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	finish()
	if err != nil {
		s.invalidateReadiness()
		return "", fmt.Errorf("S3 PutObject: %w", err)
	}

	expiry := s.presignExpiry
	if !expiresAt.IsZero() {
		expiry = time.Until(expiresAt)
		// S3 签名按整秒处理；不得让不足一秒的期限变成 SDK 的零值默认期限。
		if expiry < time.Second {
			return "", errors.New("image object retention deadline expired during upload")
		}
	}
	if s.publicBaseURL != "" {
		return s.publicBaseURL + "/" + strings.TrimLeft(key, "/"), nil
	}

	presignClient := s3.NewPresignClient(s.client)
	result, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		s.invalidateReadiness()
		return "", fmt.Errorf("presign url: %w", err)
	}
	if !expiresAt.IsZero() && !imageStorageSignedURLWithinDeadline(result.URL, expiresAt) {
		// 即使签名耗时跨越整秒边界，也不能返回比首次登记期限更长的链接。
		return "", errors.New("image object signed URL exceeds retention deadline")
	}
	return result.URL, nil
}

func imageStorageSignedURLWithinDeadline(rawURL string, expiresAt time.Time) bool {
	signedURL, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	signedAt, err := time.Parse("20060102T150405Z", signedURL.Query().Get("X-Amz-Date"))
	if err != nil {
		return false
	}
	seconds, err := strconv.ParseInt(signedURL.Query().Get("X-Amz-Expires"), 10, 64)
	if err != nil || seconds <= 0 || seconds > int64(config.ImageStorageMaxPresignExpiryHours)*3600 {
		return false
	}
	deadline := signedAt.Add(time.Duration(seconds) * time.Second)
	return deadline.After(time.Now()) && !deadline.After(expiresAt)
}

// SignObjectURL 为已上传对象重新签发下载链接，期限取配置签名期与 expiresAt 的较小值；公开直链直接返回。
func (s *S3ImageStorage) SignObjectURL(ctx context.Context, key string, expiresAt time.Time) (string, error) {
	if s.publicBaseURL != "" {
		return s.publicBaseURL + "/" + strings.TrimLeft(key, "/"), nil
	}
	now := time.Now()
	if configuredDeadline := now.Add(s.presignExpiry); configuredDeadline.Before(expiresAt) {
		expiresAt = configuredDeadline
	}
	expiry := time.Until(expiresAt)
	if expiry < time.Second {
		return "", errors.New("image object signing deadline has passed")
	}
	result, err := s3.NewPresignClient(s.client).PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("presign url: %w", err)
	}
	return result.URL, nil
}

// Delete 只删除已登记的完整对象键，不补前缀、不枚举桶，也不把目录当作删除范围。
// 存储端确认对象已不存在时视为成功；权限、服务故障和网络异常交由持久清理队列重试。
func (s *S3ImageStorage) Delete(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("S3 delete requires an exact non-empty object key")
	}
	finish := servertiming.ObserveDependency(ctx, "s3")
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	finish()
	if err == nil || imageStorageObjectAlreadyAbsent(err) {
		return nil
	}
	return fmt.Errorf("S3 DeleteObject: %w", err)
}

func imageStorageObjectAlreadyAbsent(err error) bool {
	var apiError interface{ ErrorCode() string }
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "NoSuchKey", "NoSuchObject", "NoSuchBucket":
			return true
		}
	}
	// 反代/CDN 的空正文或 HTML 404 不能证明对象不存在；SDK 合成的 NotFound 也不作为证据。
	// 保留错误及清理队列，避免临时路由故障导致永久遗留对象。
	return false
}
