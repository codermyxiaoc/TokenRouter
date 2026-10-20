//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// cleanupObjectFixture 只登记集成测试容器内的虚拟对象，不接触真实桶或生成任务。
func cleanupObjectFixture(t *testing.T, expires time.Time) service.ImageObjectCleanupRecord {
	t.Helper()
	sum := sha256.Sum256([]byte(uuid.NewString()))
	record := service.ImageObjectCleanupRecord{
		ID: fmt.Sprintf("%x", sum), ObjectKey: "images/imgtask_" + uuid.NewString() + "-0.png",
		StorageCiphertext: "encrypted-test-config", ExpiresAt: expires.UTC().Truncate(time.Microsecond),
	}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM image_object_cleanup WHERE id=$1`, record.ID)
	})
	return record
}

func scheduleCleanupObject(t *testing.T, store service.ImageObjectCleanupStore, record service.ImageObjectCleanupRecord) time.Time {
	t.Helper()
	expiresAt, err := store.Schedule(context.Background(), record)
	require.NoError(t, err)
	return expiresAt
}

func TestImageObjectCleanupPostgresMigrationReapplyAndImmutableSchedule(t *testing.T) {
	ctx := context.Background()
	store := NewImageObjectCleanupStore(integrationDB)
	require.NoError(t, store.Check(ctx))
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := cleanupObjectFixture(t, now.Add(time.Hour))
	require.True(t, record.ExpiresAt.Equal(scheduleCleanupObject(t, store, record)))
	duplicate := record
	duplicate.ObjectKey = "backups/must-not-replace-original"
	duplicate.StorageCiphertext = "new-config-must-not-replace-original"
	duplicate.ExpiresAt = now.Add(10 * time.Hour)
	require.True(t, record.ExpiresAt.Equal(scheduleCleanupObject(t, store, duplicate)), "重试返回首次截止时间")

	migration, err := dbmigrations.FS.ReadFile("313_image_object_cleanup.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migration))
	require.NoError(t, err, "增量迁移重复执行不能改变既有清理对象")
	var key, cipher string
	var expires, next time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT object_key,storage_ciphertext,expires_at,next_attempt_at
 FROM image_object_cleanup WHERE id=$1`, record.ID).Scan(&key, &cipher, &expires, &next))
	require.Equal(t, record.ObjectKey, key)
	require.Equal(t, record.StorageCiphertext, cipher)
	require.True(t, record.ExpiresAt.Equal(expires))
	require.True(t, record.ExpiresAt.Equal(next))
	before, err := store.ClaimDue(ctx, now, 100, time.Minute)
	require.NoError(t, err)
	require.Empty(t, before, "未到期对象不能进入删除流程")
	due, err := NewImageObjectCleanupStore(integrationDB).ClaimDue(ctx, record.ExpiresAt, 100, time.Minute)
	require.NoError(t, err)
	require.Len(t, due, 1, "重建仓储后仍能领取持久队列")
	require.Equal(t, record.ID, due[0].ID)
	require.Equal(t, 1, due[0].Attempts)
	require.NotEmpty(t, due[0].LeaseToken)
	require.NoError(t, store.Complete(ctx, record.ID, due[0].LeaseToken))
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_object_cleanup WHERE id=$1`, record.ID).Scan(&count))
	require.Zero(t, count)
}

func TestImageObjectCleanupPostgresRetryHonorsBackoffAndSafeError(t *testing.T) {
	ctx := context.Background()
	store := NewImageObjectCleanupStore(integrationDB)
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := cleanupObjectFixture(t, now.Add(-time.Hour))
	scheduleCleanupObject(t, store, record)
	first, err := store.ClaimDue(ctx, now, 100, time.Minute)
	require.NoError(t, err)
	require.Len(t, first, 1)
	next := now.Add(5 * time.Minute)
	require.NoError(t, store.Retry(ctx, record.ID, first[0].LeaseToken, next,
		"SDK: https://r2.test/private.png?X-Amz-Signature=secret"))
	var code, token string
	var expires time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT last_error_code,lease_token,expires_at
 FROM image_object_cleanup WHERE id=$1`, record.ID).Scan(&code, &token, &expires))
	require.Equal(t, "cleanup_failed", code)
	require.Empty(t, token)
	require.True(t, record.ExpiresAt.Equal(expires))
	early, err := store.ClaimDue(ctx, next.Add(-time.Second), 100, time.Minute)
	require.NoError(t, err)
	require.Empty(t, early)
	second, err := store.ClaimDue(ctx, next, 100, time.Minute)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, 2, second[0].Attempts)
	require.NotEqual(t, first[0].LeaseToken, second[0].LeaseToken)
	require.NoError(t, store.Retry(ctx, record.ID, second[0].LeaseToken, next.Add(time.Hour), "cleanup_access_denied"))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT last_error_code FROM image_object_cleanup WHERE id=$1`,
		record.ID).Scan(&code))
	require.Equal(t, "cleanup_access_denied", code)
}

func TestImageObjectCleanupPostgresOldLeaseCannotRemoveOrRescheduleNewOwner(t *testing.T) {
	ctx := context.Background()
	store := NewImageObjectCleanupStore(integrationDB)
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := cleanupObjectFixture(t, now)
	scheduleCleanupObject(t, store, record)
	first, err := store.ClaimDue(ctx, now, 100, time.Second)
	require.NoError(t, err)
	require.Len(t, first, 1)
	leased, err := store.ClaimDue(ctx, now.Add(500*time.Millisecond), 100, time.Second)
	require.NoError(t, err)
	require.Empty(t, leased)
	second, err := store.ClaimDue(ctx, now.Add(2*time.Second), 100, time.Minute)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.NotEqual(t, first[0].LeaseToken, second[0].LeaseToken)
	require.ErrorIs(t, store.Complete(ctx, record.ID, first[0].LeaseToken), service.ErrImageObjectCleanupLeaseLost)
	require.ErrorIs(t, store.Retry(ctx, record.ID, first[0].LeaseToken, now.Add(time.Hour), "cleanup_delete_failed"),
		service.ErrImageObjectCleanupLeaseLost)
	var token string
	var attempts int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT lease_token,attempts FROM image_object_cleanup WHERE id=$1`,
		record.ID).Scan(&token, &attempts))
	require.Equal(t, second[0].LeaseToken, token)
	require.Equal(t, 2, attempts)
	require.NoError(t, store.Complete(ctx, record.ID, second[0].LeaseToken))
	require.ErrorIs(t, store.Complete(ctx, record.ID, second[0].LeaseToken), service.ErrImageObjectCleanupLeaseLost)
}

func TestImageObjectCleanupPostgresConcurrentClaimsAreDisjoint(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewImageObjectCleanupStore(integrationDB)
	const objectCount = 20
	for i := 0; i < objectCount; i++ {
		scheduleCleanupObject(t, store, cleanupObjectFixture(t, now))
	}
	type claimResult struct {
		records []service.ImageObjectCleanupRecord
		err     error
	}
	results := make(chan claimResult, 32)
	var workers sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			records, err := NewImageObjectCleanupStore(integrationDB).ClaimDue(ctx, now, 1, 2*time.Minute)
			results <- claimResult{records: records, err: err}
		}()
	}
	workers.Wait()
	close(results)
	seen := make(map[string]bool)
	for result := range results {
		require.NoError(t, result.err)
		for _, record := range result.records {
			require.False(t, seen[record.ID], "不同清理实例不得同时领取同一对象")
			seen[record.ID] = true
			require.Equal(t, 1, record.Attempts)
		}
	}
	require.Len(t, seen, objectCount)
}

func TestImageObjectCleanupPostgresSkipsLockedRow(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewImageObjectCleanupStore(integrationDB)
	first, second := cleanupObjectFixture(t, now), cleanupObjectFixture(t, now)
	scheduleCleanupObject(t, store, first)
	scheduleCleanupObject(t, store, second)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var id string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT id FROM image_object_cleanup WHERE id=$1 FOR UPDATE`, first.ID).Scan(&id))
	due, err := store.ClaimDue(ctx, now, 100, time.Minute)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, second.ID, due[0].ID)
	require.NoError(t, tx.Rollback())
	due, err = store.ClaimDue(ctx, now, 100, time.Minute)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, first.ID, due[0].ID)
}

func TestImageObjectCleanupPostgresConcurrentScheduleReturnsOriginalDeadline(t *testing.T) {
	ctx := context.Background()
	store := NewImageObjectCleanupStore(integrationDB)
	record := cleanupObjectFixture(t, time.Now().UTC().Add(time.Hour))
	type scheduleResult struct {
		expires time.Time
		err     error
	}
	const callers = 12
	results := make(chan scheduleResult, callers)
	var workers sync.WaitGroup
	for i := 0; i < callers; i++ {
		next := record
		next.ExpiresAt = record.ExpiresAt.Add(time.Duration(i) * time.Hour)
		next.StorageCiphertext = fmt.Sprintf("encrypted-config-%d", i)
		workers.Add(1)
		go func() {
			defer workers.Done()
			expires, err := store.Schedule(ctx, next)
			results <- scheduleResult{expires: expires, err: err}
		}()
	}
	workers.Wait()
	close(results)
	var storedExpiry time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT expires_at FROM image_object_cleanup WHERE id=$1`,
		record.ID).Scan(&storedExpiry))
	for result := range results {
		require.NoError(t, result.err)
		require.True(t, storedExpiry.Equal(result.expires), "所有重复上传必须沿用同一个持久删除期限")
	}
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_object_cleanup WHERE id=$1`,
		record.ID).Scan(&count))
	require.Equal(t, 1, count)
}
