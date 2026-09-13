package repository

import (
	"context"
	"database/sql/driver"
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

func TestOpsErrorListAndDetailPreserveRecoveredOutcome(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &opsRepository{db: db}
	// 列表和详情都必须同时返回上游 503 与最终 200，防止 SQL 扫描错位或覆盖状态。
	listColumns := strings.Fields(`id created_at phase type owner source severity status platform model resolved resolved_at resolved_by resolved_by_name client_request_id request_id message user_id user_email api_key_id account_id account_name group_id group_name client_ip request_path stream inbound_endpoint upstream_endpoint requested_model upstream_model user_agent request_type api_key_name api_key_deleted client_status_code`)
	detailColumns := strings.Fields(`id created_at phase type owner source severity status platform model resolved resolved_at resolved_by client_request_id request_id message error_body upstream_status_code upstream_error_message upstream_error_detail upstream_errors is_business_limited user_id user_email api_key_id account_id account_name group_id group_name client_ip request_path stream inbound_endpoint upstream_endpoint requested_model upstream_model request_type user_agent auth_latency routing_latency upstream_latency response_latency ttft api_key_prefix api_key_name api_key_deleted client_status_code`)
	values := map[string]driver.Value{
		"id": int64(120), "created_at": time.Now(), "phase": "upstream", "type": "upstream_error",
		"owner": "provider", "source": "upstream_http", "severity": "P1", "status": int64(503),
		"platform": "openai", "model": "gpt-6-astra", "resolved": false,
		"resolved_at": nil, "resolved_by": nil, "api_key_deleted": nil, "request_type": nil,
		"client_request_id": "client-id", "request_id": "request-id", "message": "Recovered upstream error 503: unavailable",
		"user_id": int64(1), "api_key_id": int64(1), "account_id": int64(22), "group_id": int64(3),
		"group_name": "test1", "stream": true, "inbound_endpoint": "/v1/responses", "upstream_endpoint": "/v1/chat/completions",
		"client_status_code": int64(200), "upstream_status_code": int64(503), "is_business_limited": false,
		"auth_latency": nil, "routing_latency": nil, "upstream_latency": nil, "response_latency": nil, "ttft": nil,
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
	mock.ExpectQuery("SELECT COUNT").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT.*COALESCE\\(e.status_code, 0\\).*FROM ops_error_logs").WillReturnRows(makeRow(listColumns))
	list, err := repo.ListErrorLogs(context.Background(), &service.OpsErrorLogFilter{IncludeRecoveredUpstream: true, View: "all"})
	require.NoError(t, err)
	require.Len(t, list.Errors, 1)
	require.Equal(t, 503, list.Errors[0].StatusCode)
	require.Equal(t, 200, list.Errors[0].ClientStatusCode)
	require.True(t, list.Errors[0].RecoveredUpstream)
	mock.ExpectQuery("(?s)SELECT.*COALESCE\\(e.status_code, 0\\).*FROM ops_error_logs").WithArgs(int64(120)).WillReturnRows(makeRow(detailColumns))
	detail, err := repo.GetErrorLogByID(context.Background(), 120)
	require.NoError(t, err)
	require.Equal(t, 503, detail.StatusCode)
	require.Equal(t, 200, detail.ClientStatusCode)
	require.True(t, detail.RecoveredUpstream)
	require.Equal(t, 503, *detail.UpstreamStatusCode)
	require.NoError(t, mock.ExpectationsWereMet())
}
