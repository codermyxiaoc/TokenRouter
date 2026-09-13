-- 为 TLS 路由器增加 ChatGPT OAuth token 请求专用 UA 与 TLS 模板配置。

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE tls_fingerprint_routers
    ADD COLUMN IF NOT EXISTS chatgpt_oauth_token_user_agent VARCHAR(512) NOT NULL DEFAULT '';

ALTER TABLE tls_fingerprint_routers
    ADD COLUMN IF NOT EXISTS chatgpt_oauth_token_tls_fingerprint_profile_id BIGINT;

COMMENT ON COLUMN tls_fingerprint_routers.chatgpt_oauth_token_user_agent IS
    'ChatGPT OAuth token exchange/refresh 请求使用的 User-Agent，空值使用系统兜底';

COMMENT ON COLUMN tls_fingerprint_routers.chatgpt_oauth_token_tls_fingerprint_profile_id IS
    'ChatGPT OAuth token exchange/refresh 请求使用的 TLS 模板：NULL=不启用，0=内置默认，-1=随机，正数=指定模板';
