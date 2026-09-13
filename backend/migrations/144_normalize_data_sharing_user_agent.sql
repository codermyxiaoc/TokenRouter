UPDATE data_share_sessions
SET user_agent = LEFT(TRIM(SPLIT_PART(user_agent, '/', 1)), 512)
WHERE user_agent LIKE '%/%';

UPDATE data_share_sessions
SET user_agent = LEFT(TRIM(SPLIT_PART(COALESCE(NULLIF(meta->>'user_agent', ''), ''), '/', 1)), 512)
WHERE user_agent = ''
  AND COALESCE(NULLIF(meta->>'user_agent', ''), '') <> '';
