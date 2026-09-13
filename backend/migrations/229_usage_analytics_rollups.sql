-- 用户用量多维预聚合表。迁移阶段只创建空表，历史数据由后台限流任务回填。
CREATE TABLE IF NOT EXISTS usage_analytics_hourly (
    bucket_start TIMESTAMPTZ NOT NULL,
    user_id BIGINT NOT NULL,
    billing_user_id BIGINT NOT NULL,
    team_id BIGINT NOT NULL DEFAULT 0,
    api_key_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL DEFAULT 0,
    requested_model VARCHAR(100) NOT NULL DEFAULT '',
    request_type SMALLINT NOT NULL DEFAULT 0,
    stream BOOLEAN NOT NULL DEFAULT FALSE,
    billing_type SMALLINT NOT NULL DEFAULT 0,
    billing_mode VARCHAR(20) NOT NULL DEFAULT '',
    platform VARCHAR(64) NOT NULL DEFAULT '',
    inbound_endpoint VARCHAR(128) NOT NULL DEFAULT '',
    total_requests BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    total_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    actual_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    total_duration_ms BIGINT NOT NULL DEFAULT 0,
    duration_count BIGINT NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (
        bucket_start, user_id, billing_user_id, team_id, api_key_id, group_id,
        requested_model, request_type, stream, billing_type, billing_mode,
        platform, inbound_endpoint
    )
);

CREATE INDEX IF NOT EXISTS idx_usage_analytics_hourly_user_bucket
    ON usage_analytics_hourly (user_id, bucket_start);
CREATE INDEX IF NOT EXISTS idx_usage_analytics_hourly_billing_user_bucket
    ON usage_analytics_hourly (billing_user_id, bucket_start);
CREATE INDEX IF NOT EXISTS idx_usage_analytics_hourly_team_bucket
    ON usage_analytics_hourly (team_id, bucket_start) WHERE team_id <> 0;
CREATE INDEX IF NOT EXISTS idx_usage_analytics_hourly_api_key_bucket
    ON usage_analytics_hourly (api_key_id, bucket_start);
CREATE INDEX IF NOT EXISTS idx_usage_analytics_hourly_group_bucket
    ON usage_analytics_hourly (group_id, bucket_start) WHERE group_id <> 0;

COMMENT ON TABLE usage_analytics_hourly IS '用户用量多维小时预聚合，桶边界使用 UTC 时间';

-- 日表由小时表汇总，避免再次扫描大型原始记录表。
CREATE TABLE IF NOT EXISTS usage_analytics_daily (
    bucket_date DATE NOT NULL,
    user_id BIGINT NOT NULL,
    billing_user_id BIGINT NOT NULL,
    team_id BIGINT NOT NULL DEFAULT 0,
    api_key_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL DEFAULT 0,
    requested_model VARCHAR(100) NOT NULL DEFAULT '',
    request_type SMALLINT NOT NULL DEFAULT 0,
    stream BOOLEAN NOT NULL DEFAULT FALSE,
    billing_type SMALLINT NOT NULL DEFAULT 0,
    billing_mode VARCHAR(20) NOT NULL DEFAULT '',
    platform VARCHAR(64) NOT NULL DEFAULT '',
    inbound_endpoint VARCHAR(128) NOT NULL DEFAULT '',
    total_requests BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    total_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    actual_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    total_duration_ms BIGINT NOT NULL DEFAULT 0,
    duration_count BIGINT NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (
        bucket_date, user_id, billing_user_id, team_id, api_key_id, group_id,
        requested_model, request_type, stream, billing_type, billing_mode,
        platform, inbound_endpoint
    )
);

CREATE INDEX IF NOT EXISTS idx_usage_analytics_daily_user_bucket
    ON usage_analytics_daily (user_id, bucket_date);
CREATE INDEX IF NOT EXISTS idx_usage_analytics_daily_billing_user_bucket
    ON usage_analytics_daily (billing_user_id, bucket_date);
CREATE INDEX IF NOT EXISTS idx_usage_analytics_daily_team_bucket
    ON usage_analytics_daily (team_id, bucket_date) WHERE team_id <> 0;
CREATE INDEX IF NOT EXISTS idx_usage_analytics_daily_api_key_bucket
    ON usage_analytics_daily (api_key_id, bucket_date);
CREATE INDEX IF NOT EXISTS idx_usage_analytics_daily_group_bucket
    ON usage_analytics_daily (group_id, bucket_date) WHERE group_id <> 0;

COMMENT ON TABLE usage_analytics_daily IS '用户用量多维日预聚合，日期边界使用 UTC';

-- 单行状态同时记录实时水位和从新到旧的历史覆盖游标。
CREATE TABLE IF NOT EXISTS usage_analytics_aggregation_state (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    live_watermark TIMESTAMPTZ NOT NULL DEFAULT TIMESTAMPTZ '1970-01-01 00:00:00+00',
    coverage_start TIMESTAMPTZ,
    backfill_cursor TIMESTAMPTZ,
    source_oldest_at TIMESTAMPTZ,
    phase VARCHAR(24) NOT NULL DEFAULT 'idle',
    last_run_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_error_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    last_duration_ms BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO usage_analytics_aggregation_state (id)
VALUES (1)
ON CONFLICT (id) DO NOTHING;

COMMENT ON TABLE usage_analytics_aggregation_state IS '用户用量预聚合运行状态与历史覆盖游标';

-- 旧运维聚合字段不再参与运行时配置；异常 JSON 不阻塞迁移。
DO $$
BEGIN
    UPDATE settings
    SET value = CASE
            WHEN jsonb_typeof(value::jsonb) = 'object' THEN (value::jsonb - 'aggregation')::text
            ELSE value
        END,
        updated_at = NOW()
    WHERE key = 'ops_advanced_settings';
EXCEPTION WHEN invalid_text_representation THEN
    RAISE NOTICE '跳过无法解析的 ops_advanced_settings 聚合字段清理';
END $$;

DELETE FROM settings WHERE key = 'ops_query_mode_default';
