//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 验证新迁移后旧记录仍可读取，两个引擎的元数据经真实数据库写入和详情读取不丢失。
func TestContentModerationEngineMetadataPersistence(t *testing.T) {
	ctx := context.Background()
	repo := &contentModerationRepository{db: integrationDB}
	for _, meta := range []*service.ContentModerationEngineMeta{nil, {Engine: "openai", Model: "omni-test"}, {Engine: "typesafe", Model: "jev-test", RulesVersion: service.TypeSafeModerationRulesVersion, SkippedImages: 2}} {
		log := &service.ContentModerationLog{RequestID: fmt.Sprintf("engine-meta-%d", time.Now().UnixNano()), Mode: "observe", Action: "allow", Source: "user", EngineMeta: meta}
		require.NoError(t, repo.CreateLog(ctx, log))
		t.Cleanup(func() {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM content_moderation_logs WHERE id=$1", log.ID)
		})
		saved, err := repo.GetLog(ctx, log.ID)
		require.NoError(t, err)
		require.Equal(t, meta, saved.EngineMeta)
		items, _, err := repo.ListLogs(ctx, service.ContentModerationLogFilter{Search: log.RequestID})
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.Equal(t, meta, items[0].EngineMeta)
	}
}
