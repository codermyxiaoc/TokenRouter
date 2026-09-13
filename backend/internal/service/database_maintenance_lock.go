package service

import (
	"context"
	"database/sql"
)

const databaseHeavyMaintenanceLockKey = "maintenance:database-heavy"

var databaseHeavyMaintenanceLockID = hashAdvisoryLockID(databaseHeavyMaintenanceLockKey)

// tryAcquireDatabaseHeavyMaintenanceLock 为数据库重任务提供跨实例互斥。
// 测试或未注入数据库的精简运行模式直接放行，生产环境则使用会话级 advisory lock。
func tryAcquireDatabaseHeavyMaintenanceLock(ctx context.Context, db *sql.DB) (func(), bool, error) {
	if db == nil {
		return func() {}, true, nil
	}
	return tryAcquireDBAdvisoryLockWithError(ctx, db, databaseHeavyMaintenanceLockID)
}
