package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

const upsertOpsRequestPayloadSQL = `
INSERT INTO ops_request_details (
  request_id, client_request_id, method, path, inbound_endpoint, upstream_endpoint,
  platform, model, status_code, stream, request_headers, request_body,
  response_headers, response_body, request_truncated, response_truncated,
  created_at, completed_at, kind, user_id, api_key_id, group_id, expires_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13::jsonb,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
ON CONFLICT (request_id) DO UPDATE SET
  client_request_id = EXCLUDED.client_request_id,
  method = EXCLUDED.method,
  path = EXCLUDED.path,
  inbound_endpoint = EXCLUDED.inbound_endpoint,
  upstream_endpoint = EXCLUDED.upstream_endpoint,
  platform = EXCLUDED.platform,
  model = EXCLUDED.model,
  status_code = EXCLUDED.status_code,
  stream = EXCLUDED.stream,
  request_headers = EXCLUDED.request_headers,
  request_body = EXCLUDED.request_body,
  response_headers = EXCLUDED.response_headers,
  response_body = EXCLUDED.response_body,
  request_truncated = EXCLUDED.request_truncated,
  response_truncated = EXCLUDED.response_truncated,
  created_at = EXCLUDED.created_at,
  completed_at = EXCLUDED.completed_at,
  kind = EXCLUDED.kind,
  user_id = EXCLUDED.user_id,
  api_key_id = EXCLUDED.api_key_id,
  group_id = EXCLUDED.group_id,
  expires_at = EXCLUDED.expires_at`

func (r *opsRepository) UpsertRequestPayloadDetail(ctx context.Context, detail *service.OpsRequestPayloadDetail) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil ops repository")
	}
	if detail == nil || strings.TrimSpace(detail.RequestID) == "" {
		return fmt.Errorf("invalid request detail")
	}
	_, err := r.db.ExecContext(ctx, upsertOpsRequestPayloadSQL,
		detail.RequestID,
		nullableString(detail.ClientRequestID),
		nullableString(detail.Method),
		nullableString(detail.Path),
		nullableString(detail.InboundEndpoint),
		nullableString(detail.UpstreamEndpoint),
		nullableString(detail.Platform),
		nullableString(detail.Model),
		detail.StatusCode,
		detail.Stream,
		jsonOrEmptyObject(detail.RequestHeaders),
		nullableString(detail.RequestBody),
		jsonOrEmptyObject(detail.ResponseHeaders),
		nullableString(detail.ResponseBody),
		detail.RequestTruncated,
		detail.ResponseTruncated,
		detail.CreatedAt,
		detail.CompletedAt,
		requestPayloadKind(detail.Kind),
		detail.UserID,
		detail.APIKeyID,
		detail.GroupID,
		detail.ExpiresAt,
	)
	return err
}

// requestPayloadKind 把空类别归为错误请求，与迁移默认值一致。
func requestPayloadKind(kind string) string {
	if kind == service.RequestDataCacheKindNormal {
		return kind
	}
	return service.RequestDataCacheKindError
}

func (r *opsRepository) GetRequestPayloadDetail(ctx context.Context, requestID string) (*service.OpsRequestPayloadDetail, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	requestID = strings.TrimSpace(requestID)
	// 使用记录的 request_id 可能是计费幂等键 client:<客户端请求 ID>，
	// 而请求详情表保存的是内部 request_id，同时单独保存原始 client_request_id。
	// 查询时同时匹配两种形态，兼容成功使用记录、错误记录和历史数据。
	clientRequestAlias := strings.TrimPrefix(requestID, "client:")
	var detail service.OpsRequestPayloadDetail
	var clientRequestID, method, path, inbound, upstream, platform, model sql.NullString
	var requestHeaders, responseHeaders []byte
	var requestBody, responseBody, kind sql.NullString
	var userID, apiKeyID, groupID sql.NullInt64
	var expiresAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
SELECT request_id, client_request_id, method, path, inbound_endpoint, upstream_endpoint,
       platform, model, status_code, stream, request_headers, request_body,
       response_headers, response_body, request_truncated, response_truncated,
       created_at, completed_at, kind, user_id, api_key_id, group_id, expires_at
FROM ops_request_details
WHERE (request_id = $1
   OR client_request_id = $1
   OR request_id = $2
   OR client_request_id = $2)
  -- 到期快照在清理任务删除前同样不可读，管理员与用户入口口径一致。
  AND (expires_at IS NULL OR expires_at > NOW())
ORDER BY CASE
  WHEN request_id = $1 THEN 0
  WHEN client_request_id = $1 THEN 1
  WHEN request_id = $2 THEN 2
  ELSE 3
END
LIMIT 1`, requestID, clientRequestAlias).Scan(
		&detail.RequestID, &clientRequestID, &method, &path, &inbound, &upstream,
		&platform, &model, &detail.StatusCode, &detail.Stream, &requestHeaders,
		&requestBody, &responseHeaders, &responseBody, &detail.RequestTruncated,
		&detail.ResponseTruncated, &detail.CreatedAt, &detail.CompletedAt,
		&kind, &userID, &apiKeyID, &groupID, &expiresAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	detail.ClientRequestID = clientRequestID.String
	detail.Method = method.String
	detail.Path = path.String
	detail.InboundEndpoint = inbound.String
	detail.UpstreamEndpoint = upstream.String
	detail.Platform = platform.String
	detail.Model = model.String
	detail.RequestBody = requestBody.String
	detail.ResponseBody = responseBody.String
	detail.Kind = requestPayloadKind(kind.String)
	detail.UserID = sqlNullInt64Ptr(userID)
	detail.APIKeyID = sqlNullInt64Ptr(apiKeyID)
	detail.GroupID = sqlNullInt64Ptr(groupID)
	if expiresAt.Valid {
		t := expiresAt.Time
		detail.ExpiresAt = &t
	}
	if len(requestHeaders) > 0 {
		detail.RequestHeaders = string(requestHeaders)
	}
	if len(responseHeaders) > 0 {
		detail.ResponseHeaders = string(responseHeaders)
	}
	return &detail, nil
}

func jsonOrEmptyObject(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return `{}`
	}
	var raw json.RawMessage
	if json.Unmarshal([]byte(value), &raw) != nil || len(raw) == 0 {
		return `{}`
	}
	return string(raw)
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// sqlNullInt64Ptr 把可空整数列转换为指针。
func sqlNullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
}

// requestDataCacheDeleteBatch 是手动清除与过期清理的单批删除行数，避免一次性长事务锁表。
const requestDataCacheDeleteBatch = 5000

// CountRequestDataCache 统计两类缓存中仍在有效期内的条数。
func (r *opsRepository) CountRequestDataCache(ctx context.Context, now time.Time) (*service.RequestDataCacheStats, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	stats := &service.RequestDataCacheStats{}
	err := r.db.QueryRowContext(ctx, `
SELECT
  COUNT(*) FILTER (WHERE kind = 'normal'),
  COUNT(*) FILTER (WHERE kind <> 'normal')
FROM ops_request_details
WHERE expires_at IS NULL OR expires_at > $1`, now).Scan(&stats.Normal, &stats.Error)
	if err != nil {
		return nil, err
	}
	return stats, nil
}

// DeleteRequestDataCache 分批删除指定类别的缓存；kind 为空时删除全部。
func (r *opsRepository) DeleteRequestDataCache(ctx context.Context, kind string) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil ops repository")
	}
	if kind == "" {
		return r.truncateRequestDetails(ctx)
	}
	where := ""
	switch kind {
	case service.RequestDataCacheKindNormal:
		where = "kind = 'normal'"
	default:
		where = "kind <> 'normal'"
	}
	return r.deleteRequestDetailsInBatches(ctx, where)
}

// truncateRequestDetails 清除全部缓存：直接 TRUNCATE，立即把磁盘空间还给系统，避免 DELETE 留下大量死元组。
// TRUNCATE 不返回行数，返回值取自表统计信息的估算行数（从未 ANALYZE 时为 0）。
func (r *opsRepository) truncateRequestDetails(ctx context.Context) (int64, error) {
	var estimated int64
	if err := r.db.QueryRowContext(ctx, `
SELECT COALESCE((
  SELECT GREATEST(reltuples, 0)::bigint FROM pg_class WHERE oid = to_regclass('public.ops_request_details')
), 0)`).Scan(&estimated); err != nil {
		return 0, fmt.Errorf("estimate ops_request_details rows: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `TRUNCATE TABLE ops_request_details`); err != nil {
		return 0, fmt.Errorf("truncate ops_request_details: %w", err)
	}
	return estimated, nil
}

// deleteRequestDetailsInBatches 按 ctid 分批删除；where 只能是内部常量条件，$1 固定为批大小。
func (r *opsRepository) deleteRequestDetailsInBatches(ctx context.Context, where string) (int64, error) {
	q := `
WITH batch AS MATERIALIZED (
  SELECT ctid AS row_ctid FROM ops_request_details WHERE ` + where + ` LIMIT $1
)
DELETE FROM ops_request_details AS target
USING batch
WHERE target.ctid = batch.row_ctid`
	var total int64
	for {
		res, err := r.db.ExecContext(ctx, q, requestDataCacheDeleteBatch)
		if err != nil {
			return total, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += affected
		if affected < requestDataCacheDeleteBatch {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}
