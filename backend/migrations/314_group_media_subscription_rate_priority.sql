-- 图片与视频独立倍率分别允许套餐分组倍率优先；默认关闭，保留存量分组及旧任务的计费语义。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS image_subscription_rate_priority BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS video_subscription_rate_priority BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.image_subscription_rate_priority IS '图片独立倍率下优先采用已配置的套餐分组倍率，缺省关闭';
COMMENT ON COLUMN groups.video_subscription_rate_priority IS '视频独立倍率下优先采用已配置的套餐分组倍率，缺省关闭';

-- 新开关更新后持久失效普通及复合 Key 快照，避免其它实例继续使用旧计费选择。
CREATE OR REPLACE FUNCTION enqueue_group_auth_cache_invalidation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    target_group_id BIGINT;
BEGIN
    target_group_id := OLD.id;
    IF TG_OP = 'UPDATE'
       AND OLD.status IS NOT DISTINCT FROM NEW.status
       AND OLD.is_exclusive IS NOT DISTINCT FROM NEW.is_exclusive
       AND OLD.allow_image_generation IS NOT DISTINCT FROM NEW.allow_image_generation
       AND OLD.image_subscription_rate_priority IS NOT DISTINCT FROM NEW.image_subscription_rate_priority
       AND OLD.video_subscription_rate_priority IS NOT DISTINCT FROM NEW.video_subscription_rate_priority
       AND OLD.deleted_at IS NOT DISTINCT FROM NEW.deleted_at THEN
        RETURN NEW;
    END IF;

    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT encode(sha256(convert_to(k.key, 'UTF8')), 'hex')
    FROM api_keys AS k
    WHERE k.deleted_at IS NULL
      AND k.key <> ''
      AND (k.group_id = target_group_id OR EXISTS (
          SELECT 1 FROM api_key_composite_groups AS mapping
          WHERE mapping.api_key_id = k.id AND mapping.group_id = target_group_id
      ));
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
