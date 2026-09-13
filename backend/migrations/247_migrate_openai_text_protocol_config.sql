-- 将 OpenAI APIKey 账号的文本能力、协议路由和探测事实迁移到彼此独立的字段。
CREATE OR REPLACE FUNCTION migrate_openai_workload_capabilities_247(raw_value JSONB)
RETURNS JSONB
LANGUAGE SQL
IMMUTABLE
AS $$
    SELECT CASE
        WHEN raw_value IS NULL OR raw_value = 'null'::jsonb
            THEN '["text_generation", "embeddings"]'::jsonb
        WHEN jsonb_typeof(raw_value) NOT IN ('array', 'object')
            THEN '[]'::jsonb
        ELSE COALESCE(
            (
                SELECT jsonb_agg(capability ORDER BY ordinal)
                FROM (
                    VALUES
                        (
                            'text_generation'::text,
                            1,
                            CASE jsonb_typeof(raw_value)
                                WHEN 'array' THEN raw_value ? 'text_generation' OR raw_value ? 'chat_completions'
                                WHEN 'object' THEN raw_value -> 'text_generation' = 'true'::jsonb
                                    OR raw_value -> 'chat_completions' = 'true'::jsonb
                                ELSE FALSE
                            END
                        ),
                        (
                            'embeddings'::text,
                            2,
                            CASE jsonb_typeof(raw_value)
                                WHEN 'array' THEN raw_value ? 'embeddings'
                                WHEN 'object' THEN raw_value -> 'embeddings' = 'true'::jsonb
                                ELSE FALSE
                            END
                        )
                ) AS capabilities(capability, ordinal, enabled)
                WHERE enabled
            ),
            '[]'::jsonb
        )
    END;
$$;

UPDATE accounts
SET credentials = (COALESCE(credentials, '{}'::jsonb) - 'openai_capabilities')
        || jsonb_build_object(
            'openai_workload_capabilities',
            CASE
                WHEN COALESCE(credentials, '{}'::jsonb) ? 'openai_workload_capabilities'
                    THEN migrate_openai_workload_capabilities_247(credentials -> 'openai_workload_capabilities')
                WHEN COALESCE(credentials, '{}'::jsonb) ? 'openai_capabilities'
                    THEN migrate_openai_workload_capabilities_247(credentials -> 'openai_capabilities')
                ELSE '["text_generation", "embeddings"]'::jsonb
            END
        ),
    extra = (COALESCE(extra, '{}'::jsonb) - 'openai_responses_mode' - 'openai_responses_supported')
        || jsonb_build_object(
            'openai_text_route_mode',
            CASE
                WHEN extra ->> 'openai_text_route_mode' IN (
                    'preserve_client_protocol',
                    'force_responses',
                    'force_chat_completions'
                ) THEN extra ->> 'openai_text_route_mode'
                WHEN COALESCE(extra, '{}'::jsonb) ? 'openai_text_route_mode'
                    THEN 'preserve_client_protocol'
                WHEN extra ->> 'openai_responses_mode' IN ('force_responses', 'force_chat_completions')
                    THEN extra ->> 'openai_responses_mode'
                ELSE 'preserve_client_protocol'
            END,
            'openai_responses_probe_status',
            CASE
                WHEN extra ->> 'openai_responses_probe_status' IN ('supported', 'unsupported', 'unknown')
                    THEN extra ->> 'openai_responses_probe_status'
                WHEN COALESCE(extra, '{}'::jsonb) ? 'openai_responses_probe_status'
                    THEN 'unknown'
                WHEN jsonb_typeof(extra -> 'openai_responses_supported') = 'boolean'
                    THEN CASE WHEN (extra ->> 'openai_responses_supported')::boolean
                        THEN 'supported'
                        ELSE 'unsupported'
                    END
                ELSE 'unknown'
            END
        )
WHERE platform = 'openai'
  AND type = 'apikey';

DROP FUNCTION IF EXISTS migrate_openai_workload_capabilities_247(JSONB);
