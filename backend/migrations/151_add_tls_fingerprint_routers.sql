-- 创建 TLS 指纹路由器，用于按入站 User-Agent 选择 TLS 指纹模板。

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS tls_fingerprint_routers (
    id          BIGSERIAL    PRIMARY KEY,
    name        VARCHAR(100) NOT NULL UNIQUE,
    description TEXT,
    enabled     BOOLEAN      NOT NULL DEFAULT true,
    rules       JSONB        NOT NULL DEFAULT '[]'::jsonb,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tls_fingerprint_routers_enabled ON tls_fingerprint_routers (enabled);

COMMENT ON TABLE tls_fingerprint_routers IS 'TLS 指纹路由器，用于按入站 User-Agent 选择 TLS 指纹模板';
COMMENT ON COLUMN tls_fingerprint_routers.rules IS '有序 User-Agent 匹配规则，第一条命中规则决定使用的 TLS 指纹模板';
