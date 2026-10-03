package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type intelligenceRepository struct{ db *sql.DB }

// NewIntelligenceRepository 使用现有连接池，不为检测引入新的存储依赖。
func NewIntelligenceRepository(db *sql.DB) service.IntelligenceRepository {
	return &intelligenceRepository{db: db}
}

const intelligenceConfigColumns = `c.id,c.group_id,g.name,c.model,c.benchmark,c.base_url,c.api_key_id,c.protocol,c.reasoning_effort,c.service_tier,c.enabled,c.schedule_enabled,c.interval_minutes,c.next_run_at,c.created_at,c.updated_at`

type intelligenceScanner interface{ Scan(...any) error }

func scanIntelligenceConfig(row intelligenceScanner) (*service.IntelligenceConfig, error) {
	c := &service.IntelligenceConfig{}
	err := row.Scan(&c.ID, &c.GroupID, &c.GroupName, &c.Model, &c.Benchmark, &c.BaseURL, &c.APIKeyID, &c.Protocol, &c.ReasoningEffort, &c.ServiceTier, &c.Enabled, &c.ScheduleEnabled, &c.IntervalMinutes, &c.NextRunAt, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrIntelligenceNotFound
	}
	c.APIKeyConfigured = c.APIKeyID > 0
	return c, err
}

func (r *intelligenceRepository) ListConfigs(ctx context.Context) ([]service.IntelligenceConfig, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+intelligenceConfigColumns+` FROM intelligence_test_configs c JOIN groups g ON g.id=c.group_id ORDER BY g.sort_order,g.id,c.id LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]service.IntelligenceConfig, 0)
	for rows.Next() {
		c, err := scanIntelligenceConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *intelligenceRepository) GetConfig(ctx context.Context, id int64) (*service.IntelligenceConfig, error) {
	return scanIntelligenceConfig(r.db.QueryRowContext(ctx, `SELECT `+intelligenceConfigColumns+` FROM intelligence_test_configs c JOIN groups g ON g.id=c.group_id WHERE c.id=$1`, id))
}

// SaveConfig 与创建检测共用配置行锁，运行中的密钥、模型和目标地址不能被修改。
func (r *intelligenceRepository) SaveConfig(ctx context.Context, c *service.IntelligenceConfig) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if c.ID > 0 {
		if err = lockIntelligenceConfig(ctx, tx, c.ID); err != nil {
			return err
		}
		if err = checkIntelligenceIdle(ctx, tx, c.ID); err != nil {
			return err
		}
	}
	args := []any{c.GroupID, c.Model, c.Benchmark, c.BaseURL, c.APIKeyID, c.Protocol, c.ReasoningEffort, c.ServiceTier, c.Enabled, c.ScheduleEnabled, c.IntervalMinutes}
	if c.ID == 0 {
		// 配置总数与列表上限一致，串行化新增避免并发突破上限后出现隐藏收费配置。
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(29420261003)`); err != nil {
			return err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM intelligence_test_configs`).Scan(&count); err != nil {
			return err
		}
		if count >= 200 {
			return infraerrors.Conflict("INTELLIGENCE_CONFIG_LIMIT", "最多支持200条检测配置")
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO intelligence_test_configs(group_id,model,benchmark,base_url,api_key_id,protocol,reasoning_effort,service_tier,enabled,schedule_enabled,interval_minutes,next_run_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,CASE WHEN $10 THEN NOW()+($11::integer * INTERVAL '1 minute') ELSE NULL END) RETURNING id,created_at,updated_at,next_run_at`, args...).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt, &c.NextRunAt)
	} else {
		args = append(args, c.ID)
		err = tx.QueryRowContext(ctx, `UPDATE intelligence_test_configs SET group_id=$1,model=$2,benchmark=$3,base_url=$4,api_key_id=$5,protocol=$6,reasoning_effort=$7,service_tier=$8,enabled=$9,schedule_enabled=$10,interval_minutes=$11,next_run_at=CASE WHEN $10 THEN NOW()+($11::integer * INTERVAL '1 minute') ELSE NULL END,updated_at=NOW() WHERE id=$12 RETURNING created_at,updated_at,next_run_at`, args...).Scan(&c.CreatedAt, &c.UpdatedAt, &c.NextRunAt)
	}
	if err != nil {
		return intelligenceDBError(err)
	}
	return tx.Commit()
}

func lockIntelligenceConfig(ctx context.Context, tx *sql.Tx, id int64) error {
	var got int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM intelligence_test_configs WHERE id=$1 FOR UPDATE`, id).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrIntelligenceNotFound
	}
	return err
}
func checkIntelligenceIdle(ctx context.Context, tx *sql.Tx, id int64) error {
	var busy bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM intelligence_test_runs WHERE config_id=$1 AND status IN ('queued','submitting','running'))`, id).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return service.ErrIntelligenceBusy
	}
	return nil
}
func intelligenceDBError(err error) error {
	var p *pq.Error
	if errors.As(err, &p) && p.Code == "23505" {
		return infraerrors.Conflict("INTELLIGENCE_DUPLICATE", "该分组和模型已有同类型检测配置，或正在检测")
	}
	return err
}
func (r *intelligenceRepository) DeleteConfig(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockIntelligenceConfig(ctx, tx, id); err != nil {
		return err
	}
	if err = checkIntelligenceIdle(ctx, tx, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM intelligence_test_configs WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func insertIntelligenceRun(ctx context.Context, tx *sql.Tx, c *service.IntelligenceConfig, now time.Time) (*service.IntelligenceRun, error) {
	run := &service.IntelligenceRun{ID: "iq_" + uuid.NewString(), ConfigID: c.ID, GroupID: c.GroupID, Model: c.Model, Benchmark: c.Benchmark, Status: "queued", Verdict: "pending", CreatedAt: now, UpdatedAt: now}
	raw, payload, err := marshalIntelligenceRun(run)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO intelligence_test_runs(id,config_id,status,record,payload,next_poll_at,created_at,updated_at) VALUES($1,$2,'queued',$3::jsonb,$4::jsonb,$5,$5,$5)`, run.ID, c.ID, string(raw), string(payload), now)
	if err != nil {
		return nil, intelligenceDBError(err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE intelligence_test_configs SET next_run_at=$2::timestamptz+(interval_minutes * INTERVAL '1 minute') WHERE id=$1`, c.ID, now)
	return run, err
}

func (r *intelligenceRepository) CreateRun(ctx context.Context, id int64, now time.Time) (*service.IntelligenceRun, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := scanIntelligenceConfig(tx.QueryRowContext(ctx, `SELECT `+intelligenceConfigColumns+` FROM intelligence_test_configs c JOIN groups g ON g.id=c.group_id WHERE c.id=$1 AND g.deleted_at IS NULL AND g.status='active' FOR UPDATE OF c`, id))
	if err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, service.ErrIntelligenceDisabled
	}
	if err = checkIntelligenceIdle(ctx, tx, id); err != nil {
		return nil, err
	}
	run, err := insertIntelligenceRun(ctx, tx, c, now)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return run, nil
}

// ScheduleDue 在配置锁内创建持久任务，多实例不会重复领取同一检测轮次。
func (r *intelligenceRepository) ScheduleDue(ctx context.Context, now time.Time, limit int) error {
	if limit < 1 || limit > 10 {
		limit = 2
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+intelligenceConfigColumns+` FROM intelligence_test_configs c JOIN groups g ON g.id=c.group_id WHERE c.enabled AND c.schedule_enabled AND g.deleted_at IS NULL AND g.status='active' AND (c.next_run_at IS NULL OR c.next_run_at<=$1) AND NOT EXISTS(SELECT 1 FROM intelligence_test_runs r WHERE r.config_id=c.id AND r.status IN ('queued','submitting','running')) ORDER BY c.next_run_at NULLS FIRST,c.id LIMIT $2 FOR UPDATE OF c SKIP LOCKED`, now, limit)
	if err != nil {
		return err
	}
	var configs []*service.IntelligenceConfig
	for rows.Next() {
		c, e := scanIntelligenceConfig(rows)
		if e != nil {
			rows.Close()
			return e
		}
		configs = append(configs, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range configs {
		if _, err = insertIntelligenceRun(ctx, tx, c, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// 大正文单独保存，列表和状态条只读取小摘要；详情和 worker 才合并正文。
type intelligenceRunPayload struct {
	HTML             string `json:"html,omitempty"`
	Question         string `json:"question,omitempty"`
	Answer           string `json:"answer,omitempty"`
	AssessmentReason string `json:"assessment_reason,omitempty"`
}

func marshalIntelligenceRun(run *service.IntelligenceRun) ([]byte, []byte, error) {
	if run == nil {
		return nil, nil, service.ErrIntelligenceNotFound
	}
	summary := *run
	summary.HTML, summary.Question, summary.Answer, summary.AssessmentReason = "", "", "", ""
	summary.HasArtifact = run.HTML != ""
	record, err := json.Marshal(summary)
	if err != nil {
		return nil, nil, err
	}
	payload, err := json.Marshal(intelligenceRunPayload{HTML: run.HTML, Question: run.Question, Answer: run.Answer, AssessmentReason: run.AssessmentReason})
	return record, payload, err
}

func scanIntelligenceRun(row intelligenceScanner) (*service.IntelligenceRun, error) {
	var raw []byte
	var status, remote, lease string
	if err := row.Scan(&raw, &status, &remote, &lease); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrIntelligenceNotFound
		}
		return nil, err
	}
	run := &service.IntelligenceRun{}
	if err := json.Unmarshal(raw, run); err != nil {
		return nil, err
	}
	run.Status, run.RemoteID, run.LeaseToken = status, remote, lease
	return run, nil
}

func (r *intelligenceRepository) ClaimRun(ctx context.Context, now, until time.Time, token string) (*service.IntelligenceRun, error) {
	run, err := scanIntelligenceRun(r.db.QueryRowContext(ctx, `WITH due AS(SELECT id FROM intelligence_test_runs WHERE status IN ('queued','submitting','running') AND next_poll_at<=$1 AND (lease_until IS NULL OR lease_until<=$1) ORDER BY next_poll_at,created_at LIMIT 1 FOR UPDATE SKIP LOCKED) UPDATE intelligence_test_runs r SET lease_token=$3,lease_until=$2 FROM due WHERE r.id=due.id RETURNING (r.record||r.payload),r.status,r.remote_id,r.lease_token`, now, until, token))
	if errors.Is(err, service.ErrIntelligenceNotFound) {
		return nil, nil
	}
	return run, err
}

// SaveRun 对租约令牌和期限做双重比较，过期执行者不能覆盖新实例的结果。
func (r *intelligenceRepository) SaveRun(ctx context.Context, run *service.IntelligenceRun, next time.Time, keepLease bool) (bool, error) {
	raw, payload, err := marshalIntelligenceRun(run)
	if err != nil {
		return false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE intelligence_test_runs SET status=$3,remote_id=$4,record=$5::jsonb,payload=$6::jsonb,next_poll_at=$7,updated_at=NOW(),lease_until=CASE WHEN $8 THEN lease_until ELSE NULL END,lease_token=CASE WHEN $8 THEN lease_token ELSE '' END WHERE id=$1 AND lease_token=$2 AND lease_until>NOW()`, run.ID, run.LeaseToken, run.Status, run.RemoteID, string(raw), string(payload), next, keepLease)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if !keepLease {
		// 详情有界保存；仅移除较旧的画图正文，保留原来的通过状态和统计。
		if _, err = tx.ExecContext(ctx, `UPDATE intelligence_test_runs SET payload=payload-'html',record=record||'{"has_artifact":false}'::jsonb WHERE id IN(SELECT id FROM intelligence_test_runs WHERE config_id=$1 AND record->>'has_artifact'='true' ORDER BY created_at DESC,id DESC OFFSET 10)`, run.ConfigID); err != nil {
			return false, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM intelligence_test_runs WHERE config_id=$1 AND status NOT IN ('queued','submitting','running') AND id IN(SELECT id FROM intelligence_test_runs WHERE config_id=$1 ORDER BY created_at DESC,id DESC OFFSET 60)`, run.ConfigID); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
func (r *intelligenceRepository) GetRun(ctx context.Context, id string) (*service.IntelligenceRun, error) {
	return scanIntelligenceRun(r.db.QueryRowContext(ctx, `SELECT (record||payload),status,remote_id,lease_token FROM intelligence_test_runs WHERE id=$1`, id))
}
func (r *intelligenceRepository) ListRuns(ctx context.Context, id int64) ([]service.IntelligenceRun, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT record,status,remote_id,lease_token FROM intelligence_test_runs WHERE config_id=$1 ORDER BY created_at DESC,id DESC LIMIT 60`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]service.IntelligenceRun, 0)
	for rows.Next() {
		run, err := scanIntelligenceRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}
