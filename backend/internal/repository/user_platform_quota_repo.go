package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/userplatformquota"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// UserPlatformQuotaRecord 是 repository 层的传输结构体，
// 与 ent.UserPlatformQuota 实体解耦，供业务层使用。
type UserPlatformQuotaRecord struct {
	UserID             int64
	Platform           string
	DailyLimitUSD      *float64
	WeeklyLimitUSD     *float64
	MonthlyLimitUSD    *float64
	DailyUsageUSD      float64
	WeeklyUsageUSD     float64
	MonthlyUsageUSD    float64
	DailyWindowStart   *time.Time
	WeeklyWindowStart  *time.Time
	MonthlyWindowStart *time.Time
}

// HasAnyLimit 保留 nil 与显式零限额的区别。
func (r UserPlatformQuotaRecord) HasAnyLimit() bool {
	return r.DailyLimitUSD != nil || r.WeeklyLimitUSD != nil || r.MonthlyLimitUSD != nil
}

// ErrUserPlatformQuotaNotFound 用于 ResetExpiredWindow 等需要"必须命中已有记录"的方法。
var ErrUserPlatformQuotaNotFound = fmt.Errorf("user platform quota record not found")

// ErrUserPlatformQuotaFKViolation 保留旧适配器的错误契约；只更新快照不再触发缺失用户的外键插入错误。
var ErrUserPlatformQuotaFKViolation = errors.New("user platform quota snapshot FK violation")

// UserPlatformQuotaSnapshot 是 BatchSnapshotUsage 的输入结构体，
// 表示 Redis 当前窗口快照（用于绝对值覆盖写入 DB）。
type UserPlatformQuotaSnapshot struct {
	UserID             int64
	Platform           string
	DailyUsageUSD      float64
	WeeklyUsageUSD     float64
	MonthlyUsageUSD    float64
	DailyWindowStart   time.Time
	WeeklyWindowStart  time.Time
	MonthlyWindowStart time.Time
}

// UserPlatformQuotaRepository 定义用户平台配额的数据访问接口。
type UserPlatformQuotaRepository interface {
	// BulkInsertInitial 幂等批量插入初始配额记录（ON CONFLICT DO NOTHING）。
	BulkInsertInitial(ctx context.Context, records []UserPlatformQuotaRecord) error
	// GetByUserPlatform 查询单条配额记录，未找到时返回 (nil, nil)。
	GetByUserPlatform(ctx context.Context, userID int64, platform string) (*UserPlatformQuotaRecord, error)
	// ListByUser 查询用户的所有平台配额记录（排除软删除）。
	ListByUser(ctx context.Context, userID int64) ([]UserPlatformQuotaRecord, error)
	// IncrementUsageWithReset 原子地累加用量，若窗口已过期则先重置再累加。
	IncrementUsageWithReset(ctx context.Context, userID int64, platform string, cost float64, now time.Time) error
	// ResetExpiredWindow 重置指定窗口（daily/weekly/monthly）的用量与起始时间。
	ResetExpiredWindow(ctx context.Context, userID int64, platform string, window string, newStart time.Time) error
	// UpsertForUser 全量替换限额、保留既有用量（详见 service.UserPlatformQuotaRepository.UpsertForUser）。
	UpsertForUser(ctx context.Context, userID int64, records []UserPlatformQuotaRecord) error
	// BatchSnapshotUsage 只更新既有活跃行，缺失或软删除行跳过，不重新建行。
	// 历史无限额行仍可承接待刷快照；要求 snapshots 内 (user,platform) 不重复。
	BatchSnapshotUsage(ctx context.Context, snapshots []UserPlatformQuotaSnapshot, now time.Time) error
}

type userPlatformQuotaRepository struct {
	client *dbent.Client
}

// NewUserPlatformQuotaRepository 创建 UserPlatformQuotaRepository 实现。
func NewUserPlatformQuotaRepository(client *dbent.Client) UserPlatformQuotaRepository {
	return &userPlatformQuotaRepository{client: client}
}

// BulkInsertInitial 用原生 SQL ON CONFLICT 实现幂等批量插入（带条件 limit 覆盖）。
// 仅插入 limit_usd 与元数据，usage_usd 用 DB 默认 0，window_start 留 NULL。
// FK 约束要求 user_id 在 users 表中存在，调用方负责保证。
//
// 冲突策略：CASE WHEN existing.*_limit_usd IS NULL THEN EXCLUDED.*_limit_usd ELSE existing ...
//   - 若此前版本留下了 limit 全 NULL 的历史行，
//     此处会把注册时的默认 limit 写入，避免该用户在该平台永久无限额。
//   - 若管理员已通过 UpsertForUser 设置了非 NULL 个性化 limit，**保留不动**
//     —— 旧实现无条件 EXCLUDED 覆盖会丢失个性化配置。
//   - 不会改 usage_usd / window_start，保留累计的用量。
//   - 仅命中 deleted_at IS NULL 的活跃记录（partial unique index 作用域）。
func (r *userPlatformQuotaRepository) BulkInsertInitial(ctx context.Context, records []UserPlatformQuotaRecord) error {
	records = configuredQuotaRecords(records)
	if len(records) == 0 {
		return nil
	}

	client := clientFromContext(ctx, r.client)

	var sb strings.Builder
	_, _ = sb.WriteString("INSERT INTO user_platform_quotas (user_id, platform, daily_limit_usd, weekly_limit_usd, monthly_limit_usd, daily_usage_usd, weekly_usage_usd, monthly_usage_usd, created_at, updated_at) VALUES ")
	args := make([]any, 0, len(records)*6)
	// 统一时间戳：避免循环内多次 time.Now() 让同一批记录的 created_at/updated_at
	// 出现亚毫秒级偏差（与 UpsertForUser 的 now := time.Now() 风格一致）。
	now := time.Now()
	for i, rec := range records {
		base := i * 6
		if i > 0 {
			_, _ = sb.WriteString(",")
		}
		fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d,0,0,0,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+6)
		args = append(args,
			rec.UserID, rec.Platform,
			rec.DailyLimitUSD, rec.WeeklyLimitUSD, rec.MonthlyLimitUSD,
			now,
		)
	}
	// 精确命中 partial unique index（deleted_at IS NULL），避免对软删记录的歧义冲突。
	// 条件覆盖：仅在现有 limit 为 NULL 时才写入 EXCLUDED，否则保留现有非 NULL 值。
	// - 兼容此前版本已用 NULL limit 建行的场景（NULL → 注册默认）
	// - 保护管理员通过 UpsertForUser 设置的个性化 limit 不被静默覆盖
	_, _ = sb.WriteString(` ON CONFLICT (user_id, platform) WHERE deleted_at IS NULL
		DO UPDATE SET
			daily_limit_usd   = COALESCE(user_platform_quotas.daily_limit_usd, EXCLUDED.daily_limit_usd),
			weekly_limit_usd  = COALESCE(user_platform_quotas.weekly_limit_usd, EXCLUDED.weekly_limit_usd),
			monthly_limit_usd = COALESCE(user_platform_quotas.monthly_limit_usd, EXCLUDED.monthly_limit_usd),
			updated_at        = EXCLUDED.updated_at`)

	_, err := client.ExecContext(ctx, sb.String(), args...)
	return err
}

// GetByUserPlatform 通过 ent 查询单条配额（排除软删除）。未找到返回 (nil, nil)。
func (r *userPlatformQuotaRepository) GetByUserPlatform(ctx context.Context, userID int64, platform string) (*UserPlatformQuotaRecord, error) {
	client := clientFromContext(ctx, r.client)
	entity, err := client.UserPlatformQuota.Query().
		Where(
			userplatformquota.UserIDEQ(userID),
			userplatformquota.PlatformEQ(platform),
			userplatformquota.DeletedAtIsNil(),
		).
		Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return entQuotaToRecord(entity), nil
}

// ListByUser 查询用户的所有平台配额记录（排除软删除）。
func (r *userPlatformQuotaRepository) ListByUser(ctx context.Context, userID int64) ([]UserPlatformQuotaRecord, error) {
	client := clientFromContext(ctx, r.client)
	rows, err := client.UserPlatformQuota.Query().
		Where(
			userplatformquota.UserIDEQ(userID),
			userplatformquota.DeletedAtIsNil(),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]UserPlatformQuotaRecord, 0, len(rows))
	for _, e := range rows {
		out = append(out, *entQuotaToRecord(e))
	}
	return out, nil
}

// IncrementUsageWithReset 仅对既有行原子重置窗口并累加用量，不创建无配置记录。
// 正常无限额请求由结算守卫跳过；已通过守卫的在途用量仍须落库，不能因限额刚被清空而丢失。
func (r *userPlatformQuotaRepository) IncrementUsageWithReset(ctx context.Context, userID int64, platform string, cost float64, now time.Time) error {
	return r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		existing, err := txClient.UserPlatformQuota.Query().
			Where(
				userplatformquota.UserIDEQ(userID),
				userplatformquota.PlatformEQ(platform),
				userplatformquota.DeletedAtIsNil(),
			).
			ForUpdate().
			Only(txCtx)
		if dbent.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}

		newDaily := maybeReset(existing.DailyUsageUsd, existing.DailyWindowStart, timezone.StartOfDay(now), cost)
		newWeekly := maybeReset(existing.WeeklyUsageUsd, existing.WeeklyWindowStart, timezone.StartOfWeek(now), cost)
		// 30 天滚动月度窗口：过期时重置为 cost 并以 now 为新起始，否则累加保留原起始
		newMonthly, newMonthlyStart := monthlyMaybeReset(existing.MonthlyUsageUsd, existing.MonthlyWindowStart, cost, now)

		_, e := existing.Update().
			SetDailyUsageUsd(newDaily).
			SetWeeklyUsageUsd(newWeekly).
			SetMonthlyUsageUsd(newMonthly).
			SetDailyWindowStart(timezone.StartOfDay(now)).
			SetWeeklyWindowStart(timezone.StartOfWeek(now)).
			SetMonthlyWindowStart(newMonthlyStart). // 30 天滚动：仅过期时更新起始
			Save(txCtx)
		return e
	})
}

// ResetExpiredWindow 无条件重置指定窗口（daily/weekly/monthly）的用量与起始时间。
//
// ⚠️ 命名警告（NOT a "check-then-reset" helper）：
//
//	名字里的 "Expired" 是历史遗留，**实现并不校验窗口是否真的过期**。
//	任何调用都会无条件把对应窗口的 *_usage_usd 清零并重写 *_window_start。
//	目前唯一合法 caller 是 admin POST /reset 接口（管理员强制归零）。
//
//	如果你想要"仅在窗口过期才重置"的语义，请直接使用 IncrementUsageWithReset
//	的内部判断（maybeReset / monthlyMaybeReset），或新增独立函数；
//	不要复用这里的函数，否则会出现"明明窗口未过期，用量却被清零"的隐蔽 bug。
//
// 未命中活跃记录时返回 ErrUserPlatformQuotaNotFound。
func (r *userPlatformQuotaRepository) ResetExpiredWindow(ctx context.Context, userID int64, platform string, window string, newStart time.Time) error {
	client := clientFromContext(ctx, r.client)
	upd := client.UserPlatformQuota.Update().
		Where(
			userplatformquota.UserIDEQ(userID),
			userplatformquota.PlatformEQ(platform),
			userplatformquota.DeletedAtIsNil(),
		)
	switch window {
	case "daily":
		upd = upd.SetDailyUsageUsd(0).SetDailyWindowStart(newStart)
	case "weekly":
		upd = upd.SetWeeklyUsageUsd(0).SetWeeklyWindowStart(newStart)
	case "monthly":
		upd = upd.SetMonthlyUsageUsd(0).SetMonthlyWindowStart(newStart)
	default:
		return fmt.Errorf("unknown window %q", window)
	}
	n, err := upd.Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUserPlatformQuotaNotFound
	}
	return nil
}

// withTx 在事务中执行 fn，若 ctx 中已有事务则复用。
func (r *userPlatformQuotaRepository) withTx(ctx context.Context, fn func(txCtx context.Context, txClient *dbent.Client) error) error {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return fn(ctx, tx.Client())
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin user_platform_quota transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := fn(txCtx, tx.Client()); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit user_platform_quota transaction: %w", err)
	}
	return nil
}

// entQuotaToRecord 将 ent entity 映射为 repository record。
// 注意 ent 生成字段名为 DailyLimitUsd（非 DailyLimitUSD）。
func entQuotaToRecord(e *dbent.UserPlatformQuota) *UserPlatformQuotaRecord {
	return &UserPlatformQuotaRecord{
		UserID:             e.UserID,
		Platform:           e.Platform,
		DailyLimitUSD:      e.DailyLimitUsd,
		WeeklyLimitUSD:     e.WeeklyLimitUsd,
		MonthlyLimitUSD:    e.MonthlyLimitUsd,
		DailyUsageUSD:      e.DailyUsageUsd,
		WeeklyUsageUSD:     e.WeeklyUsageUsd,
		MonthlyUsageUSD:    e.MonthlyUsageUsd,
		DailyWindowStart:   e.DailyWindowStart,
		WeeklyWindowStart:  e.WeeklyWindowStart,
		MonthlyWindowStart: e.MonthlyWindowStart,
	}
}

// maybeReset 判断是否需要重置窗口用量：
// - 若 prevStart 为 nil 或与 currStart 不同，表示窗口已过期，返回 cost（重置）
// - 否则返回 prevUsage + cost（累加）
func maybeReset(prevUsage float64, prevStart *time.Time, currStart time.Time, cost float64) float64 {
	if prevStart == nil || !prevStart.Equal(currStart) {
		return cost
	}
	return prevUsage + cost
}

// monthlyMaybeReset 判断 30 天滚动月度窗口是否需要重置。
// 过期条件：prevStart 为 nil 或 now - prevStart >= 30×24h（与订阅模式 NeedsMonthlyReset 语义一致）。
// 过期时重置为 cost，否则累加。返回 (newUsage, newWindowStart)。
func monthlyMaybeReset(prevUsage float64, prevStart *time.Time, cost float64, now time.Time) (float64, time.Time) {
	if prevStart == nil || now.Sub(*prevStart) >= 30*24*time.Hour {
		return cost, now
	}
	return prevUsage + cost, *prevStart
}

// UpsertForUser 全量替换限额配置，但保留既有行的用量与窗口。
// 清空或省略平台仅取消限制，避免丢失历史统计和 Redis 尚未落库的快照；
// 三档全空且没有既有行的平台不建行，显式零限额仍建行。
func (r *userPlatformQuotaRepository) UpsertForUser(ctx context.Context, userID int64, records []UserPlatformQuotaRecord) error {
	return r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		platforms := make([]string, 0, len(records))
		for _, rec := range records {
			platforms = append(platforms, rec.Platform)
		}
		now := time.Now()
		if err := clearMissingPlatformLimits(txCtx, txClient, userID, platforms, now); err != nil {
			return err
		}
		for _, rec := range records {
			affected, err := updateLimitsRow(txCtx, txClient, userID, rec, now)
			if err != nil {
				return err
			}
			if affected == 0 && rec.HasAnyLimit() {
				if err := insertLimitsRow(txCtx, txClient, userID, rec, now); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// clearMissingPlatformLimits 只清空未提交平台的限额，保留既有用量与待刷快照的目标行。
func clearMissingPlatformLimits(ctx context.Context, client *dbent.Client, userID int64, keepPlatforms []string, now time.Time) error {
	var (
		query string
		args  []any
	)
	if len(keepPlatforms) == 0 {
		query = `UPDATE user_platform_quotas SET daily_limit_usd = NULL, weekly_limit_usd = NULL, monthly_limit_usd = NULL, updated_at = $2
		         WHERE user_id = $1 AND deleted_at IS NULL`
		args = []any{userID, now}
	} else {
		placeholders := make([]string, len(keepPlatforms))
		args = make([]any, 0, len(keepPlatforms)+2)
		args = append(args, userID, now)
		for i, p := range keepPlatforms {
			placeholders[i] = fmt.Sprintf("$%d", i+3)
			args = append(args, p)
		}
		query = fmt.Sprintf(`UPDATE user_platform_quotas SET daily_limit_usd = NULL, weekly_limit_usd = NULL, monthly_limit_usd = NULL, updated_at = $2
		         WHERE user_id = $1 AND deleted_at IS NULL AND platform NOT IN (%s)`,
			strings.Join(placeholders, ","))
	}
	_, err := client.ExecContext(ctx, query, args...)
	return err
}

// updateLimitsRow 尝试 UPDATE active 行（deleted_at IS NULL），返回受影响行数。
// 仅更新 active 行：若存在多条历史软删记录，加 deleted_at IS NULL 守卫可避免
// 批量重激活导致的 partial unique index（userplatformquota_user_id_platform_uq）冲突。
// affected=0 时由调用方 UpsertForUser 走 insertLimitsRow 路径创建新行。
func updateLimitsRow(ctx context.Context, client *dbent.Client, userID int64, rec UserPlatformQuotaRecord, now time.Time) (int64, error) {
	const query = `UPDATE user_platform_quotas
		SET daily_limit_usd = $1, weekly_limit_usd = $2, monthly_limit_usd = $3,
		    deleted_at = NULL, updated_at = $4
		WHERE user_id = $5 AND platform = $6 AND deleted_at IS NULL`
	res, err := client.ExecContext(ctx, query,
		rec.DailyLimitUSD, rec.WeeklyLimitUSD, rec.MonthlyLimitUSD, now,
		userID, rec.Platform)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// insertLimitsRow 插入新限额行（usage 默认 0，window_start 默认 NULL）。
// 带 ON CONFLICT ... DO NOTHING 守卫：防止两个并发请求同时为同一 user/platform 新建行时
// 触发 unique constraint 违反（userplatformquota_user_id_platform_uq 部分唯一索引）。
// affected=0 时说明另一个并发请求刚完成 INSERT，fallback 到 updateLimitsRow 覆写 limits 值。
func insertLimitsRow(ctx context.Context, client *dbent.Client, userID int64, rec UserPlatformQuotaRecord, now time.Time) error {
	const query = `INSERT INTO user_platform_quotas
		(user_id, platform, daily_limit_usd, weekly_limit_usd, monthly_limit_usd,
		 daily_usage_usd, weekly_usage_usd, monthly_usage_usd, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 0, 0, 0, $6, $6)
		ON CONFLICT (user_id, platform) WHERE deleted_at IS NULL DO NOTHING`
	res, err := client.ExecContext(ctx, query,
		userID, rec.Platform,
		rec.DailyLimitUSD, rec.WeeklyLimitUSD, rec.MonthlyLimitUSD,
		now)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		// 并发情形：另一请求已插入该行，fallback 到 UPDATE 覆写 limits 值（last-writer-wins）。
		_, err = updateLimitsRow(ctx, client, userID, rec, now)
		return err
	}
	return nil
}

// batchRows 是 BatchSnapshotUsage 每批最大行数（9 参/行 × 6000 ≈ 54000 参,低于 Postgres 65535 上限）。
const batchRows = 6000

// BatchSnapshotUsage 用一条多行 UPDATE ... FROM (VALUES ...) 把整批 usage 以绝对值覆盖写入既有活跃行（非累加）。
// 每批最多 batchRows 行；$1=now 共用；每行 8 个 per-row 参（user_id, platform, 3×usage, 3×window_start）。
// 只命中 deleted_at IS NULL 的既有行：行不存在或已软删的 (user, platform) 直接跳过，不建行。
//
// 注意:snapshots 超过 batchRows 会分多条 SQL 执行且【非单事务】——若某子批失败,
// 先前子批已写入无法回滚。调用方(flusher)应保证单次 batchSize ≤ batchRows
// (默认 flush_batch_size=1000 < 6000,安全)。
// 另注:启用 flusher 后,本绝对值覆盖与 admin 直写 DB(ResetExpiredWindow/UpsertForUser)存在覆盖竞态,
// 详见 service/user_platform_quota_flusher.go 中 flushOneBatch 的"已知竞态"注释。
func (r *userPlatformQuotaRepository) BatchSnapshotUsage(ctx context.Context, snapshots []UserPlatformQuotaSnapshot, now time.Time) error {
	if len(snapshots) == 0 {
		return nil
	}

	client := clientFromContext(ctx, r.client)

	for start := 0; start < len(snapshots); start += batchRows {
		end := start + batchRows
		if end > len(snapshots) {
			end = len(snapshots)
		}
		batch := snapshots[start:end]

		var sb strings.Builder
		_, _ = sb.WriteString(
			"UPDATE user_platform_quotas AS q SET" +
				"  daily_usage_usd      = v.daily_usage_usd," +
				"  weekly_usage_usd     = v.weekly_usage_usd," +
				"  monthly_usage_usd    = v.monthly_usage_usd," +
				"  daily_window_start   = v.daily_window_start," +
				"  weekly_window_start  = v.weekly_window_start," +
				"  monthly_window_start = v.monthly_window_start," +
				"  updated_at           = $1" +
				" FROM (VALUES ")

		// $1 = now（共用）；每行 8 个 per-row 参，从 $2 起连续编号。
		// VALUES 里的占位符显式转型，避免 Postgres 对多行 VALUES 推断不出参数类型。
		args := []any{now}
		for i, s := range batch {
			if i > 0 {
				_, _ = sb.WriteString(",")
			}
			b := len(args) // 当前 per-row 第一个参数的 0-based 索引，实际占位符 = b+1
			fmt.Fprintf(&sb, "($%d::bigint,$%d::varchar,$%d::numeric,$%d::numeric,$%d::numeric,$%d::timestamptz,$%d::timestamptz,$%d::timestamptz)",
				b+1, b+2, b+3, b+4, b+5, b+6, b+7, b+8)
			args = append(args,
				s.UserID, s.Platform,
				s.DailyUsageUSD, s.WeeklyUsageUSD, s.MonthlyUsageUSD,
				s.DailyWindowStart, s.WeeklyWindowStart, s.MonthlyWindowStart,
			)
		}

		_, _ = sb.WriteString(
			") AS v(user_id, platform, daily_usage_usd, weekly_usage_usd, monthly_usage_usd," +
				" daily_window_start, weekly_window_start, monthly_window_start)" +
				" WHERE q.user_id = v.user_id AND q.platform = v.platform AND q.deleted_at IS NULL")

		if _, err := client.ExecContext(ctx, sb.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

// configuredQuotaRecords 仅保留新建时有实际限额配置的记录，不改写原切片。
func configuredQuotaRecords(records []UserPlatformQuotaRecord) []UserPlatformQuotaRecord {
	out := make([]UserPlatformQuotaRecord, 0, len(records))
	for _, record := range records {
		if record.HasAnyLimit() {
			out = append(out, record)
		}
	}
	return out
}
