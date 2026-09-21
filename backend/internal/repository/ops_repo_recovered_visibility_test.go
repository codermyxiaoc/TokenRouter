package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsMixedErrorListIncludesOnlyRecoveredProviderRows(t *testing.T) {
	uid, gid := int64(1), int64(3)
	filter := &service.OpsErrorLogFilter{
		IncludeRecoveredUpstream: true, View: "all", UserID: &uid, GroupID: &gid,
		StatusCodes: []int{503},
	}
	where, args := buildOpsErrorLogsWhere(filter)
	require.Contains(t, where, "COALESCE(e.status_code, 0) >= 400 OR e.error_type = 'cyber_policy' OR (e.status_code >= 200 AND e.status_code < 300 AND e.error_phase IN ('upstream', 'account_auth'))")
	require.Contains(t, where, "e.group_id = $")
	require.Contains(t, where, "e.user_id = $")
	require.Contains(t, where, "COALESCE(e.upstream_status_code, e.status_code, 0) = ANY($")
	require.Len(t, args, 3)
	// 非提供方阶段仍只查询最终失败，防止显式恢复开关放宽其它成功记录。
	filter.Phase = "request"
	where, _ = buildOpsErrorLogsWhere(filter)
	require.NotContains(t, where, "OR (e.status_code >= 200")
}

// 用户上游分类同时含网络阶段，恢复开关必须只纳入提供方成功且保留本人范围。
func TestOpsUserUpstreamCategoryIncludesRecoveredProviderRows(t *testing.T) {
	uid := int64(42)
	phases, _ := service.CategoryToFilter("upstream")
	where, args := buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{
		UserID: &uid, ErrorPhasesAny: phases, IncludeRecoveredUpstream: true, View: "all",
	})
	require.Contains(t, where, "COALESCE(e.status_code, 0) >= 400")
	require.Contains(t, where, "OR (e.status_code >= 200 AND e.status_code < 300 AND e.error_phase IN ('upstream', 'account_auth'))")
	require.Contains(t, where, "e.error_phase = ANY($")
	require.Contains(t, where, "e.user_id = $")
	require.Contains(t, args, uid)
	// 用户接口额外要求固定恢复标记，不能只凭 HTTP 200 开放原先不可见的尝试原文。
	where, _ = buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{
		UserID: &uid, ErrorPhasesAny: phases, IncludeRecoveredUpstream: true, RequireConfirmedRecovery: true, View: "all",
	})
	require.Contains(t, where, "e.error_message LIKE 'Recovered upstream error%'")
	require.Contains(t, where, "e.error_message LIKE 'Recovered account authentication failure%'")
	require.Contains(t, where, "COALESCE(e.status_code, 0) >= 400")
	// 纯提供方分类不能绕过用户的恢复确认条件。
	where, _ = buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{
		UserID: &uid, ErrorPhasesAny: []string{"upstream", "account_auth"}, IncludeRecoveredUpstream: true, RequireConfirmedRecovery: true, View: "all",
	})
	require.Contains(t, where, "e.error_message LIKE 'Recovered upstream error%'")
}

func TestOpsErrorListAndDetailPreserveRecoveredOutcome(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			checkOpsErrorListAndDetailRecoveredOutcome(t, status)
		})
	}
}

// 实际列表/详情扫描与用户查询服务共同验证 4xx 恢复不会被映射成请求阶段而丢失。
func checkOpsErrorListAndDetailRecoveredOutcome(t *testing.T, status int) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &opsRepository{db: db}
	// 列表和详情都必须同时返回原始上游状态与最终 200，防止 SQL 扫描错位或覆盖状态。
	listColumns := strings.Fields(`id created_at phase type owner source severity status platform model resolved resolved_at resolved_by resolved_by_name client_request_id request_id message user_id user_email api_key_id account_id account_name group_id group_name client_ip request_path stream inbound_endpoint upstream_endpoint requested_model upstream_model user_agent request_type api_key_name api_key_deleted client_status_code attempt_snapshot`)
	detailColumns := strings.Fields(`id created_at phase type owner source severity status platform model resolved resolved_at resolved_by client_request_id request_id message error_body upstream_status_code upstream_error_message upstream_error_detail upstream_errors is_business_limited user_id user_email api_key_id account_id account_name group_id group_name client_ip request_path stream inbound_endpoint upstream_endpoint requested_model upstream_model request_type user_agent auth_latency routing_latency upstream_latency response_latency ttft api_key_prefix api_key_name api_key_deleted client_status_code`)
	values := map[string]driver.Value{
		"id": int64(120), "created_at": time.Now(), "phase": "upstream", "type": "upstream_error",
		"owner": "provider", "source": "upstream_http", "severity": "P1", "status": int64(status),
		"platform": "openai", "model": "gpt-6-astra", "resolved": false,
		"resolved_at": nil, "resolved_by": nil, "api_key_deleted": nil, "request_type": nil,
		"client_request_id": "client-id", "request_id": "request-id", "message": fmt.Sprintf("Recovered upstream error %d: unavailable", status),
		"user_id": int64(1), "api_key_id": int64(1), "account_id": int64(22), "group_id": int64(3),
		"group_name": "test1", "stream": true, "inbound_endpoint": "/v1/responses", "upstream_endpoint": "/v1/chat/completions",
		"client_status_code": int64(200), "upstream_status_code": int64(status), "is_business_limited": false,
		"auth_latency": nil, "routing_latency": nil, "upstream_latency": nil, "response_latency": nil, "ttft": nil,
		"attempt_snapshot": `{"group_id":3,"group_name":"failed snapshot","recovered_group_id":7,"recovered_group_name":"recovered snapshot","recovered_platform":"anthropic"}`,
		"upstream_errors":  fmt.Sprintf(`[{"group_id":3,"group_name":"failed snapshot","upstream_status_code":%d,"recovered_group_id":7,"recovered_group_name":"recovered snapshot","recovered_platform":"anthropic"}]`, status),
	}
	makeRow := func(columns []string) *sqlmock.Rows {
		row := make([]driver.Value, len(columns))
		for i, column := range columns {
			value, exists := values[column]
			if !exists {
				value = ""
			}
			row[i] = value
		}
		return sqlmock.NewRows(columns).AddRow(row...)
	}
	// 该用例只验证恢复信息；没有结算记录时套餐摘要必须保持为空。
	expectNoBilling := func() {
		mock.ExpectQuery("(?s)SELECT e.id, u.id.*JOIN usage_logs u").
			WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"error_id"}))
	}
	mock.ExpectQuery("SELECT COUNT").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT.*COALESCE\\(e.status_code, 0\\).*FROM ops_error_logs").WillReturnRows(makeRow(listColumns))
	expectNoBilling()
	list, err := repo.ListErrorLogs(context.Background(), &service.OpsErrorLogFilter{IncludeRecoveredUpstream: true, View: "all"})
	require.NoError(t, err)
	require.Len(t, list.Errors, 1)
	require.Equal(t, status, list.Errors[0].StatusCode)
	require.Equal(t, 200, list.Errors[0].ClientStatusCode)
	require.True(t, list.Errors[0].RecoveredUpstream)
	require.Equal(t, int64(3), *list.Errors[0].GroupID)
	require.Equal(t, "failed snapshot", list.Errors[0].GroupName)
	require.Equal(t, int64(7), *list.Errors[0].RecoveredGroupID)
	require.Equal(t, "recovered snapshot", list.Errors[0].RecoveredGroupName)
	require.Equal(t, "anthropic", list.Errors[0].RecoveredPlatform)
	mock.ExpectQuery("(?s)SELECT.*COALESCE\\(e.status_code, 0\\).*FROM ops_error_logs").WithArgs(int64(120)).WillReturnRows(makeRow(detailColumns))
	expectNoBilling()
	detail, err := repo.GetErrorLogByID(context.Background(), 120)
	require.NoError(t, err)
	require.Equal(t, status, detail.StatusCode)
	require.Equal(t, 200, detail.ClientStatusCode)
	require.True(t, detail.RecoveredUpstream)
	require.Equal(t, list.Errors[0].RecoveredGroupID, detail.RecoveredGroupID)
	require.Equal(t, list.Errors[0].RecoveredGroupName, detail.RecoveredGroupName)
	require.Equal(t, list.Errors[0].RecoveredPlatform, detail.RecoveredPlatform)
	require.Equal(t, list.Errors[0].GroupName, detail.GroupName)
	require.Equal(t, status, *detail.UpstreamStatusCode)
	// 用户查询必须走实际仓储的本人约束与固定恢复标记过滤，DTO 不保留上游原文。
	svc := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	mock.ExpectQuery("(?s)SELECT COUNT.*Recovered upstream error%.*e.user_id =").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT.*Recovered upstream error%.*e.user_id =").WillReturnRows(makeRow(listColumns))
	expectNoBilling()
	phases, _ := service.CategoryToFilter("upstream")
	userList, err := svc.ListUserErrorRequests(context.Background(), 1, &service.OpsErrorLogFilter{
		IncludeRecoveredUpstream: true, ErrorPhasesAny: phases, StatusCodes: []int{status},
	})
	require.NoError(t, err)
	require.Len(t, userList.Items, 1)
	userRow := userList.Items[0]
	require.Equal(t, "upstream", userRow.Category)
	require.Equal(t, status, userRow.StatusCode)
	require.Equal(t, 200, userRow.ClientStatusCode)
	require.True(t, userRow.RecoveredUpstream)
	require.Equal(t, int64(7), *userRow.RecoveredGroupID)
	require.Equal(t, "recovered snapshot", userRow.RecoveredGroupName)
	mock.ExpectQuery("(?s)SELECT.*COALESCE\\(e.status_code, 0\\).*FROM ops_error_logs").WithArgs(int64(120)).WillReturnRows(makeRow(detailColumns))
	expectNoBilling()
	userDetail, err := svc.GetUserErrorRequestDetail(context.Background(), 1, 120)
	require.NoError(t, err)
	require.Equal(t, *userRow, userDetail.UserErrorRequest)
	require.Empty(t, userDetail.ErrorBody)
	raw, err := json.Marshal(userDetail)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"recovered_group_id":7`)
	require.Contains(t, string(raw), `"recovered_group_name":"recovered snapshot"`)
	require.NotContains(t, string(raw), "unavailable")
	require.NotContains(t, string(raw), "upstream_errors")
	require.NoError(t, mock.ExpectationsWereMet())
}
