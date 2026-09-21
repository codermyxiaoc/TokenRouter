package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

const upsertOpsRequestPayloadSQL = `
INSERT INTO ops_request_details (
  request_id, client_request_id, method, path, inbound_endpoint, upstream_endpoint,
  platform, model, status_code, stream, request_headers, request_body,
  response_headers, response_body, request_truncated, response_truncated,
  created_at, completed_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13::jsonb,$14,$15,$16,$17,$18)
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
  completed_at = EXCLUDED.completed_at`

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
	)
	return err
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
	var requestBody, responseBody sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT request_id, client_request_id, method, path, inbound_endpoint, upstream_endpoint,
       platform, model, status_code, stream, request_headers, request_body,
       response_headers, response_body, request_truncated, response_truncated,
       created_at, completed_at
FROM ops_request_details
WHERE request_id = $1
   OR client_request_id = $1
   OR request_id = $2
   OR client_request_id = $2
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
