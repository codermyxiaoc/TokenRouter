-- 用户禁用公开分组表。
-- 公开分组默认可用；该表只记录管理员为某个用户显式禁止的公开分组。

CREATE TABLE IF NOT EXISTS user_disabled_public_groups (
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id    BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_user_disabled_public_groups_group_id
    ON user_disabled_public_groups(group_id);

COMMENT ON TABLE user_disabled_public_groups IS '用户禁用公开分组配置';
COMMENT ON COLUMN user_disabled_public_groups.user_id IS '用户ID';
COMMENT ON COLUMN user_disabled_public_groups.group_id IS '被禁止使用的公开分组ID';
