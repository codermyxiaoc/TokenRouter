package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGroupClientProtocolMigrationBackfillMatrix 锁定六个平台及 OpenAI 旧开关的回填语义。
func TestGroupClientProtocolMigrationBackfillMatrix(t *testing.T) {
	content, err := FS.ReadFile("235_add_group_allowed_client_protocols.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	require.Contains(t, sql, "add column if not exists allowed_client_protocols jsonb")
	require.Contains(t, sql, "when 'anthropic' then '[\"anthropic_messages\",\"openai_responses\",\"openai_chat_completions\"]'::jsonb")
	require.Contains(t, sql, "when allow_messages_dispatch then '[\"anthropic_messages\",\"openai_responses\",\"openai_chat_completions\"]'::jsonb")
	require.Contains(t, sql, "else '[\"openai_responses\",\"openai_chat_completions\"]'::jsonb")
	require.Contains(t, sql, "when 'gemini' then '[\"anthropic_messages\",\"openai_responses\",\"openai_chat_completions\",\"gemini_generate_content\"]'::jsonb")
	require.Contains(t, sql, "when 'antigravity' then '[\"anthropic_messages\",\"openai_responses\",\"openai_chat_completions\",\"gemini_generate_content\"]'::jsonb")
	require.Contains(t, sql, "when 'qoder' then '[\"anthropic_messages\",\"openai_responses\",\"openai_chat_completions\"]'::jsonb")
	require.Contains(t, sql, "when 'grok' then '[\"anthropic_messages\",\"openai_responses\",\"openai_chat_completions\"]'::jsonb")
	require.Contains(t, sql, "where allowed_client_protocols is null")
	require.Contains(t, sql, "alter column allowed_client_protocols set default '[]'::jsonb")
	require.Contains(t, sql, "alter column allowed_client_protocols set not null")
}
