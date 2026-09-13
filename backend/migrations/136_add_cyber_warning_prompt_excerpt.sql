-- 为已部署的 Cyber 警告表补充用户提示词摘要，便于管理员回溯触发原因。
ALTER TABLE content_moderation_cyber_warnings
    ADD COLUMN IF NOT EXISTS prompt_excerpt TEXT NOT NULL DEFAULT '';
