package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	imageTaskDurableBatch       = 100
	imageTaskLegacyBudget       = 32 * time.Minute
	imageTaskResultTTL          = 24 * time.Hour
	imageTaskRecordLimit        = (1 << 20) + (16 << 10)
	imageTaskPrimaryBudget      = 3 * time.Second
	imageTaskCacheBudget        = time.Second
	imageTaskInterruptedSummary = "执行中断，结果不确定且可能已计费；系统未重新生成"
)

type durableImageTaskStore struct {
	db  *sql.DB
	rdb *redis.Client
}

// NewDurableImageTaskStore 将 PostgreSQL 作为结果主存储，Redis 只承担缓存和短时写入补偿。
func NewDurableImageTaskStore(db *sql.DB, rdb *redis.Client) service.ImageTaskStore {
	if rdb != nil {
		// 使用共享连接池的独立客户端视图，限制本模块缓存等待，不改变其它业务 Redis 超时。
		rdb = rdb.WithTimeout(imageTaskCacheBudget)
		rdb.Options().ContextTimeoutEnabled = true
		rdb.Options().MaxRetries = 0
		// 当前 go-redis 的超时视图不继承 hooks，补回项目已有的按请求计时。
		rdb.AddHook(serverTimingRedisHook{})
	}
	return &durableImageTaskStore{db: db, rdb: rdb}
}

var _ service.ImageTaskDurableStore = (*durableImageTaskStore)(nil)

// Save 首次受理必须落 PostgreSQL；终态与列表同事务，缓存故障不会丢掉数据库结果。
func (s *durableImageTaskStore) Save(ctx context.Context, task *service.ImageTaskRecord, ttl time.Duration) error {
	next, err := prepareDurableImageTask(task, ttl)
	if err != nil {
		return err
	}
	stored, err := s.savePostgres(ctx, next, false, time.Now())
	if err == nil {
		_, _ = s.mirror(ctx, stored, false)
		return nil
	}
	if errors.Is(err, service.ErrImageTaskNotFound) || !imageTaskTerminal(next) {
		return err
	}
	// 只允许已受理且执行身份一致的任务降级，不能在数据库故障期间受理新任务。
	ok, cacheErr := s.mirror(ctx, next, true)
	if cacheErr == nil && ok {
		return nil
	}
	return err
}

// Get 优先返回数据库终态；缓存里的更新终态会补写数据库和列表，不触发生成或扣费。
func (s *durableImageTaskStore) Get(ctx context.Context, id string) (*service.ImageTaskRecord, error) {
	id = strings.TrimSpace(id)
	task, err := s.getPostgres(ctx, id)
	if err != nil {
		cached, cacheErr := s.getRedis(ctx, id)
		if cacheErr != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if errors.Is(cacheErr, redis.Nil) || errors.Is(cacheErr, service.ErrImageTaskNotFound) {
					return nil, service.ErrImageTaskNotFound
				}
				return nil, cacheErr
			}
			return nil, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			// 滚动升级兼容旧 Redis 任务，但不从请求正文或图片内容伪造列表元数据。
			stored, writeErr := s.savePostgres(ctx, cached, false, time.Now())
			if writeErr == nil {
				task = stored
			} else {
				task = cached
			}
		} else {
			task = cached
		}
	} else if !imageTaskTerminal(task) || imageTaskInterrupted(task) {
		cached, cacheErr := s.getRedis(ctx, id)
		if cacheErr == nil && sameImageTaskExecution(task, cached) && imageTaskTerminal(cached) &&
			(!imageTaskInterrupted(cached) || !imageTaskTerminal(task)) {
			stored, writeErr := s.savePostgres(ctx, cached, false, time.Now())
			if writeErr == nil {
				task = stored
			} else {
				task = cached
			}
		}
	}
	if task.ExpiresAt <= time.Now().Unix() {
		return nil, service.ErrImageTaskNotFound
	}
	return task, nil
}

// Heartbeat 只续租当前执行者，不能复活真实终态，也不能突破独立的最长执行期限。
func (s *durableImageTaskStore) Heartbeat(ctx context.Context, id, executionID string, leaseExpiresAt int64) error {
	if s.db == nil {
		return service.ErrImageTaskUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, imageTaskPrimaryBudget)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE image_tasks SET
 lease_expires_at=LEAST($3,execution_deadline),
 record=jsonb_set(record,'{lease_expires_at}',to_jsonb(LEAST($3,execution_deadline)))
 WHERE id=$1 AND execution_id=$2 AND status='processing' AND execution_deadline>$4`, id, executionID, leaseExpiresAt, time.Now().Unix())
	return err
}

// Maintain 限量修复缓存终态或登记失联任务；从不重新请求上游，避免重复生成和扣费。
func (s *durableImageTaskStore) Maintain(ctx context.Context, now time.Time) error {
	if s.db == nil {
		return service.ErrImageTaskUnavailable
	}
	// 兼容旧任务是尽力修复，不能耗尽整轮预算而饿死新任务的租约恢复。
	legacyCtx, legacyCancel := context.WithTimeout(ctx, 2*time.Second)
	legacyErr := s.importLegacyTasks(legacyCtx, now)
	legacyTimedOut := errors.Is(legacyCtx.Err(), context.DeadlineExceeded)
	legacyCancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	readCtx, readCancel := context.WithTimeout(ctx, imageTaskPrimaryBudget)
	defer readCancel()
	rows, err := s.db.QueryContext(readCtx, `SELECT id FROM image_tasks WHERE status='processing'
 OR (status='failed' AND record->'error'->>'type'='execution_interrupted')
 ORDER BY maintained_at,id LIMIT $1`, imageTaskDurableBatch)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	var firstErr error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = s.maintainTask(ctx, id, now); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// 结果到期只删除结果表，媒体任务历史与既有使用记录继续保留。
	deleteCtx, deleteCancel := context.WithTimeout(ctx, imageTaskPrimaryBudget)
	defer deleteCancel()
	_, err = s.db.ExecContext(deleteCtx, `DELETE FROM image_tasks WHERE id IN
 (SELECT id FROM image_tasks WHERE status<>'processing' AND expires_at<=$1 ORDER BY expires_at LIMIT $2)`, now.Unix(), imageTaskDurableBatch)
	if firstErr != nil {
		return firstErr
	}
	if err == nil && legacyErr != nil && !legacyTimedOut && !errors.Is(legacyErr, context.DeadlineExceeded) {
		return legacyErr
	}
	return err
}

// importLegacyTasks 让只查看列表、没有调用旧任务轮询接口的用户也能完成滚动升级。
func (s *durableImageTaskStore) importLegacyTasks(ctx context.Context, now time.Time) error {
	readCtx, cancel := context.WithTimeout(ctx, imageTaskPrimaryBudget)
	defer cancel()
	rows, err := s.db.QueryContext(readCtx, `SELECT m.task_id,m.user_id,m.api_key_id,
 EXTRACT(EPOCH FROM m.created_at)::bigint,EXTRACT(EPOCH FROM COALESCE(m.expires_at,m.created_at+INTERVAL '24 hours'))::bigint
 FROM media_tasks m WHERE m.source='async_image' AND m.status='processing'
 AND (m.expires_at IS NULL OR m.expires_at>$1)
 AND NOT EXISTS(SELECT 1 FROM image_tasks t WHERE t.id=m.task_id)
 ORDER BY m.created_at,m.id LIMIT $2`, now.UTC(), imageTaskDurableBatch)
	if err != nil {
		return err
	}
	var legacy []service.ImageTaskRecord
	for rows.Next() {
		var task service.ImageTaskRecord
		if err = rows.Scan(&task.ID, &task.UserID, &task.APIKeyID, &task.CreatedAt, &task.ExpiresAt); err != nil {
			_ = rows.Close()
			return err
		}
		task.Status = service.ImageTaskStatusProcessing
		legacy = append(legacy, task)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for i := range legacy {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		task := &legacy[i]
		cached, cacheErr := s.getRedis(ctx, task.ID)
		if cacheErr == nil {
			if cached.UserID != task.UserID || cached.APIKeyID != task.APIKeyID {
				continue
			}
			task = cached
		} else {
			// 缓存暂时离线不代表任务丢失；缓存确实缺失时也等待完整的旧执行期限。
			if !errors.Is(cacheErr, redis.Nil) && !errors.Is(cacheErr, service.ErrImageTaskNotFound) {
				continue
			}
			if now.Unix() < task.CreatedAt+int64(imageTaskLegacyBudget/time.Second) {
				continue
			}
		}
		prepared, prepErr := prepareDurableImageTask(task, 0)
		if prepErr != nil {
			return prepErr
		}
		if _, err = s.savePostgres(ctx, prepared, cacheErr != nil, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *durableImageTaskStore) maintainTask(ctx context.Context, id string, now time.Time) error {
	task, err := s.getPostgres(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if cached, cacheErr := s.getRedis(ctx, id); cacheErr == nil && sameImageTaskExecution(task, cached) && imageTaskTerminal(cached) {
		stored, writeErr := s.savePostgres(ctx, cached, false, now)
		if writeErr != nil {
			return writeErr
		}
		_, _ = s.mirror(ctx, stored, false)
		return nil
	}
	if imageTaskLeaseExpired(task, now) {
		stored, writeErr := s.savePostgres(ctx, task, true, now)
		if writeErr != nil {
			return writeErr
		}
		_, _ = s.mirror(ctx, stored, false)
		return nil
	}
	writeCtx, cancel := context.WithTimeout(ctx, imageTaskPrimaryBudget)
	defer cancel()
	_, err = s.db.ExecContext(writeCtx, `UPDATE image_tasks SET maintained_at=$2 WHERE id=$1`, id, now.UTC())
	return err
}

func (s *durableImageTaskStore) getPostgres(ctx context.Context, id string) (*service.ImageTaskRecord, error) {
	if s.db == nil {
		return nil, service.ErrImageTaskUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, imageTaskPrimaryBudget)
	defer cancel()
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT record FROM image_tasks WHERE id=$1`, id).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return decodeDurableImageTask(raw)
}

func (s *durableImageTaskStore) getRedis(ctx context.Context, id string) (*service.ImageTaskRecord, error) {
	if s.rdb == nil {
		return nil, service.ErrImageTaskNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, imageTaskCacheBudget)
	defer cancel()
	raw, err := s.rdb.Get(ctx, imageTaskKey(id)).Bytes()
	if err != nil {
		return nil, err
	}
	task, err := decodeDurableImageTask(raw)
	if err != nil {
		return nil, err
	}
	if task.ID != id || task.ExpiresAt <= time.Now().Unix() {
		return nil, service.ErrImageTaskNotFound
	}
	return task, nil
}

func decodeDurableImageTask(raw []byte) (*service.ImageTaskRecord, error) {
	var task service.ImageTaskRecord
	if err := json.Unmarshal(raw, &task); err != nil {
		return nil, err
	}
	return prepareDurableImageTask(&task, 0)
}

func prepareDurableImageTask(task *service.ImageTaskRecord, ttl time.Duration) (*service.ImageTaskRecord, error) {
	if task == nil || task.ID == "" || len(task.ID) > 255 || task.UserID <= 0 || task.APIKeyID <= 0 || len(task.ExecutionID) > 64 ||
		(task.Status != service.ImageTaskStatusProcessing && !imageTaskTerminal(task)) {
		return nil, fmt.Errorf("invalid durable image task")
	}
	// 用已定义结构序列化，不接受未知请求字段；结果内禁止内嵌图片。
	if len(task.Result) > 0 {
		var result any
		if json.Unmarshal(task.Result, &result) != nil || hasInlineImage(result) {
			return nil, fmt.Errorf("image task result must contain compact image URLs")
		}
	}
	raw, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	if len(raw) > imageTaskRecordLimit {
		return nil, fmt.Errorf("image task record exceeds metadata limit")
	}
	var copy service.ImageTaskRecord
	if err = json.Unmarshal(raw, &copy); err != nil {
		return nil, err
	}
	if copy.ExpiresAt == 0 && ttl > 0 {
		copy.ExpiresAt = time.Now().Add(ttl).Unix()
	}
	// 旧实例没有租约，保留原 30 分钟生成与 2 分钟上传预算，不能在滚动升级时立刻判失败。
	if copy.ExecutionDeadline <= 0 {
		copy.ExecutionDeadline = copy.CreatedAt + int64(imageTaskLegacyBudget/time.Second)
	}
	if copy.LeaseExpiresAt <= 0 {
		copy.LeaseExpiresAt = copy.ExecutionDeadline
	}
	return &copy, nil
}

func hasInlineImage(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if k == "b64_json" && v != "" && v != nil {
				return true
			}
			if hasInlineImage(v) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if hasInlineImage(v) {
				return true
			}
		}
	case string:
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(x)), "data:image/")
	}
	return false
}

func sameImageTaskExecution(a, b *service.ImageTaskRecord) bool {
	return a.ID == b.ID && a.UserID == b.UserID && a.APIKeyID == b.APIKeyID && a.ExecutionID == b.ExecutionID
}

func imageTaskTerminal(task *service.ImageTaskRecord) bool {
	return task.Status == service.ImageTaskStatusCompleted || task.Status == service.ImageTaskStatusFailed
}

func imageTaskInterrupted(task *service.ImageTaskRecord) bool {
	var e struct {
		Type string `json:"type"`
	}
	return task.Status == service.ImageTaskStatusFailed && json.Unmarshal(task.Error, &e) == nil && e.Type == "execution_interrupted"
}

func imageTaskLeaseExpired(task *service.ImageTaskRecord, now time.Time) bool {
	return !imageTaskTerminal(task) && (task.LeaseExpiresAt <= now.Unix() || task.ExecutionDeadline <= now.Unix())
}

func (s *durableImageTaskStore) savePostgres(ctx context.Context, next *service.ImageTaskRecord, reap bool, now time.Time) (*service.ImageTaskRecord, error) {
	if s.db == nil {
		return nil, service.ErrImageTaskUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, imageTaskPrimaryBudget)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	raw, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	// INSERT 先占据唯一 ID，后续并发写通过行锁串行决定状态；不依赖应用进程锁。
	_, err = tx.ExecContext(ctx, `INSERT INTO image_tasks(id,user_id,api_key_id,status,execution_id,lease_expires_at,execution_deadline,expires_at,record)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(id) DO NOTHING`, next.ID, next.UserID, next.APIKeyID, next.Status, next.ExecutionID, next.LeaseExpiresAt, next.ExecutionDeadline, next.ExpiresAt, raw)
	if err != nil {
		return nil, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT record FROM image_tasks WHERE id=$1 FOR UPDATE`, next.ID).Scan(&raw); err != nil {
		return nil, err
	}
	current, err := decodeDurableImageTask(raw)
	if err != nil {
		return nil, err
	}
	if !sameImageTaskExecution(current, next) {
		return nil, service.ErrImageTaskNotFound
	}
	chosen := next
	if reap {
		chosen = current
		// 行锁内重新检查心跳，避免维护扫描刚结束就误判另一个活跃实例。
		if imageTaskLeaseExpired(current, now) {
			completed := now.Unix()
			chosen.Status = service.ImageTaskStatusFailed
			chosen.HTTPStatus = http.StatusServiceUnavailable
			chosen.CompletedAt = &completed
			chosen.ExpiresAt = now.Add(imageTaskResultTTL).Unix()
			chosen.Result = nil
			chosen.Error = json.RawMessage(`{"type":"execution_interrupted","message":"Image execution was interrupted; the upstream result is unknown and generation usage may already have been billed. The request was not replayed."}`)
		}
	} else if imageTaskTerminal(current) && (!imageTaskInterrupted(current) || !imageTaskTerminal(next) || imageTaskInterrupted(next)) {
		chosen = current
	} else if !imageTaskTerminal(next) {
		// 迟到的 processing 写不能缩短已经续过的租约，也不能扩大硬期限。
		if current.LeaseExpiresAt > next.LeaseExpiresAt {
			next.LeaseExpiresAt = current.LeaseExpiresAt
		}
		next.ExecutionDeadline = current.ExecutionDeadline
	}
	if chosen.Observation == nil {
		chosen.Observation = current.Observation
	}
	if chosen.Observation == nil {
		// 旧版 Redis 记录没有 observation，只从已存在的安全投影补充，绝不凭空编造归属。
		chosen.Observation, err = loadLegacyImageObservation(ctx, tx, chosen)
		if err != nil {
			return nil, err
		}
	}
	normalizeImageTaskObservation(chosen)
	raw, err = json.Marshal(chosen)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE image_tasks SET status=$2,lease_expires_at=$3,execution_deadline=$4,expires_at=$5,record=$6,maintained_at=$7 WHERE id=$1`, chosen.ID, chosen.Status, chosen.LeaseExpiresAt, chosen.ExecutionDeadline, chosen.ExpiresAt, raw, now.UTC())
	if err != nil {
		return nil, err
	}
	if chosen.Observation != nil {
		if err = projectDurableImageObservation(ctx, tx, *chosen.Observation); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return chosen, nil
}

func normalizeImageTaskObservation(task *service.ImageTaskRecord) {
	o := task.Observation
	if o == nil {
		return
	}
	o.Source, o.TaskID, o.MediaType = "async_image", task.ID, "image"
	o.UserID, o.APIKeyID, o.Status, o.HTTPStatus = task.UserID, task.APIKeyID, task.Status, task.HTTPStatus
	o.CreatedAt = time.Unix(task.CreatedAt, 0).UTC()
	expires := time.Unix(task.ExpiresAt, 0).UTC()
	o.ExpiresAt = &expires
	o.CompletedAt = nil
	if task.CompletedAt != nil {
		completed := time.Unix(*task.CompletedAt, 0).UTC()
		o.CompletedAt = &completed
	}
	// 列表仅展示固定分类，原错误对象只留在短期任务结果内。
	o.ErrorMessage = ""
	if task.Status == service.ImageTaskStatusFailed {
		o.ErrorMessage = "图片生成失败，请根据请求 ID 查看错误日志"
		var envelope struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(task.Error, &envelope) == nil {
			if strings.Contains(envelope.Message, "image was generated but object storage failed") {
				o.ErrorMessage = "图片生成完成但结果存储失败，生成用量可能已经计费"
			} else if envelope.Type == "timeout_error" {
				o.ErrorMessage = "图片生成任务超时"
			}
		}
	}
	if imageTaskInterrupted(task) {
		o.ErrorMessage = imageTaskInterruptedSummary
	}
}

func loadLegacyImageObservation(ctx context.Context, tx *sql.Tx, task *service.ImageTaskRecord) (*service.MediaTaskObservation, error) {
	var o service.MediaTaskObservation
	err := tx.QueryRowContext(ctx, `SELECT source,task_id,media_type,platform,model,status,upstream_status,user_id,api_key_id,group_id,account_id,
 http_status,error_message,request_id,created_at,completed_at,expires_at FROM media_tasks WHERE source='async_image' AND task_id=$1 AND user_id=$2 AND api_key_id=$3`, task.ID, task.UserID, task.APIKeyID).
		Scan(&o.Source, &o.TaskID, &o.MediaType, &o.Platform, &o.Model, &o.Status, &o.UpstreamStatus, &o.UserID, &o.APIKeyID, &o.GroupID, &o.AccountID, &o.HTTPStatus, &o.ErrorMessage, &o.RequestID, &o.CreatedAt, &o.CompletedAt, &o.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &o, err
}

func projectDurableImageObservation(ctx context.Context, tx *sql.Tx, o service.MediaTaskObservation) error {
	// 新任务状态受行锁校验；滚动升级期间也保留旧实例已经写出的真实终态。
	// 只有维护器的固定中断分类可以被随后到达的真实结果纠正。
	const preserve = `media_tasks.status IN ('completed','failed','cancelled','expired') AND media_tasks.error_message<>$18`
	result, err := tx.ExecContext(ctx, `INSERT INTO media_tasks
 (source,task_id,media_type,platform,model,status,upstream_status,user_id,api_key_id,group_id,account_id,http_status,error_message,request_id,created_at,completed_at,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
 ON CONFLICT(source,task_id,api_key_id) DO UPDATE SET
 status=CASE WHEN `+preserve+` THEN media_tasks.status ELSE EXCLUDED.status END,
 upstream_status=CASE WHEN `+preserve+` THEN media_tasks.upstream_status ELSE EXCLUDED.upstream_status END,
 http_status=CASE WHEN `+preserve+` THEN media_tasks.http_status ELSE EXCLUDED.http_status END,
 error_message=CASE WHEN `+preserve+` THEN media_tasks.error_message ELSE EXCLUDED.error_message END,
 group_id=COALESCE(media_tasks.group_id,EXCLUDED.group_id),account_id=COALESCE(media_tasks.account_id,EXCLUDED.account_id),
 model=CASE WHEN media_tasks.model='' THEN EXCLUDED.model ELSE media_tasks.model END,
 request_id=CASE WHEN media_tasks.request_id='' THEN EXCLUDED.request_id ELSE media_tasks.request_id END,
 created_at=LEAST(media_tasks.created_at,EXCLUDED.created_at),
 completed_at=CASE WHEN `+preserve+` THEN media_tasks.completed_at ELSE EXCLUDED.completed_at END,
 expires_at=CASE WHEN `+preserve+` THEN media_tasks.expires_at ELSE EXCLUDED.expires_at END,
 updated_at=NOW() WHERE media_tasks.user_id=EXCLUDED.user_id`,
		o.Source, o.TaskID, o.MediaType, o.Platform, o.Model, o.Status, o.UpstreamStatus, o.UserID, o.APIKeyID, o.GroupID, o.AccountID, o.HTTPStatus, o.ErrorMessage, o.RequestID, o.CreatedAt, o.CompletedAt, o.ExpiresAt, imageTaskInterruptedSummary)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return service.ErrImageTaskNotFound
	}
	return err
}

// 原子比较执行归属和终态，避免数据库提交之后较慢的缓存写覆盖更新结果。
var durableImageTaskMirror = redis.NewScript(`
local raw=redis.call('GET',KEYS[1])
local incoming=cjson.decode(ARGV[1])
if ARGV[3]=='1' and not raw then return 0 end
if raw then
 local old=cjson.decode(raw)
 if old.id~=incoming.id or old.user_id~=incoming.user_id or old.api_key_id~=incoming.api_key_id or (old.execution_id or '')~=(incoming.execution_id or '') then return 0 end
 if old.status=='completed' or old.status=='failed' then
  local interrupted=old.status=='failed' and type(old.error)=='table' and old.error.type=='execution_interrupted'
  local incomingTerminal=incoming.status=='completed' or incoming.status=='failed'
  local incomingInterrupted=incoming.status=='failed' and type(incoming.error)=='table' and incoming.error.type=='execution_interrupted'
  if not interrupted or not incomingTerminal or incomingInterrupted then return 1 end
 end
end
redis.call('SET',KEYS[1],ARGV[1],'PX',ARGV[2])
return 1
`)

func (s *durableImageTaskStore) mirror(ctx context.Context, task *service.ImageTaskRecord, requireExisting bool) (bool, error) {
	if s.rdb == nil {
		return false, service.ErrImageTaskUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, imageTaskCacheBudget)
	defer cancel()
	ttl := time.Until(time.Unix(task.ExpiresAt, 0))
	if ttl <= 0 {
		return false, service.ErrImageTaskNotFound
	}
	raw, err := json.Marshal(task)
	if err != nil {
		return false, err
	}
	required := "0"
	if requireExisting {
		required = "1"
	}
	n, err := durableImageTaskMirror.Run(ctx, s.rdb, []string{imageTaskKey(task.ID)}, raw, ttl.Milliseconds(), required).Int()
	return n == 1, err
}
