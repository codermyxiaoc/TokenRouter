-- 异步图片创建支持 Idempotency-Key：同一 API Key 下同一幂等键只对应一个未过期任务，
-- 客户端超时重试取回原任务，不重新生成或扣费。过期任务的幂等键由仓储在复用前清空。
ALTER TABLE image_tasks ADD COLUMN IF NOT EXISTS idempotency_key VARCHAR(128);
CREATE UNIQUE INDEX IF NOT EXISTS idx_image_tasks_idempotency
    ON image_tasks(api_key_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
