//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// 单篇合并保存：不同文档互不覆盖；同一文档以过期起点保存返回冲突；空正文恢复默认。
func TestPatchAPIDocOverridesMergesAndDetectsConflict(t *testing.T) {
	repo := newMockSettingRepo()
	svc := NewSettingService(repo, nil)
	ctx := context.Background()

	got, err := svc.PatchAPIDocOverrides(ctx, map[string]APIDocOverridePatch{"quickstart": {Markdown: "# A"}})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"quickstart": "# A"}, got)

	// 另一名管理员改别的文档（起点为未改写），不能覆盖 quickstart。
	got, err = svc.PatchAPIDocOverrides(ctx, map[string]APIDocOverridePatch{"responses": {Markdown: "# B"}})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"quickstart": "# A", "responses": "# B"}, got)

	// 以过期起点保存同一文档：冲突且不写入。
	_, err = svc.PatchAPIDocOverrides(ctx, map[string]APIDocOverridePatch{"quickstart": {Markdown: "# stale", Base: ""}})
	require.ErrorIs(t, err, ErrAPIDocConflict)
	stored, err := svc.GetAPIDocOverrides(ctx)
	require.NoError(t, err)
	require.Equal(t, "# A", stored["quickstart"])

	// 空正文恢复默认。
	got, err = svc.PatchAPIDocOverrides(ctx, map[string]APIDocOverridePatch{"quickstart": {Markdown: "", Base: "# A"}})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"responses": "# B"}, got)

	_, err = svc.PatchAPIDocOverrides(ctx, map[string]APIDocOverridePatch{"Bad ID": {Markdown: "x"}})
	require.Error(t, err)
}

// 多名管理员并发修改不同文档，全部修改都保留。
func TestPatchAPIDocOverridesConcurrentDifferentDocuments(t *testing.T) {
	repo := newMockSettingRepo()
	svc := NewSettingService(repo, nil)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := svc.PatchAPIDocOverrides(context.Background(), map[string]APIDocOverridePatch{fmt.Sprintf("doc-%d", i): {Markdown: fmt.Sprintf("# %d", i)}})
			require.NoError(t, err)
		}(i)
	}
	wg.Wait()
	var stored map[string]string
	raw, err := repo.GetValue(context.Background(), SettingKeyAPIDocOverrides)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	require.Len(t, stored, 32)
}
