package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

// videoProtocolProfileRepository 用原生 SQL 保存模板版本；版本号在同一事务内按键名递增。
type videoProtocolProfileRepository struct{ db *sql.DB }

func NewVideoProtocolProfileRepository(db *sql.DB) service.VideoProtocolProfileRepository {
	return &videoProtocolProfileRepository{db: db}
}

const videoProtocolProfileColumns = `id,profile_key,version,name,spec,spec_hash,status,created_by,created_at`

func scanVideoProtocolProfile(row interface{ Scan(...any) error }) (*service.VideoProtocolProfile, error) {
	var profile service.VideoProtocolProfile
	var spec []byte
	var createdBy sql.NullInt64
	if err := row.Scan(&profile.ID, &profile.ProfileKey, &profile.Version, &profile.Name, &spec, &profile.SpecHash, &profile.Status, &createdBy, &profile.CreatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(spec, &profile.Spec); err != nil {
		return nil, err
	}
	if createdBy.Valid {
		id := createdBy.Int64
		profile.CreatedBy = &id
	}
	return &profile, nil
}

// CreateVersion 以咨询锁串行化同一键名的版本分配，避免并发保存得到相同版本号。
func (r *videoProtocolProfileRepository) CreateVersion(ctx context.Context, profile *service.VideoProtocolProfile) (*service.VideoProtocolProfile, error) {
	spec, err := json.Marshal(profile.Spec)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('video_protocol_profile:' || $1))`, profile.ProfileKey); err != nil {
		return nil, err
	}
	var createdBy any
	if profile.CreatedBy != nil {
		createdBy = *profile.CreatedBy
	}
	row := tx.QueryRowContext(ctx, `INSERT INTO video_protocol_profiles (profile_key,version,name,spec,spec_hash,status,created_by)
 SELECT $1, COALESCE(MAX(version),0)+1, $2, $3::jsonb, $4, $5, $6 FROM video_protocol_profiles WHERE profile_key=$1
 RETURNING `+videoProtocolProfileColumns, profile.ProfileKey, profile.Name, string(spec), profile.SpecHash, profile.Status, createdBy)
	saved, err := scanVideoProtocolProfile(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

// Get 读取指定版本；version 为 0 时读取当前启用版本。
func (r *videoProtocolProfileRepository) Get(ctx context.Context, key string, version int) (*service.VideoProtocolProfile, error) {
	query := `SELECT ` + videoProtocolProfileColumns + ` FROM video_protocol_profiles WHERE profile_key=$1 AND version=$2`
	args := []any{key, version}
	if version == 0 {
		query = `SELECT ` + videoProtocolProfileColumns + ` FROM video_protocol_profiles WHERE profile_key=$1 AND status='active'`
		args = []any{key}
	}
	profile, err := scanVideoProtocolProfile(r.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrVideoProtocolProfileNotFound
	}
	return profile, err
}

// List 按键名与版本倒序返回全部模板。
func (r *videoProtocolProfileRepository) List(ctx context.Context) ([]service.VideoProtocolProfile, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+videoProtocolProfileColumns+` FROM video_protocol_profiles ORDER BY profile_key, version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.VideoProtocolProfile{}
	for rows.Next() {
		profile, err := scanVideoProtocolProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *profile)
	}
	return out, rows.Err()
}

// Delete 删除指定版本；version 为 0 时删除该键名的全部版本。
func (r *videoProtocolProfileRepository) Delete(ctx context.Context, key string, version int) error {
	query, args := `DELETE FROM video_protocol_profiles WHERE profile_key=$1 AND version=$2`, []any{key, version}
	if version == 0 {
		query, args = `DELETE FROM video_protocol_profiles WHERE profile_key=$1`, []any{key}
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return service.ErrVideoProtocolProfileNotFound
	}
	return nil
}

// SetStatus 启用版本时先把同键名其它启用版本降为草稿（锁定版本的绑定仍可使用），保证部分唯一索引成立。
// 同一键名的状态变更用事务级咨询锁串行化：并发启用不同版本时后者等待前者提交，
// 最终恰好一个启用版本，而不是返回唯一约束冲突（23505）或死锁（40P01）。
func (r *videoProtocolProfileRepository) SetStatus(ctx context.Context, key string, version int, status string) error {
	_, err := retryPostgresDeadlock(ctx, "video_protocol_profile_set_status", 0, func() (struct{}, error) {
		return struct{}{}, r.setStatusOnce(ctx, key, version, status)
	})
	return err
}

func (r *videoProtocolProfileRepository) setStatusOnce(ctx context.Context, key string, version int, status string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// 与 CreateVersion 使用同一把锁，创建版本与切换状态也相互串行。
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('video_protocol_profile:' || $1))`, key); err != nil {
		return err
	}
	if status == service.VideoProtocolProfileActive {
		if _, err := tx.ExecContext(ctx, `UPDATE video_protocol_profiles SET status='draft' WHERE profile_key=$1 AND status='active' AND version<>$2`, key, version); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE video_protocol_profiles SET status=$3 WHERE profile_key=$1 AND version=$2`, key, version, status)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return service.ErrVideoProtocolProfileNotFound
	}
	return tx.Commit()
}
