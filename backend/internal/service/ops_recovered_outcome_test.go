package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 只有采集器确认的恢复记录显示恢复徽章，流内失败不能因 HTTP 200 被误标。
func TestOpsRecoveredOutcomeRequiresRecordedRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, phase, message string
		status               int
		recovered            bool
	}{
		{"recovered_upstream", "upstream", "Recovered upstream error 503: unavailable", 200, true},
		{"recovered_credentials", "account_auth", "Recovered account authentication failure", 200, true},
		{"final_failure", "upstream", "Recovered upstream error 503", 503, false},
		{"partial_stream_failure", "upstream", "upstream stream failed", 200, false},
		{"local_rejection", "request", "Recovered upstream error 503", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := &OpsErrorLog{Phase: tc.phase, Message: tc.message, StatusCode: 503}
			entry.SetClientStatus(tc.status)
			require.Equal(t, 503, entry.StatusCode)
			require.Equal(t, tc.status, entry.ClientStatusCode)
			require.Equal(t, tc.recovered, entry.RecoveredUpstream)
		})
	}
}
