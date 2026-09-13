package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// recoveredOpsFilterCapture 只捕获查询条件，不连接真实数据库或更改监控设置。
type recoveredOpsFilterCapture struct {
	service.OpsRepository
	filter *service.OpsErrorLogFilter
}

func (r *recoveredOpsFilterCapture) ListErrorLogs(_ context.Context, filter *service.OpsErrorLogFilter) (*service.OpsErrorLogList, error) {
	r.filter = filter
	return &service.OpsErrorLogList{Errors: []*service.OpsErrorLog{}, Page: 1, PageSize: 20}, nil
}

func TestAdminOpsRecoveredVisibilityIsExplicitAndScoped(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		status     int
		include    bool
	}{
		{"usage_includes_recovered", "/errors?include_recovered_upstream=true&view=all&status_codes=503", http.StatusOK, true},
		{"default_excludes_recovered", "/errors?view=all", http.StatusOK, false},
		{"explicit_false", "/errors?include_recovered_upstream=false", http.StatusOK, false},
		{"invalid_flag", "/errors?include_recovered_upstream=invalid", http.StatusBadRequest, false},
		{"request_errors_remain_failures", "/request-errors?include_recovered_upstream=true", http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &recoveredOpsFilterCapture{}
			svc := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			h := NewOpsHandler(svc)
			router := gin.New()
			router.GET("/errors", h.GetErrorLogs)
			router.GET("/request-errors", h.ListRequestErrors)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			require.Equal(t, tc.status, rec.Code)
			if tc.status != http.StatusOK {
				require.Nil(t, repo.filter)
				return
			}
			require.NotNil(t, repo.filter)
			require.Equal(t, tc.include, repo.filter.IncludeRecoveredUpstream)
			if tc.include {
				require.Equal(t, "all", repo.filter.View)
				require.Equal(t, []int{503}, repo.filter.StatusCodes)
			}
		})
	}
}
