package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration241RemovesOpenAILongContextBillingToggle(t *testing.T) {
	content, err := FS.ReadFile("241_remove_openai_long_context_billing_toggle.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "DROP TRIGGER IF EXISTS accounts_enforce_openai_long_context_billing_extra")
	require.Contains(t, sql, "DROP TRIGGER IF EXISTS accounts_propagate_openai_long_context_billing_extra")
	require.Contains(t, sql, "DROP FUNCTION IF EXISTS public.enforce_openai_long_context_billing_extra()")
	require.Contains(t, sql, "DROP FUNCTION IF EXISTS public.propagate_openai_long_context_billing_extra_to_shadows()")
	require.Contains(t, sql, "- 'openai_long_context_billing_enabled'")
}
