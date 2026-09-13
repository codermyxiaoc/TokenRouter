-- 团队 Key Owner 锁定、批任务额度预记和用户删除生命周期修复。
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS team_owner_disabled BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE batch_image_jobs
    ADD COLUMN IF NOT EXISTS allowance_reserved BOOLEAN NOT NULL DEFAULT FALSE;

-- 历史异常数据中若已删除用户仍是 Owner，只能解散团队，避免留下不可读取的活跃团队。
WITH orphaned_teams AS (
    SELECT DISTINCT tm.team_id
    FROM team_memberships tm
    JOIN users u ON u.id = tm.user_id
    WHERE tm.left_at IS NULL AND tm.role = 'owner' AND u.deleted_at IS NOT NULL
), dissolved AS (
    UPDATE teams t
    SET status = 'suspended', deleted_at = COALESCE(t.deleted_at, NOW()), updated_at = NOW()
    FROM orphaned_teams ot
    WHERE t.id = ot.team_id
    RETURNING t.id
)
UPDATE team_memberships tm
SET left_at = COALESCE(tm.left_at, NOW()), updated_at = NOW()
FROM dissolved d
WHERE tm.team_id = d.id AND tm.left_at IS NULL;

UPDATE api_keys k
SET status = 'disabled', updated_at = NOW()
WHERE k.team_id IN (SELECT t.id FROM teams t WHERE t.deleted_at IS NOT NULL)
  AND k.deleted_at IS NULL;

-- 已删除普通 Member 立即释放容量，并确保其团队 Key 不再可用。
UPDATE team_memberships tm
SET left_at = COALESCE(u.deleted_at, NOW()), updated_at = NOW()
FROM users u
WHERE tm.user_id = u.id
  AND tm.left_at IS NULL
  AND tm.role = 'member'
  AND u.deleted_at IS NOT NULL;

UPDATE api_keys k
SET status = 'disabled', updated_at = NOW()
FROM users u
WHERE k.user_id = u.id
  AND k.team_id IS NOT NULL
  AND k.deleted_at IS NULL
  AND u.deleted_at IS NOT NULL;

-- Owner 必须先转让或解散；普通 Member 删除时在同一事务内自动离队并禁用团队 Key。
CREATE OR REPLACE FUNCTION prevent_active_team_owner_deletion()
RETURNS TRIGGER AS $$
DECLARE
    removed_at TIMESTAMPTZ;
BEGIN
    IF EXISTS (
        SELECT 1
        FROM team_memberships tm
        JOIN teams t ON t.id = tm.team_id
        WHERE tm.user_id = OLD.id
          AND tm.left_at IS NULL
          AND tm.role = 'owner'
          AND t.deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'TEAM_OWNER_TRANSFER_REQUIRED' USING ERRCODE = '23514';
    END IF;

    removed_at := CASE WHEN TG_OP = 'DELETE' THEN NOW() ELSE COALESCE(NEW.deleted_at, NOW()) END;
    UPDATE team_memberships
    SET left_at = removed_at, updated_at = removed_at
    WHERE user_id = OLD.id AND left_at IS NULL AND role = 'member';

    UPDATE api_keys
    SET status = 'disabled', updated_at = removed_at
    WHERE user_id = OLD.id AND team_id IS NOT NULL AND deleted_at IS NULL;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS users_prevent_active_team_owner_soft_delete ON users;
CREATE TRIGGER users_prevent_active_team_owner_soft_delete
    BEFORE UPDATE OF deleted_at ON users
    FOR EACH ROW
    WHEN (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL)
    EXECUTE FUNCTION prevent_active_team_owner_deletion();

DROP TRIGGER IF EXISTS users_prevent_active_team_owner_hard_delete ON users;
CREATE TRIGGER users_prevent_active_team_owner_hard_delete
    BEFORE DELETE ON users
    FOR EACH ROW EXECUTE FUNCTION prevent_active_team_owner_deletion();
