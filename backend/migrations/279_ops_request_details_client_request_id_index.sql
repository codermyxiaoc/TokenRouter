-- 请求详情支持按使用记录中的 client:<request_id> 别名查询，补充客户端请求 ID 索引。
CREATE INDEX IF NOT EXISTS idx_ops_request_details_client_request_id
    ON ops_request_details (client_request_id);
