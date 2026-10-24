package repository

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/lib/pq"
)

// 原账号写事务内推进代次；单独更新代次不会触发317的配置变化触发器。
func advanceChanshuiManualAccountRevision(ctx context.Context, exec sqlExecutor, ids []int64) error {
	if !service.HasChanshuiManualAccountState(ctx) || len(ids) == 0 {
		return nil
	}
	_, err := exec.ExecContext(ctx, `UPDATE accounts SET chanshui_config_revision=chanshui_config_revision+1 WHERE id=ANY($1) AND deleted_at IS NULL`, pq.Array(ids))
	return err
}
