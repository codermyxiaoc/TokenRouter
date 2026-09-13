package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrokBillingSnapshotIsSchedulerNeutral(t *testing.T) {
	t.Parallel()

	require.True(t, isSchedulerNeutralExtraKey("grok_billing_snapshot"))
	require.False(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"grok_billing_snapshot": map[string]any{"usage_percent": 50},
	}))
}

func TestOpenAIResetCreditSnapshotIsSchedulerNeutral(t *testing.T) {
	t.Parallel()

	require.True(t, isSchedulerNeutralExtraKey("codex_reset_credit_snapshot"))
	require.False(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"codex_reset_credit_snapshot": map[string]any{"available_count": 1},
	}))
}

func TestCNUsageMonitorSnapshotIsSchedulerNeutralForGenericUpdates(t *testing.T) {
	t.Parallel()
	require.True(t, isSchedulerNeutralExtraKey("cn_usage_monitor_snapshot"))
	require.False(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"cn_usage_monitor_snapshot": map[string]any{"version": 1},
	}))
}
