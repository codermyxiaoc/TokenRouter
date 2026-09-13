ALTER TABLE api_keys
  ADD COLUMN IF NOT EXISTS fallback_to_default_group_when_unavailable BOOLEAN NOT NULL DEFAULT TRUE;

COMMENT ON COLUMN api_keys.fallback_to_default_group_when_unavailable IS '绑定分组不可用时自动回退到同平台默认分组';
