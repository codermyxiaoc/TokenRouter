//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func durableImageFixture(t *testing.T) (*durableImageTaskStore, *service.ImageTaskRecord, *redis.Client) {
	t.Helper()
	_ = testEntClient(t)
	ctx := context.Background()
	var userID, keyID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'test') RETURNING id`, t.Name()+"@example.test").Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, userID, t.Name()).Scan(&keyID))
	// 使用专用测试数据库隔离缓存；当前 go-redis 的 WithTimeout 视图不继承测试命名空间 hook。
	opts := *integrationRedis.Options()
	opts.DB = 14
	rdb := redis.NewClient(&opts)
	t.Cleanup(func() {
		cleanup := redis.NewClient(&opts)
		require.NoError(t, cleanup.FlushDB(context.Background()).Err())
		_ = cleanup.Close()
		_ = rdb.Close()
	})
	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(24 * time.Hour)
	task := &service.ImageTaskRecord{ID: "imgtask_" + t.Name(), UserID: userID, APIKeyID: keyID, Status: service.ImageTaskStatusProcessing,
		CreatedAt: now.Unix(), ExpiresAt: expires.Unix(), ExecutionID: "test-executor", LeaseExpiresAt: now.Add(90 * time.Second).Unix(), ExecutionDeadline: now.Add(33 * time.Minute).Unix(),
		Observation: &service.MediaTaskObservation{Source: "async_image", MediaType: "image", Platform: "openai", Model: "gpt-image-2", TaskID: "ignored-owner-fields", UserID: userID, APIKeyID: keyID, Status: "processing", CreatedAt: now, ExpiresAt: &expires, RequestID: "client:" + t.Name()}}
	return NewDurableImageTaskStore(integrationDB, rdb).(*durableImageTaskStore), task, rdb
}

func completeDurableImage(task *service.ImageTaskRecord) *service.ImageTaskRecord {
	copy := *task
	now := time.Now().Unix()
	copy.Status, copy.HTTPStatus = service.ImageTaskStatusCompleted, 200
	copy.CompletedAt = &now
	copy.ExpiresAt = time.Now().Add(24 * time.Hour).Unix()
	copy.Result = json.RawMessage(`{"data":[{"url":"https://images.example.test/generated.png"}],"usage":{"output_tokens":42}}`)
	copy.Error = nil
	return &copy
}

func durableImageStatuses(t *testing.T, id string) (string, string) {
	t.Helper()
	var stored, projected string
	require.NoError(t, integrationDB.QueryRow(`SELECT status FROM image_tasks WHERE id=$1`, id).Scan(&stored))
	require.NoError(t, integrationDB.QueryRow(`SELECT status FROM media_tasks WHERE source='async_image' AND task_id=$1`, id).Scan(&projected))
	return stored, projected
}

func TestDurableImageTaskIntegrationRedisFailureAndProcessRecreation(t *testing.T) {
	s, task, rdb := durableImageFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	// 关闭隔离客户端模拟 Redis 故障；实际数据库和业务 Redis 完全不参与测试。
	require.NoError(t, rdb.Close())
	completed := completeDurableImage(task)
	require.NoError(t, s.Save(ctx, completed, 24*time.Hour), "Redis 写失败不得抹去数据库终态")
	recreated := NewDurableImageTaskStore(integrationDB, nil)
	got, err := recreated.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStatusCompleted, got.Status)
	require.JSONEq(t, string(completed.Result), string(got.Result))
	stored, projected := durableImageStatuses(t, task.ID)
	require.Equal(t, "completed", stored)
	require.Equal(t, "completed", projected)
	// 重建服务后，所有权校验仍必须由原用户和原 Key 一起约束。
	svc := service.NewImageTaskService(recreated)
	_, err = svc.Get(ctx, service.ImageTaskOwner{UserID: task.UserID + 1, APIKeyID: task.APIKeyID}, task.ID)
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
	_, err = svc.Get(ctx, service.ImageTaskOwner{UserID: task.UserID, APIKeyID: task.APIKeyID + 1}, task.ID)
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
}

// 在隔离 PostgreSQL 注入投影失败，验证结果和列表共用同一事务。
func blockDurableImageProjection(t *testing.T) func() {
	t.Helper()
	_, err := integrationDB.Exec(`CREATE FUNCTION test_image_projection_failure() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected projection failure'; END $$;
 CREATE TRIGGER test_image_projection_failure BEFORE INSERT OR UPDATE ON media_tasks FOR EACH ROW EXECUTE FUNCTION test_image_projection_failure()`)
	require.NoError(t, err)
	var once sync.Once
	restore := func() {
		once.Do(func() {
			_, err := integrationDB.Exec(`DROP TRIGGER test_image_projection_failure ON media_tasks; DROP FUNCTION test_image_projection_failure()`)
			require.NoError(t, err)
		})
	}
	t.Cleanup(restore)
	return restore
}

func TestDurableImageTaskIntegrationTerminalFallbackAndReadRepair(t *testing.T) {
	s, task, rdb := durableImageFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	restore := blockDurableImageProjection(t)
	require.NoError(t, s.Save(ctx, completeDurableImage(task), 24*time.Hour), "已有任务可把终态暂存 Redis")
	stored, projected := durableImageStatuses(t, task.ID)
	require.Equal(t, "processing", stored, "列表失败必须回滚结果表，不允许半次提交")
	require.Equal(t, "processing", projected)
	var cached service.ImageTaskRecord
	raw, err := rdb.Get(ctx, imageTaskKey(task.ID)).Bytes()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &cached))
	require.Equal(t, "completed", cached.Status)
	got, err := s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status, "投影仍故障时可以返回已保存的终态")
	restore()
	got, err = s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status)
	stored, projected = durableImageStatuses(t, task.ID)
	require.Equal(t, "completed", stored)
	require.Equal(t, "completed", projected)
	// 缓存删除后仍能读取完整结果，修复没有依赖随后持续在线的客户端。
	require.NoError(t, rdb.Del(ctx, imageTaskKey(task.ID)).Err())
	got, err = s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.NotEmpty(t, got.Result)
}

func TestDurableImageTaskIntegrationInitialFailureIsNotAccepted(t *testing.T) {
	s, task, rdb := durableImageFixture(t)
	ctx := context.Background()
	restore := blockDurableImageProjection(t)
	require.Error(t, s.Save(ctx, task, 24*time.Hour))
	var n int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM image_tasks WHERE id=$1`, task.ID).Scan(&n))
	require.Zero(t, n)
	require.Zero(t, rdb.Exists(ctx, imageTaskKey(task.ID)).Val())
	// 不能把一个未受理任务直接伪装成终态，借 Redis 绕过数据库失败。
	require.Error(t, s.Save(ctx, completeDurableImage(task), 24*time.Hour))
	require.Zero(t, rdb.Exists(ctx, imageTaskKey(task.ID)).Val())
	restore()
}

func TestDurableImageTaskIntegrationMaintenanceRepairWithoutPolling(t *testing.T) {
	s, task, _ := durableImageFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	restore := blockDurableImageProjection(t)
	require.NoError(t, s.Save(ctx, completeDurableImage(task), 24*time.Hour))
	restore()
	require.NoError(t, s.Maintain(ctx, time.Now()))
	stored, projected := durableImageStatuses(t, task.ID)
	require.Equal(t, "completed", stored)
	require.Equal(t, "completed", projected)
}

func TestDurableImageTaskIntegrationLostLeaseAndLateActualResult(t *testing.T) {
	s, task, _ := durableImageFixture(t)
	ctx := context.Background()
	task.LeaseExpiresAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	// 模拟执行进程已退出，新的仓储实例只登记中断，不生成、不收费。
	recreated := NewDurableImageTaskStore(integrationDB, s.rdb).(service.ImageTaskDurableStore)
	require.NoError(t, recreated.Maintain(ctx, time.Now()))
	got, err := recreated.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
	require.Equal(t, 503, got.HTTPStatus)
	require.Contains(t, string(got.Error), "execution_interrupted")
	require.Contains(t, string(got.Error), "billed")
	stored, projected := durableImageStatuses(t, task.ID)
	require.Equal(t, "failed", stored)
	require.Equal(t, "failed", projected)
	// 维护判定后原执行者返回真实成功，允许纠正未知结果，不允许另一个执行者越权。
	foreign := completeDurableImage(task)
	foreign.ExecutionID = "another-process"
	require.ErrorIs(t, s.Save(ctx, foreign, 24*time.Hour), service.ErrImageTaskNotFound)
	require.NoError(t, s.Save(ctx, completeDurableImage(task), 24*time.Hour))
	stored, projected = durableImageStatuses(t, task.ID)
	require.Equal(t, "completed", stored)
	require.Equal(t, "completed", projected)
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	failed := *task
	failed.Status, failed.HTTPStatus, failed.Error = "failed", 502, json.RawMessage(`{"type":"upstream_error"}`)
	require.NoError(t, s.Save(ctx, &failed, 24*time.Hour))
	got, err = s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status, "真实终态不能被迟到失败或处理中覆盖")
}

func TestDurableImageTaskIntegrationHeartbeatAndHardDeadline(t *testing.T) {
	s, task, _ := durableImageFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	task.LeaseExpiresAt = now.Add(-time.Second).Unix()
	task.ExecutionDeadline = now.Add(3 * time.Minute).Unix()
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	require.NoError(t, s.Heartbeat(ctx, task.ID, task.ExecutionID, now.Add(2*time.Minute).Unix()))
	require.NoError(t, s.Maintain(ctx, now.Add(time.Minute)))
	got, err := s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "processing", got.Status, "其它进程维护不能误伤已续租任务")
	require.NoError(t, s.Heartbeat(ctx, task.ID, "wrong-execution", now.Add(time.Hour).Unix()))
	got, err = s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, now.Add(2*time.Minute).Unix(), got.LeaseExpiresAt)
	require.NoError(t, s.Heartbeat(ctx, task.ID, task.ExecutionID, now.Add(time.Hour).Unix()))
	got, err = s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, task.ExecutionDeadline, got.LeaseExpiresAt, "硬期限不受心跳无限延长")
	require.NoError(t, s.Maintain(ctx, now.Add(4*time.Minute)))
	got, err = s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
}

func TestDurableImageTaskIntegrationLegacyUpgradeAndProjection(t *testing.T) {
	s, task, rdb := durableImageFixture(t)
	ctx := context.Background()
	// 旧版只在 Redis 存任务、在 PostgreSQL 保存独立投影；升级时保留这部分状态。
	o := *task.Observation
	o.TaskID = task.ID
	require.NoError(t, NewMediaTaskRepository(integrationDB).Observe(ctx, o))
	task.ExecutionID = ""
	task.ExecutionDeadline, task.LeaseExpiresAt = 0, 0
	task.Observation = nil
	require.NoError(t, NewImageTaskStore(rdb).Save(ctx, task, 24*time.Hour))
	got, err := s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "processing", got.Status)
	require.Equal(t, task.CreatedAt+32*60, got.ExecutionDeadline)
	require.NotNil(t, got.Observation)
	require.Equal(t, "gpt-image-2", got.Observation.Model)
	require.NoError(t, s.Maintain(ctx, time.Now().Add(31*time.Minute)))
	got, err = s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "processing", got.Status, "兼容仍在旧实例执行的任务")
	require.NoError(t, s.Maintain(ctx, time.Now().Add(33*time.Minute)))
	stored, projected := durableImageStatuses(t, task.ID)
	require.Equal(t, "failed", stored)
	require.Equal(t, "failed", projected)
	// 旧实例完成后回写 Redis，也能纠正升级维护器的未知终态。
	require.NoError(t, NewImageTaskStore(rdb).Save(ctx, completeDurableImage(task), 24*time.Hour))
	require.NoError(t, s.Maintain(ctx, time.Now().Add(34*time.Minute)))
	stored, projected = durableImageStatuses(t, task.ID)
	require.Equal(t, "completed", stored)
	require.Equal(t, "completed", projected)
}

func TestDurableImageTaskIntegrationConcurrentMonotonicAndNoBilling(t *testing.T) {
	s, task, rdb := durableImageFixture(t)
	ctx := context.Background()
	var before int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1`, task.APIKeyID).Scan(&before))
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := range 64 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%8 == 0 {
				errs <- s.Save(ctx, completeDurableImage(task), 24*time.Hour)
			} else {
				errs <- s.Save(ctx, task, 24*time.Hour)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	got, err := s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status)
	var cached service.ImageTaskRecord
	raw, err := rdb.Get(ctx, imageTaskKey(task.ID)).Bytes()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &cached))
	require.Equal(t, "completed", cached.Status, "慢缓存写也必须遵守终态单调")
	for range 30 {
		_, err = s.Get(ctx, task.ID)
		require.NoError(t, err)
	}
	var after, usageCount, projectionCount int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1`, task.APIKeyID).Scan(&after))
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE api_key_id=$1`, task.APIKeyID).Scan(&usageCount))
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM media_tasks WHERE task_id=$1`, task.ID).Scan(&projectionCount))
	require.Equal(t, before, after)
	require.Zero(t, usageCount)
	require.Equal(t, 1, projectionCount)
}

func TestDurableImageTaskIntegrationExpiryRetainsHistory(t *testing.T) {
	s, task, rdb := durableImageFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	completed := completeDurableImage(task)
	completed.ExpiresAt = time.Now().Add(-time.Second).Unix()
	require.NoError(t, s.Save(ctx, completed, time.Second))
	require.NoError(t, rdb.Del(ctx, imageTaskKey(task.ID)).Err())
	_, err := s.Get(ctx, task.ID)
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
	require.NoError(t, s.Maintain(ctx, time.Now()))
	var records, history int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM image_tasks WHERE id=$1`, task.ID).Scan(&records))
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM media_tasks WHERE task_id=$1`, task.ID).Scan(&history))
	require.Zero(t, records)
	require.Equal(t, 1, history)
}

func TestDurableImageTaskIntegrationRejectsInlineImagesAndForeignCache(t *testing.T) {
	s, task, rdb := durableImageFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	for _, result := range []string{`{"data":[{"b64_json":"SGVsbG8="}]}`, `{"data":[{"url":"data:image/png;base64,SGVsbG8="}]}`} {
		invalid := completeDurableImage(task)
		invalid.Result = json.RawMessage(result)
		require.Error(t, s.Save(ctx, invalid, 24*time.Hour))
	}
	foreign := completeDurableImage(task)
	foreign.UserID++
	raw, err := json.Marshal(foreign)
	require.NoError(t, err)
	require.NoError(t, rdb.Set(ctx, imageTaskKey(task.ID), raw, time.Hour).Err())
	got, err := s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "processing", got.Status, "不能用另一个用户的缓存终态修复当前任务")
	require.Equal(t, task.UserID, got.UserID)
	restore := blockDurableImageProjection(t)
	require.Error(t, s.Save(ctx, completeDurableImage(task), 24*time.Hour), "归属不匹配的缓存不能承担数据库失败降级")
	restore()
	t.Log(fmt.Sprintf("用户 %d 的持久任务归属与紧凑结果边界已验证", task.UserID))
}

func TestDurableImageTaskIntegrationPostgresTimeoutLeavesCacheBudget(t *testing.T) {
	s, task, rdb := durableImageFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	// 真正的 PostgreSQL 表锁让读写阻塞，模拟主存储超时，而不是仅模拟立即返回错误。
	locked, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer locked.Rollback()
	_, err = locked.ExecContext(ctx, `LOCK TABLE image_tasks IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	started := time.Now()
	got, err := s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "processing", got.Status)
	require.Less(t, time.Since(started), 5*time.Second, "读取主存储超时后仍有缓存降级预算")
	started = time.Now()
	require.NoError(t, s.Save(ctx, completeDurableImage(task), 24*time.Hour))
	require.Less(t, time.Since(started), 5*time.Second, "事务超时后仍必须把终态写入缓存")
	raw, err := rdb.Get(ctx, imageTaskKey(task.ID)).Bytes()
	require.NoError(t, err)
	require.Contains(t, string(raw), `"status":"completed"`)
	require.NoError(t, locked.Rollback())
	require.NoError(t, s.Maintain(ctx, time.Now()))
	stored, projected := durableImageStatuses(t, task.ID)
	require.Equal(t, "completed", stored)
	require.Equal(t, "completed", projected)
}

func TestDurableImageTaskIntegrationRedisTimeoutDoesNotBlockSavedResult(t *testing.T) {
	s, task, _ := durableImageFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	// 只暂停此测试容器的 Redis，生产客户端和数据库均不参与。
	require.NoError(t, integrationRedis.Do(ctx, "CLIENT", "PAUSE", 2500, "ALL").Err())
	started := time.Now()
	require.NoError(t, s.Save(ctx, completeDurableImage(task), 24*time.Hour))
	require.Less(t, time.Since(started), 2*time.Second, "数据库已成功时缓存超时不得阻塞整个完成流程")
	got, err := s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status)
	// 等隔离容器自行恢复，避免暂停状态影响后面的测试。
	require.NoError(t, integrationRedis.Ping(ctx).Err())
}

func TestDurableImageTaskIntegrationLegacyListOnlyRecovery(t *testing.T) {
	s, task, rdb := durableImageFixture(t)
	ctx := context.Background()
	old := *task.Observation
	old.TaskID = task.ID
	old.CreatedAt = time.Now().Add(-40 * time.Minute)
	require.NoError(t, NewMediaTaskRepository(integrationDB).Observe(ctx, old))
	// 旧任务未调用 GET 且 Redis 记录已丢失，也能在旧执行期限后明确登记中断。
	require.NoError(t, s.Maintain(ctx, time.Now()))
	stored, projected := durableImageStatuses(t, task.ID)
	require.Equal(t, "failed", stored)
	require.Equal(t, "failed", projected)
	got, err := s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Contains(t, string(got.Error), "execution_interrupted")
	// 近期旧任务缓存暂未写入时不能抢先判失败；存在缓存则可导入并等待其旧预算。
	task.ID += "_recent"
	task.ExecutionID = ""
	task.ExecutionDeadline, task.LeaseExpiresAt = 0, 0
	task.Observation.TaskID = task.ID
	require.NoError(t, NewMediaTaskRepository(integrationDB).Observe(ctx, *task.Observation))
	require.NoError(t, s.Maintain(ctx, time.Now()))
	var n int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM image_tasks WHERE id=$1`, task.ID).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, NewImageTaskStore(rdb).Save(ctx, task, 24*time.Hour))
	require.NoError(t, s.Maintain(ctx, time.Now()))
	stored, projected = durableImageStatuses(t, task.ID)
	require.Equal(t, "processing", stored)
	require.Equal(t, "processing", projected)
}

func TestDurableImageTaskIntegrationScalarErrorsAndClassification(t *testing.T) {
	s, task, _ := durableImageFixture(t)
	ctx := context.Background()
	for i, body := range []string{`null`, `true`, `42`, `"failed"`, `{"type":"timeout_error"}`, `{"type":"api_error","message":"image was generated but object storage failed; generation usage may already have been billed"}`} {
		copy := *task
		copy.ID = fmt.Sprintf("%s_%d", task.ID, i)
		require.NoError(t, s.Save(ctx, &copy, 24*time.Hour))
		copy.Status, copy.HTTPStatus, copy.Error = "failed", 502, json.RawMessage(body)
		require.NoError(t, s.Save(ctx, &copy, 24*time.Hour))
		// 重复镜像应当识别合法 JSON 标量，不得因 Lua 访问 .type 抛错。
		ok, err := s.mirror(ctx, &copy, true)
		require.NoError(t, err)
		require.True(t, ok)
		var message string
		require.NoError(t, integrationDB.QueryRow(`SELECT error_message FROM media_tasks WHERE task_id=$1`, copy.ID).Scan(&message))
		if i == 4 {
			require.Equal(t, "图片生成任务超时", message)
		} else if i == 5 {
			require.Contains(t, message, "可能已经计费")
		}
	}
}

func TestDurableImageTaskIntegrationMissingRecordDistinguishesRedisFailure(t *testing.T) {
	s, task, rdb := durableImageFixture(t)
	ctx := context.Background()
	_, err := s.Get(ctx, task.ID)
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
	require.NoError(t, rdb.Close())
	_, err = s.Get(ctx, task.ID)
	require.Error(t, err)
	require.NotErrorIs(t, err, service.ErrImageTaskNotFound, "旧缓存不可达时不能谎报任务明确不存在")
}

type durableImageSlowRedisHook struct{}

func (durableImageSlowRedisHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (durableImageSlowRedisHook) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, _ redis.Cmder) error { <-ctx.Done(); return ctx.Err() }
}
func (durableImageSlowRedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestDurableImageTaskIntegrationLegacyCacheTimeoutCannotStarveRecovery(t *testing.T) {
	s, task, _ := durableImageFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	task.LeaseExpiresAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	for i := range 6 {
		o := *task.Observation
		o.TaskID = fmt.Sprintf("old_image_%d", i)
		o.CreatedAt = time.Now().Add(-time.Hour)
		require.NoError(t, NewMediaTaskRepository(integrationDB).Observe(ctx, o))
	}
	// 多个旧任务遇到 Redis 黑洞时，仅消耗兼容导入预算，不能阻止新持久任务登记中断。
	s.rdb.AddHook(durableImageSlowRedisHook{})
	started := time.Now()
	require.NoError(t, s.Maintain(ctx, time.Now()))
	require.Less(t, time.Since(started), 6*time.Second)
	stored, projected := durableImageStatuses(t, task.ID)
	require.Equal(t, "failed", stored)
	require.Equal(t, "failed", projected)
}

func TestDurableImageTaskIntegrationWithoutObservationRemainsCompatible(t *testing.T) {
	s, task, _ := durableImageFixture(t)
	ctx := context.Background()
	task.Observation = nil
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	require.NoError(t, s.Save(ctx, completeDurableImage(task), 24*time.Hour))
	got, err := s.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status)
	require.Nil(t, got.Observation)
	var projected int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM media_tasks WHERE task_id=$1`, task.ID).Scan(&projected))
	require.Zero(t, projected, "缺少安全元数据时不伪造列表信息")
}

func TestDurableImageTaskIntegrationLegacyTerminalProjectionCannotRegress(t *testing.T) {
	s, task, _ := durableImageFixture(t)
	ctx := context.Background()
	legacy := *task.Observation
	legacy.TaskID, legacy.Status, legacy.HTTPStatus = task.ID, "completed", 200
	completed := time.Now().UTC()
	legacy.CompletedAt = &completed
	require.NoError(t, NewMediaTaskRepository(integrationDB).Observe(ctx, legacy))
	// 模拟升级新实例先读到了旧 processing，而旧实例已先提交真实 completed 投影。
	task.LeaseExpiresAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, s.Save(ctx, task, 24*time.Hour))
	stored, projected := durableImageStatuses(t, task.ID)
	require.Equal(t, "processing", stored)
	require.Equal(t, "completed", projected)
	require.NoError(t, s.Maintain(ctx, time.Now()))
	stored, projected = durableImageStatuses(t, task.ID)
	require.Equal(t, "failed", stored, "暂时未知结果可记录中断，但不能覆盖已知的历史成功")
	require.Equal(t, "completed", projected)
	require.NoError(t, s.Save(ctx, completeDurableImage(task), 24*time.Hour))
	stored, projected = durableImageStatuses(t, task.ID)
	require.Equal(t, "completed", stored)
	require.Equal(t, "completed", projected)
}
