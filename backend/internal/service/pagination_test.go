//go:build unit

package service

import (
	"math"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

// 极大页码不能让偏移量溢出成负数导致切片越界；本站没有上游的余额历史合并分页，只保留 API Key 用例。
func TestPaginateAPIKeysLargePage(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, 1, 2, 3, 20, 21, 1000, 1001} {
		params := pagination.PaginationParams{Page: math.MaxInt, PageSize: size}
		require.Empty(t, paginateAPIKeys([]APIKey{{ID: 1}}, params), "page_size=%d", size)
	}
}
