package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGetRequestPayloadDetailResolvesClientBillingRequestID(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	createdAt := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	completedAt := createdAt.Add(time.Second)
	// 到期快照在清理前也不可读：管理员与用户读取共用同一条带到期过滤的查询。
	mock.ExpectQuery(`(?s)FROM ops_request_details.*AND \(expires_at IS NULL OR expires_at > NOW\(\)\).*ORDER BY CASE.*LIMIT 1`).
		WithArgs("client:client-1", "client-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "client_request_id", "method", "path", "inbound_endpoint", "upstream_endpoint",
			"platform", "model", "status_code", "stream", "request_headers", "request_body",
			"response_headers", "response_body", "request_truncated", "response_truncated", "created_at", "completed_at",
			"kind", "user_id", "api_key_id", "group_id", "expires_at",
		}).AddRow(
			"internal-1", "client-1", "POST", "/v1/responses", "/v1/responses", "/v1/responses",
			"openai", "gpt-5.6-sol", 200, true, []byte(`{"content-type":["application/json"]}`), `{"model":"gpt-5.6-sol"}`,
			[]byte(`{"content-type":["text/event-stream"]}`), "data: done", false, false, createdAt, completedAt,
			"normal", int64(7), int64(11), int64(3), completedAt.Add(time.Hour),
		))

	detail, err := (&opsRepository{db: db}).GetRequestPayloadDetail(context.Background(), "client:client-1")
	require.NoError(t, err)
	require.NotNil(t, detail)
	require.Equal(t, "internal-1", detail.RequestID)
	require.Equal(t, "client-1", detail.ClientRequestID)
	// 数据缓存归属与过期时间随快照一起读出。
	require.Equal(t, service.RequestDataCacheKindNormal, detail.Kind)
	require.Equal(t, int64(7), *detail.UserID)
	require.Equal(t, int64(11), *detail.APIKeyID)
	require.Equal(t, int64(3), *detail.GroupID)
	require.Equal(t, completedAt.Add(time.Hour), *detail.ExpiresAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 统计只计入未过期的缓存，并按类别分开。
func TestCountRequestDataCacheSplitsKinds(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)COUNT\(\*\) FILTER \(WHERE kind = 'normal'\).*expires_at IS NULL OR expires_at > \$1`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"normal", "error"}).AddRow(int64(4), int64(9)))
	stats, err := (&opsRepository{db: db}).CountRequestDataCache(context.Background(), now)
	require.NoError(t, err)
	require.Equal(t, int64(4), stats.Normal)
	require.Equal(t, int64(9), stats.Error)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 按类别清除分批删除，直到某批不足批大小；清除全部直接 TRUNCATE。
func TestDeleteRequestDataCacheBatchesByKind(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectExec(`(?s)WHERE kind = 'normal' LIMIT \$1.*DELETE FROM ops_request_details`).
		WithArgs(requestDataCacheDeleteBatch).WillReturnResult(sqlmock.NewResult(0, requestDataCacheDeleteBatch))
	mock.ExpectExec(`(?s)WHERE kind = 'normal' LIMIT \$1.*DELETE FROM ops_request_details`).
		WithArgs(requestDataCacheDeleteBatch).WillReturnResult(sqlmock.NewResult(0, 2))
	deleted, err := (&opsRepository{db: db}).DeleteRequestDataCache(context.Background(), service.RequestDataCacheKindNormal)
	require.NoError(t, err)
	require.Equal(t, int64(requestDataCacheDeleteBatch+2), deleted)

	mock.ExpectExec(`(?s)WHERE kind <> 'normal' LIMIT \$1`).
		WithArgs(requestDataCacheDeleteBatch).WillReturnResult(sqlmock.NewResult(0, 0))
	_, err = (&opsRepository{db: db}).DeleteRequestDataCache(context.Background(), service.RequestDataCacheKindError)
	require.NoError(t, err)
	// 清除全部改为 TRUNCATE：先读估算行数，再整表清空，不走分批 DELETE。
	mock.ExpectQuery(`(?s)reltuples.*ops_request_details`).
		WillReturnRows(sqlmock.NewRows([]string{"estimated"}).AddRow(int64(42)))
	mock.ExpectExec(`^TRUNCATE TABLE ops_request_details$`).WillReturnResult(sqlmock.NewResult(0, 0))
	all, err := (&opsRepository{db: db}).DeleteRequestDataCache(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, int64(42), all)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListRequestDetailsSLAOnlyUsesSLAErrorScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New error = %v", err)
	}
	defer func() { _ = db.Close() }()

	repo := &opsRepository{db: db}
	start := time.Date(2026, 6, 18, 1, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	// SLA 详情里的错误明细要排除配置的客户端侧状态码，避免和 SLA 卡片数字不一致。
	slaScopePattern := regexp.QuoteMeta("COALESCE(o.status_code, 0) IN (401, 403)") +
		`(?s).*` +
		regexp.QuoteMeta("o.upstream_status_code IS NOT NULL")

	mock.ExpectQuery(`(?s).*WITH combined AS.*`+slaScopePattern+`.*SELECT COUNT\(1\) FROM combined.*`).
		WithArgs(start, end, "error").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))

	mock.ExpectQuery(`(?s).*WITH combined AS.*`+slaScopePattern+`.*FROM combined.*LIMIT \$4 OFFSET \$5.*`).
		WithArgs(start, end, "error", 10, 0).
		WillReturnRows(emptyRequestDetailRows())

	items, total, err := repo.ListRequestDetails(context.Background(), &service.OpsRequestDetailFilter{
		StartTime: &start,
		EndTime:   &end,
		Kind:      string(service.OpsRequestKindError),
		SLAOnly:   true,
		IgnoredStatusCodes: []int{
			401,
			403,
		},
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListRequestDetails error = %v", err)
	}
	if total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
	if len(items) != 0 {
		t.Fatalf("items len = %d, want 0", len(items))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func emptyRequestDetailRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"kind",
		"created_at",
		"request_id",
		"platform",
		"model",
		"duration_ms",
		"first_token_ms",
		"status_code",
		"error_id",
		"phase",
		"severity",
		"message",
		"user_id",
		"api_key_id",
		"account_id",
		"group_id",
		"stream",
	})
}
