package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type chanshuiRepository struct{ db *sql.DB }

// NewChanshuiRepository 复用主数据库，任务、提交限速与自动停用均持久化。
func NewChanshuiRepository(db *sql.DB) service.ChanshuiRepository {
	return &chanshuiRepository{db: db}
}

// GetAccountRevision 读取独立配置代次，不把额度统计或最近使用时间当作人工配置变更。
func (r *chanshuiRepository) GetAccountRevision(ctx context.Context, id int64) (int64, error) {
	var revision int64
	err := r.db.QueryRowContext(ctx, `SELECT chanshui_config_revision FROM accounts WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, service.ErrChanshuiNotFound
	}
	return revision, err
}

// 只有未被人工修改的自动停用账号才保留定时复验资格。
const chanshuiRecoveryEligible = `(c.auto_recover AND c.enabled AND c.schedule_enabled AND a.deleted_at IS NULL AND a.type='apikey' AND a.status='disabled' AND NOT a.schedulable AND (a.expires_at IS NULL OR a.expires_at>clock_timestamp()) AND EXISTS(SELECT 1 FROM chanshui_account_holds h JOIN chanshui_recovery_checks rc ON rc.account_id=h.account_id WHERE h.account_id=a.id AND h.account_revision=a.chanshui_config_revision AND rc.config_id=c.id AND rc.config_updated_at=c.updated_at))`

const chanshuiConfigColumns = `c.id,c.account_id,a.name,c.model,c.protocol,c.sections,c.timeout_seconds,c.enabled,c.schedule_enabled,c.interval_minutes,c.disable_on_adulteration,c.minimum_score,c.next_run_at,c.created_at,c.updated_at,c.auto_recover,` + chanshuiRecoveryEligible

type chanshuiScanner interface{ Scan(...any) error }

func scanChanshuiConfig(row chanshuiScanner) (*service.ChanshuiConfig, error) {
	c := &service.ChanshuiConfig{}
	err := row.Scan(&c.ID, &c.AccountID, &c.AccountName, &c.Model, &c.Protocol, pq.Array(&c.Sections), &c.TimeoutSeconds, &c.Enabled, &c.ScheduleEnabled, &c.IntervalMinutes, &c.DisableOnAdulteration, &c.MinimumScore, &c.NextRunAt, &c.CreatedAt, &c.UpdatedAt, &c.AutoRecover, &c.RecoveryPending)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrChanshuiNotFound
	}
	return c, err
}

func (r *chanshuiRepository) ListConfigs(ctx context.Context) ([]service.ChanshuiConfig, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+chanshuiConfigColumns+` FROM chanshui_test_configs c JOIN accounts a ON a.id=c.account_id ORDER BY c.id LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]service.ChanshuiConfig, 0)
	for rows.Next() {
		c, err := scanChanshuiConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *chanshuiRepository) GetConfig(ctx context.Context, id int64) (*service.ChanshuiConfig, error) {
	return scanChanshuiConfig(r.db.QueryRowContext(ctx, `SELECT `+chanshuiConfigColumns+` FROM chanshui_test_configs c JOIN accounts a ON a.id=c.account_id WHERE c.id=$1`, id))
}

// 配置行锁和活动任务唯一索引共同阻止并发编辑、删除及重复提交。
func lockChanshuiConfig(ctx context.Context, tx *sql.Tx, id int64) error {
	var got int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM chanshui_test_configs WHERE id=$1 FOR UPDATE`, id).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrChanshuiNotFound
	}
	return err
}

// 账号级事务锁统一配置维护与终态的锁序，多个模型不能并行恢复同一账号。
func lockChanshuiAccount(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('chanshui-account:'||$1::text,0))`, id)
	return err
}

func checkChanshuiIdle(ctx context.Context, tx *sql.Tx, id int64) error {
	var busy bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chanshui_test_runs WHERE config_id=$1 AND status IN ('queued','submitting','running'))`, id).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return service.ErrChanshuiBusy
	}
	return nil
}

func chanshuiDBError(err error) error {
	var p *pq.Error
	if errors.As(err, &p) && p.Code == "23505" {
		return infraerrors.Conflict("CHANSHUI_DUPLICATE", "该账号和模型已有相同协议的检测配置，或正在检测")
	}
	return err
}

func (r *chanshuiRepository) SaveConfig(ctx context.Context, c *service.ChanshuiConfig) error {
	if c == nil {
		return service.ErrChanshuiNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockChanshuiAccount(ctx, tx, c.AccountID); err != nil {
		return err
	}
	if c.ID > 0 {
		if err = lockChanshuiConfig(ctx, tx, c.ID); err != nil {
			return err
		}
		if err = checkChanshuiIdle(ctx, tx, c.ID); err != nil {
			return err
		}
		var matches bool
		if err = tx.QueryRowContext(ctx, `SELECT account_id=$2 AND model=$3 AND protocol=$4 FROM chanshui_test_configs WHERE id=$1`, c.ID, c.AccountID, c.Model, c.Protocol).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return infraerrors.Conflict("CHANSHUI_IDENTITY_IMMUTABLE", "账号、模型和协议创建后不可修改")
		}
	}
	args := []any{c.AccountID, c.Model, c.Protocol, pq.Array(c.Sections), c.TimeoutSeconds, c.Enabled, c.ScheduleEnabled, c.IntervalMinutes, c.DisableOnAdulteration, c.MinimumScore, c.AutoRecover}
	if c.ID == 0 {
		// 总数检查与创建串行，防止超过可管理范围的隐藏定时检测。
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(31720261023)`); err != nil {
			return err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chanshui_test_configs`).Scan(&count); err != nil {
			return err
		}
		if count >= 200 {
			return infraerrors.Conflict("CHANSHUI_CONFIG_LIMIT", "最多支持200条掺水检测配置")
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO chanshui_test_configs(account_id,model,protocol,sections,timeout_seconds,enabled,schedule_enabled,interval_minutes,disable_on_adulteration,minimum_score,auto_recover,next_run_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,CASE WHEN $7 THEN NOW()+($8::integer*INTERVAL '1 minute') ELSE NULL END) RETURNING id,created_at,updated_at,next_run_at`, args...).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt, &c.NextRunAt)
	} else {
		args = append(args, c.ID)
		err = tx.QueryRowContext(ctx, `UPDATE chanshui_test_configs SET sections=$4,timeout_seconds=$5,enabled=$6,schedule_enabled=$7,interval_minutes=$8,disable_on_adulteration=$9,minimum_score=$10,auto_recover=$11,next_run_at=CASE WHEN $7 THEN NOW()+($8::integer*INTERVAL '1 minute') ELSE NULL END,updated_at=NOW() WHERE id=$12 AND account_id=$1 AND model=$2 AND protocol=$3 RETURNING created_at,updated_at,next_run_at`, args...).Scan(&c.CreatedAt, &c.UpdatedAt, &c.NextRunAt)
	}
	if err != nil {
		return chanshuiDBError(err)
	}
	// 管理员修改任一模型规则后，旧复验结果不能擅自恢复账号。
	if _, err = tx.ExecContext(ctx, `DELETE FROM chanshui_account_holds WHERE account_id=$1`, c.AccountID); err != nil {
		return err
	}
	c.RecoveryPending = false
	return tx.Commit()
}

func (r *chanshuiRepository) DeleteConfig(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var accountID int64
	if err = tx.QueryRowContext(ctx, `SELECT account_id FROM chanshui_test_configs WHERE id=$1`, id).Scan(&accountID); errors.Is(err, sql.ErrNoRows) {
		return service.ErrChanshuiNotFound
	} else if err != nil {
		return err
	}
	if err = lockChanshuiAccount(ctx, tx, accountID); err != nil {
		return err
	}
	if err = lockChanshuiConfig(ctx, tx, id); err != nil {
		return err
	}
	if err = checkChanshuiIdle(ctx, tx, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM chanshui_account_holds WHERE account_id=$1`, accountID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM chanshui_test_configs WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func insertChanshuiRun(ctx context.Context, tx *sql.Tx, c *service.ChanshuiConfig, now time.Time, trigger string) (*service.ChanshuiRun, error) {
	run := &service.ChanshuiRun{ID: "cs_" + uuid.NewString(), ConfigID: c.ID, AccountID: c.AccountID, Model: c.Model, Trigger: trigger, Status: "queued", Verdict: "pending", ActionStatus: "none", ConfigUpdatedAt: c.UpdatedAt, CreatedAt: now, UpdatedAt: now}
	raw, _, err := marshalChanshuiRun(run)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO chanshui_test_runs(id,config_id,status,record,config_updated_at,next_poll_at,created_at,updated_at) VALUES($1,$2,'queued',$3::jsonb,$4,$5,$5,$5)`, run.ID, c.ID, string(raw), c.UpdatedAt, now)
	if err != nil {
		return nil, chanshuiDBError(err)
	}
	// 新复验开始即撤销该模型上轮通过状态，错误或超时也不会沿用旧好结果。
	if _, err = tx.ExecContext(ctx, `UPDATE chanshui_recovery_checks SET passed_at=NULL WHERE config_id=$1`, c.ID); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE chanshui_test_configs SET next_run_at=CASE WHEN schedule_enabled THEN $2::timestamptz+(interval_minutes*INTERVAL '1 minute') ELSE NULL END WHERE id=$1`, c.ID, now)
	return run, err
}

func (r *chanshuiRepository) CreateRun(ctx context.Context, id int64, now time.Time) (*service.ChanshuiRun, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var accountID int64
	if err = tx.QueryRowContext(ctx, `SELECT account_id FROM chanshui_test_configs WHERE id=$1`, id).Scan(&accountID); errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrChanshuiNotFound
	} else if err != nil {
		return nil, err
	}
	if err = lockChanshuiAccount(ctx, tx, accountID); err != nil {
		return nil, err
	}
	// 手动请求允许诊断停用账号，创建任务本身不改变调度；恢复另需持有状态与完整证据。
	c, err := scanChanshuiConfig(tx.QueryRowContext(ctx, `SELECT `+chanshuiConfigColumns+` FROM chanshui_test_configs c JOIN accounts a ON a.id=c.account_id WHERE c.id=$1 AND a.deleted_at IS NULL AND a.type='apikey' FOR UPDATE OF c`, id))
	if err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, service.ErrChanshuiDisabled
	}
	if err = checkChanshuiIdle(ctx, tx, id); err != nil {
		return nil, err
	}
	run, err := insertChanshuiRun(ctx, tx, c, now, "manual")
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return run, nil
}

const chanshuiScheduleEligibility = `c.enabled AND c.schedule_enabled AND a.deleted_at IS NULL AND a.type='apikey' AND ((a.status='active' AND a.schedulable) OR ` + chanshuiRecoveryEligible + `) AND (a.expires_at IS NULL OR a.expires_at>clock_timestamp()) AND (c.next_run_at IS NULL OR c.next_run_at<=$1) AND NOT EXISTS(SELECT 1 FROM chanshui_test_runs r WHERE r.config_id=c.id AND r.status IN ('queued','submitting','running'))`

// ScheduleDue 先读候选，写入前按账号→配置锁序重新检查，与最终恢复原子串行。
func (r *chanshuiRepository) ScheduleDue(ctx context.Context, now time.Time, limit int) error {
	if limit < 1 || limit > 10 {
		limit = 2
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+chanshuiConfigColumns+` FROM chanshui_test_configs c JOIN accounts a ON a.id=c.account_id WHERE `+chanshuiScheduleEligibility+` ORDER BY c.next_run_at NULLS FIRST,c.id LIMIT $2`, now, limit)
	if err != nil {
		return err
	}
	var configs []*service.ChanshuiConfig
	for rows.Next() {
		c, e := scanChanshuiConfig(rows)
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
		if err = r.scheduleConfig(ctx, c, now); err != nil {
			return err
		}
	}
	return nil
}

func (r *chanshuiRepository) scheduleConfig(ctx context.Context, candidate *service.ChanshuiConfig, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockChanshuiAccount(ctx, tx, candidate.AccountID); err != nil {
		return err
	}
	c, err := scanChanshuiConfig(tx.QueryRowContext(ctx, `SELECT `+chanshuiConfigColumns+` FROM chanshui_test_configs c JOIN accounts a ON a.id=c.account_id WHERE `+chanshuiScheduleEligibility+` AND c.id=$2 FOR UPDATE OF c`, now, candidate.ID))
	if errors.Is(err, service.ErrChanshuiNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = insertChanshuiRun(ctx, tx, c, now, "schedule"); err != nil {
		return err
	}
	return tx.Commit()
}

func marshalChanshuiRun(run *service.ChanshuiRun) ([]byte, any, error) {
	if run == nil {
		return nil, nil, service.ErrChanshuiNotFound
	}
	summary := *run
	summary.Report = nil
	raw, err := json.Marshal(summary)
	if err != nil {
		return nil, nil, err
	}
	var report any
	if len(run.Report) > 0 {
		if !json.Valid(run.Report) {
			return nil, nil, errors.New("掺水检测报告格式无效")
		}
		report = string(run.Report)
	}
	return raw, report, nil
}

const chanshuiRunColumns = `record,status,remote_id,remote_expires_at,lease_token,account_snapshot,account_updated_at,config_updated_at,account_revision`

func scanChanshuiRun(row chanshuiScanner) (*service.ChanshuiRun, error) {
	var raw []byte
	run := &service.ChanshuiRun{}
	var status string
	err := row.Scan(&raw, &status, &run.RemoteID, &run.RemoteExpiresAt, &run.LeaseToken, &run.AccountSnapshot, &run.AccountUpdatedAt, &run.ConfigUpdatedAt, &run.AccountRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrChanshuiNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, run); err != nil {
		return nil, err
	}
	run.Status = status
	return run, nil
}

func (r *chanshuiRepository) ClaimRun(ctx context.Context, now, until time.Time, token string) (*service.ChanshuiRun, error) {
	if token == "" || !until.After(now) {
		return nil, errors.New("掺水检测租约无效")
	}
	run, err := scanChanshuiRun(r.db.QueryRowContext(ctx, `WITH due AS(SELECT id FROM chanshui_test_runs WHERE status IN ('queued','submitting','running') AND next_poll_at<=$1 AND (lease_until IS NULL OR lease_until<=$1) ORDER BY next_poll_at,created_at LIMIT 1 FOR UPDATE SKIP LOCKED) UPDATE chanshui_test_runs r SET lease_token=$3,lease_until=$2 FROM due WHERE r.id=due.id RETURNING r.record||jsonb_build_object('report',r.report),r.status,r.remote_id,r.remote_expires_at,r.lease_token,r.account_snapshot,r.account_updated_at,r.config_updated_at,r.account_revision`, now, until, token))
	if errors.Is(err, service.ErrChanshuiNotFound) {
		return nil, nil
	}
	return run, err
}

func chanshuiActive(status string) bool {
	return status == "queued" || status == "submitting" || status == "running"
}

// SaveRun 只保存活动状态；终态始终经过同一个事务入口，防止动作与结果分离。
func (r *chanshuiRepository) SaveRun(ctx context.Context, run *service.ChanshuiRun, next time.Time, keepLease bool) (bool, error) {
	if run == nil {
		return false, service.ErrChanshuiNotFound
	}
	if !chanshuiActive(run.Status) {
		return r.FinalizeRun(ctx, run, nil)
	}
	raw, report, err := marshalChanshuiRun(run)
	if err != nil {
		return false, err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE chanshui_test_runs SET status=$3,remote_id=$4,record=$5::jsonb,report=$6::jsonb,next_poll_at=$7,updated_at=NOW(),lease_until=CASE WHEN $8 THEN lease_until ELSE NULL END,lease_token=CASE WHEN $8 THEN lease_token ELSE '' END,account_snapshot=$9,account_updated_at=$10,remote_expires_at=$11,account_revision=$12 WHERE id=$1 AND lease_token=$2 AND lease_token<>'' AND lease_until>clock_timestamp() AND status IN ('queued','submitting','running')`, run.ID, run.LeaseToken, run.Status, run.RemoteID, string(raw), report, next, keepLease, run.AccountSnapshot, run.AccountUpdatedAt, run.RemoteExpiresAt, run.AccountRevision)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// AcquireSubmitSlot 使用数据库时钟避免多实例时钟偏移突破供应商限制；忙时只返回下次时间。
func (r *chanshuiRepository) AcquireSubmitSlot(ctx context.Context, now time.Time) (time.Time, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	var next, dbNow time.Time
	if err = tx.QueryRowContext(ctx, `SELECT next_allowed_at,clock_timestamp() FROM chanshui_submit_gate WHERE id=1 FOR UPDATE`).Scan(&next, &dbNow); err != nil {
		return time.Time{}, err
	}
	if next.After(dbNow) {
		// 返回调用者时钟上的相对等待时间，不能让领先的本机时间把忙槽误认成成功。
		return now.Add(next.Sub(dbNow)), nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE chanshui_submit_gate SET next_allowed_at=clock_timestamp()+INTERVAL '60 seconds' WHERE id=1`); err != nil {
		return time.Time{}, err
	}
	if err = tx.Commit(); err != nil {
		return time.Time{}, err
	}
	return now, nil
}

// FinalizeRun 把检测终态、账号停用/恢复和调度通知一起提交；终态重放不改变账号。
func (r *chanshuiRepository) FinalizeRun(ctx context.Context, run *service.ChanshuiRun, decision *service.ChanshuiDisableDecision) (bool, error) {
	if run == nil {
		return false, service.ErrChanshuiNotFound
	}
	if run.Status != "completed" && run.Status != "error" && run.Status != "unknown" {
		return false, errors.New("掺水检测终态无效")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// 所有写入统一按账号、配置、任务顺序加锁，避免多个模型或编辑操作锁序相反。
	if err = lockChanshuiAccount(ctx, tx, run.AccountID); err != nil {
		return false, err
	}
	if err = lockChanshuiConfig(ctx, tx, run.ConfigID); err != nil {
		return false, err
	}
	stored, err := scanChanshuiRun(tx.QueryRowContext(ctx, `SELECT `+chanshuiRunColumns+` FROM chanshui_test_runs WHERE id=$1 AND config_id=$2 AND lease_token=$3 AND lease_token<>'' AND lease_until>clock_timestamp() AND status IN ('queued','submitting','running') FOR UPDATE`, run.ID, run.ConfigID, run.LeaseToken))
	if errors.Is(err, service.ErrChanshuiNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if stored.AccountID != run.AccountID || stored.Model != run.Model {
		return false, errors.New("掺水检测任务身份不匹配")
	}
	final := *run
	final.ConfigUpdatedAt = stored.ConfigUpdatedAt
	// 服务已校验身份指纹时可以明确记录跳过；实际停用结果只能由本事务产生。
	switch final.ActionStatus {
	case "skipped_changed":
		final.ActionReason = "账号配置已改变，未自动停用"
	case "skipped_disabled":
		final.ActionReason = "账号已停用或不可调度，未重复修改"
	default:
		final.ActionStatus = "none"
		final.ActionReason = ""
	}
	if decision != nil {
		if err = r.applyDisable(ctx, tx, &final, stored, decision); err != nil {
			return false, err
		}
	}
	final.UpdatedAt = time.Now().UTC()
	if final.FinishedAt == nil {
		final.FinishedAt = &final.UpdatedAt
	}
	raw, report, err := marshalChanshuiRun(&final)
	if err != nil {
		return false, err
	}
	// 等待账号或设置锁也可能耗尽租约；提交前再次检查，失效时回滚账号变更与通知。
	result, err := tx.ExecContext(ctx, `UPDATE chanshui_test_runs SET status=$2,remote_id=$3,record=$4::jsonb,report=$5::jsonb,updated_at=NOW(),lease_until=NULL,lease_token='',remote_expires_at=$6 WHERE id=$1 AND lease_token=$7 AND lease_until>clock_timestamp()`, final.ID, final.Status, final.RemoteID, string(raw), report, final.RemoteExpiresAt, final.LeaseToken)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM chanshui_test_runs WHERE config_id=$1 AND status NOT IN ('queued','submitting','running') AND id IN(SELECT id FROM chanshui_test_runs WHERE config_id=$1 ORDER BY created_at DESC,id DESC OFFSET 60)`, final.ConfigID); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	*run = final
	return true, nil
}

// @project-doc docs/domains/chanshui_tests.md#chanshui_decision
func (r *chanshuiRepository) applyDisable(ctx context.Context, tx *sql.Tx, run, stored *service.ChanshuiRun, decision *service.ChanshuiDisableDecision) error {
	// 错误、未知执行及不完整分数绝不能因调用方误传决定而触发账号停用。
	if run.Status != "completed" {
		return nil
	}
	c, err := scanChanshuiConfig(tx.QueryRowContext(ctx, `SELECT `+chanshuiConfigColumns+` FROM chanshui_test_configs c JOIN accounts a ON a.id=c.account_id WHERE c.id=$1`, run.ConfigID))
	if err != nil {
		return err
	}
	var setting string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='chanshui_enabled' FOR SHARE`).Scan(&setting)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if setting != "true" || !c.Enabled || !c.UpdatedAt.Equal(stored.ConfigUpdatedAt) {
		run.ActionStatus = "skipped_changed"
		run.ActionReason = "检测开关或规则已改变，未自动停用"
		return nil
	}
	if decision.Recover {
		return r.applyRecovery(ctx, tx, run, stored, c, decision)
	}
	badScore := run.ScoreComplete && run.Score != nil && !math.IsNaN(*run.Score) && !math.IsInf(*run.Score, 0) && *run.Score >= 0 && *run.Score <= 100 && c.MinimumScore != nil && *run.Score < *c.MinimumScore
	adulterated := run.Verdict == "adulterated" && c.DisableOnAdulteration
	if !badScore && !adulterated {
		return nil
	}
	var updated time.Time
	var status string
	var schedulable bool
	var deleted *time.Time
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT updated_at,status,schedulable,deleted_at,chanshui_config_revision FROM accounts WHERE id=$1 FOR UPDATE`, run.AccountID).Scan(&updated, &status, &schedulable, &deleted, &revision); err != nil {
		return err
	}
	if deleted != nil || status != service.StatusActive || !schedulable {
		if deleted == nil && c.RecoveryPending && revision == stored.AccountRevision {
			run.ActionStatus = "recovery_pending"
			run.ActionReason = "账号保持停用，本次复验未达到恢复条件"
			return nil
		}
		run.ActionStatus = "skipped_disabled"
		run.ActionReason = "账号已停用或不可调度，未重复修改"
		return nil
	}
	if decision.AccountUpdatedAt.IsZero() || !updated.Equal(decision.AccountUpdatedAt) || stored.AccountSnapshot == "" || revision != stored.AccountRevision {
		run.ActionStatus = "skipped_changed"
		run.ActionReason = "账号配置已改变，未自动停用"
		return nil
	}
	// 动作原因使用固定文案，不把上游任意正文或凭据写入账号错误信息。
	reason := "掺水检测确认掺水，已自动停用"
	if !adulterated {
		reason = "掺水检测有效分数低于配置阈值，已自动停用"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE accounts SET status=$2,schedulable=false,error_message=$3,updated_at=NOW() WHERE id=$1`, run.AccountID, service.StatusDisabled, reason); err != nil {
		return err
	}
	// 同账号所有保护模型均已选择复验恢复才建立所有权，避免无望恢复的反复收费检测。
	if c.AutoRecover {
		var recoverable bool
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(bool_and(auto_recover AND schedule_enabled AND 'fingerprint'=ANY(sections)),false) FROM chanshui_test_configs WHERE account_id=$1 AND enabled AND (disable_on_adulteration OR minimum_score IS NOT NULL)`, run.AccountID).Scan(&recoverable); err != nil {
			return err
		}
		if recoverable {
			if _, err = tx.ExecContext(ctx, `INSERT INTO chanshui_account_holds(account_id,account_revision) SELECT id,chanshui_config_revision FROM accounts WHERE id=$1 ON CONFLICT(account_id) DO UPDATE SET account_revision=EXCLUDED.account_revision,created_at=clock_timestamp()`, run.AccountID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM chanshui_recovery_checks WHERE account_id=$1`, run.AccountID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO chanshui_recovery_checks(account_id,config_id,config_updated_at) SELECT account_id,id,updated_at FROM chanshui_test_configs WHERE account_id=$1 AND enabled AND (disable_on_adulteration OR minimum_score IS NOT NULL)`, run.AccountID); err != nil {
				return err
			}
			reason += "；将按计划复验全部保护模型，通过后恢复调用"
		} else {
			reason += "；同账号仍有保护模型未开启定时恢复，需人工恢复账号"
		}
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &run.AccountID, nil, nil); err != nil {
		return err
	}
	run.ActionStatus = "disabled"
	run.ActionReason = reason
	return nil
}

// applyRecovery 只解除本功能持有的停用状态；人工改动、旧报告和不完整证据均不能恢复。
func (r *chanshuiRepository) applyRecovery(ctx context.Context, tx *sql.Tx, run, stored *service.ChanshuiRun, c *service.ChanshuiConfig, decision *service.ChanshuiDisableDecision) error {
	if !c.AutoRecover || !c.ScheduleEnabled || run.Verdict != "clean" || !run.ScoreComplete || run.Score == nil || math.IsNaN(*run.Score) || math.IsInf(*run.Score, 0) || *run.Score < 0 || *run.Score > 100 || (c.MinimumScore != nil && *run.Score < *c.MinimumScore) {
		return nil
	}
	var updated time.Time
	var status string
	var schedulable, expired bool
	var deleted *time.Time
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT updated_at,status,schedulable,deleted_at,chanshui_config_revision,expires_at IS NOT NULL AND expires_at<=clock_timestamp() FROM accounts WHERE id=$1 FOR UPDATE`, run.AccountID).Scan(&updated, &status, &schedulable, &deleted, &revision, &expired); err != nil {
		return err
	}
	var heldRevision int64
	var since time.Time
	err := tx.QueryRowContext(ctx, `SELECT account_revision,created_at FROM chanshui_account_holds WHERE account_id=$1`, run.AccountID).Scan(&heldRevision, &since)
	if errors.Is(err, sql.ErrNoRows) {
		run.ActionStatus, run.ActionReason = "skipped_changed", "账号不属于可自动恢复的停用状态，需管理员处理"
		return nil
	}
	if err != nil {
		return err
	}
	if deleted != nil || expired || status != service.StatusDisabled || schedulable || revision != heldRevision || stored.AccountRevision != heldRevision || stored.AccountSnapshot == "" || !updated.Equal(decision.AccountUpdatedAt) || stored.CreatedAt.Before(since) {
		run.ActionStatus, run.ActionReason = "skipped_changed", "账号配置已改变或检测早于本次停用，未自动恢复"
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE chanshui_recovery_checks SET passed_at=clock_timestamp() WHERE account_id=$1 AND config_id=$2 AND config_updated_at=$3`, run.AccountID, run.ConfigID, stored.ConfigUpdatedAt)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		run.ActionStatus, run.ActionReason = "skipped_changed", "保护模型配置已改变，未自动恢复"
		return nil
	}
	var allPassed bool
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(bool_and(rc.passed_at IS NOT NULL AND c.updated_at=rc.config_updated_at AND c.enabled AND c.auto_recover AND c.schedule_enabled),false) AND NOT EXISTS(SELECT 1 FROM chanshui_test_runs r JOIN chanshui_recovery_checks checks ON checks.config_id=r.config_id WHERE checks.account_id=$1 AND r.id<>$2 AND r.status IN ('queued','submitting','running')) FROM chanshui_recovery_checks rc JOIN chanshui_test_configs c ON c.id=rc.config_id WHERE rc.account_id=$1`, run.AccountID, run.ID).Scan(&allPassed); err != nil {
		return err
	}
	if !allPassed {
		run.ActionStatus, run.ActionReason = "recovery_pending", "本模型复验通过，等待同账号全部保护模型通过后恢复调用"
		return nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE accounts SET status=$2,schedulable=true,error_message='',updated_at=NOW() WHERE id=$1`, run.AccountID, service.StatusActive); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM chanshui_account_holds WHERE account_id=$1`, run.AccountID); err != nil {
		return err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &run.AccountID, nil, nil); err != nil {
		return err
	}
	run.ActionStatus, run.ActionReason = "recovered", "全部保护模型确认真实且有效分数达标，已恢复账号调用"
	return nil
}

func (r *chanshuiRepository) GetRun(ctx context.Context, id string) (*service.ChanshuiRun, error) {
	return scanChanshuiRun(r.db.QueryRowContext(ctx, `SELECT record||jsonb_build_object('report',report),status,remote_id,remote_expires_at,lease_token,account_snapshot,account_updated_at,config_updated_at,account_revision FROM chanshui_test_runs WHERE id=$1`, id))
}

func (r *chanshuiRepository) ListRuns(ctx context.Context, id int64) ([]service.ChanshuiRun, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+chanshuiRunColumns+` FROM chanshui_test_runs WHERE config_id=$1 ORDER BY created_at DESC,id DESC LIMIT 60`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]service.ChanshuiRun, 0)
	for rows.Next() {
		run, e := scanChanshuiRun(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}
