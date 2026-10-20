package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
)

const (
	imageObjectCleanupDBBudget      = 3 * time.Second
	imageObjectCleanupMaxBatch      = 100
	imageObjectCleanupMaxLease      = 15 * time.Minute
	imageObjectCleanupMaxCiphertext = 64 << 10
)

type imageObjectCleanupStore struct{ db *sql.DB }

var _ service.ImageObjectCleanupStore = (*imageObjectCleanupStore)(nil)

// NewImageObjectCleanupStore 只维护本服务明确登记的清理对象，不查询桶内容或修改任务、账务。
func NewImageObjectCleanupStore(db *sql.DB) service.ImageObjectCleanupStore {
	return &imageObjectCleanupStore{db: db}
}

// Check 在付费生成之前验证清理表及必需列存在，不以连接成功代替迁移就绪。
func (s *imageObjectCleanupStore) Check(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("image object cleanup database is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, imageObjectCleanupDBBudget)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id,object_key,storage_ciphertext,expires_at,next_attempt_at,
 lease_token,lease_until,attempts,last_error_code FROM image_object_cleanup LIMIT 0`)
	if err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return rows.Err()
}

// Schedule 在上传前持久化首次到期时间和存储快照；重试同一对象不能延长保留期或替换删除目标。
func (s *imageObjectCleanupStore) Schedule(ctx context.Context, record service.ImageObjectCleanupRecord) (time.Time, error) {
	if s == nil || s.db == nil {
		return time.Time{}, errors.New("image object cleanup database is unavailable")
	}
	if !validImageObjectCleanupID(record.ID) || record.ObjectKey == "" || len(record.ObjectKey) > 1024 ||
		record.StorageCiphertext == "" || len(record.StorageCiphertext) > imageObjectCleanupMaxCiphertext || record.ExpiresAt.IsZero() {
		return time.Time{}, errors.New("invalid image object cleanup record")
	}
	ctx, cancel := context.WithTimeout(ctx, imageObjectCleanupDBBudget)
	defer cancel()
	// 冲突时仅对同一主键作无值变化更新，以原子返回首次期限，避免并发 INSERT 后 SELECT 看不到记录。
	var expiresAt time.Time
	err := s.db.QueryRowContext(ctx, `INSERT INTO image_object_cleanup
 (id,object_key,storage_ciphertext,expires_at,next_attempt_at)
 VALUES($1,$2,$3,$4,$4) ON CONFLICT(id) DO UPDATE SET id=image_object_cleanup.id
 RETURNING expires_at`,
		record.ID, record.ObjectKey, record.StorageCiphertext, record.ExpiresAt).Scan(&expiresAt)
	return expiresAt, err
}

// ClaimDue 原子领取已到期且可重试的对象；行锁跳过其它实例的在途领取，每轮设置新的租约令牌。
func (s *imageObjectCleanupStore) ClaimDue(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]service.ImageObjectCleanupRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("image object cleanup database is unavailable")
	}
	if limit <= 0 {
		return nil, nil
	}
	if now.IsZero() || lease <= 0 {
		return nil, errors.New("invalid image object cleanup lease")
	}
	if limit > imageObjectCleanupMaxBatch {
		limit = imageObjectCleanupMaxBatch
	}
	if lease > imageObjectCleanupMaxLease {
		lease = imageObjectCleanupMaxLease
	}
	ctx, cancel := context.WithTimeout(ctx, imageObjectCleanupDBBudget)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `WITH due AS (
 SELECT id FROM image_object_cleanup
 WHERE expires_at<=$1 AND next_attempt_at<=$1 AND (lease_until IS NULL OR lease_until<=$1)
 ORDER BY next_attempt_at,expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
)
UPDATE image_object_cleanup AS task SET lease_token=$3,lease_until=$4,
 attempts=LEAST(task.attempts::bigint+1,2147483647)::integer
FROM due WHERE task.id=due.id
RETURNING task.id,task.object_key,task.storage_ciphertext,task.expires_at,task.lease_token,task.attempts`,
		now, limit, uuid.NewString(), now.Add(lease))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var records []service.ImageObjectCleanupRecord
	for rows.Next() {
		var record service.ImageObjectCleanupRecord
		if err := rows.Scan(&record.ID, &record.ObjectKey, &record.StorageCiphertext, &record.ExpiresAt, &record.LeaseToken, &record.Attempts); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// Complete 仅删除当前租约的队列行；旧执行者不能把新一轮的待清理对象从队列移除。
func (s *imageObjectCleanupStore) Complete(ctx context.Context, id, token string) error {
	if !validImageObjectCleanupID(id) || token == "" {
		return service.ErrImageObjectCleanupLeaseLost
	}
	if s == nil || s.db == nil {
		return errors.New("image object cleanup database is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, imageObjectCleanupDBBudget)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `DELETE FROM image_object_cleanup WHERE id=$1 AND lease_token=$2`, id, token)
	return imageObjectCleanupLeaseResult(result, err)
}

// Retry 只修改当前租约的重试时间和固定错误码，保留原对象、加密快照及到期时间。
func (s *imageObjectCleanupStore) Retry(ctx context.Context, id, token string, next time.Time, code string) error {
	if !validImageObjectCleanupID(id) || token == "" {
		return service.ErrImageObjectCleanupLeaseLost
	}
	if s == nil || s.db == nil {
		return errors.New("image object cleanup database is unavailable")
	}
	if next.IsZero() {
		return errors.New("invalid image object cleanup retry time")
	}
	ctx, cancel := context.WithTimeout(ctx, imageObjectCleanupDBBudget)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `UPDATE image_object_cleanup
 SET next_attempt_at=$3,last_error_code=$4,lease_token='',lease_until=NULL
 WHERE id=$1 AND lease_token=$2`, id, token, next, safeImageObjectCleanupErrorCode(code))
	return imageObjectCleanupLeaseResult(result, err)
}

func imageObjectCleanupLeaseResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return service.ErrImageObjectCleanupLeaseLost
	}
	return nil
}

func validImageObjectCleanupID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, char := range id {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}

// 白名单以外全部使用通用分类，避免误把远端错误、密钥或签名链接写入长期队列。
func safeImageObjectCleanupErrorCode(code string) string {
	switch strings.TrimSpace(code) {
	case "cleanup_snapshot_invalid", "cleanup_decrypt_failed", "cleanup_storage_unavailable",
		"cleanup_delete_failed", "cleanup_access_denied", "cleanup_timeout", "cleanup_complete_failed", "cleanup_failed":
		return strings.TrimSpace(code)
	default:
		return "cleanup_failed"
	}
}
