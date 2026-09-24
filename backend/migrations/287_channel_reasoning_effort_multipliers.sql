-- 扩展推理档位倍率，保留旧 Max 列及其模型默认规则；不回写任何存量有效价格。
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS reasoning_effort_multipliers JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN channel_model_pricing.reasoning_effort_multipliers IS
    '最终推理档位的 token 倍率；缺省档位沿用旧 Max 配置及模型默认，其余为 1x';

-- 分组价卡已有 JSON 存储，旧字段继续可读；账号统计价维持独立最终成本口径。
