-- 只登记新版本上传的异步图片对象；不扫描旧桶，不删除任务历史，不改动任何资金或用量记录。
CREATE TABLE IF NOT EXISTS image_object_cleanup (
    id VARCHAR(64) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{64}$'),
    object_key TEXT NOT NULL CHECK (octet_length(object_key) BETWEEN 1 AND 1024),
    -- 保存上传时的加密存储配置，后台切换桶或凭据后仍能清理原对象。
    storage_ciphertext TEXT NOT NULL CHECK (octet_length(storage_ciphertext) BETWEEN 1 AND 65536),
    expires_at TIMESTAMPTZ NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    lease_token VARCHAR(64) NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    -- 仅允许固定错误分类，不能保存 SDK 错误正文、签名 URL 或明文凭据。
    last_error_code VARCHAR(64) NOT NULL DEFAULT ''
        CHECK (last_error_code ~ '^[a-z0-9_]*$'),
    CHECK ((lease_token = '' AND lease_until IS NULL) OR (lease_token <> '' AND lease_until IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_image_object_cleanup_due
    ON image_object_cleanup(next_attempt_at, expires_at, id);
