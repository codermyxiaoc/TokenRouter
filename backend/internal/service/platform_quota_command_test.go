//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 原任务平台必须经过报价、序列化和恢复保持不变，不能被后来修改的分组覆盖。
func TestPlatformQuotaCommandPreservesAcceptedPlatform(t *testing.T) {
	for _, tc := range []struct {
		name, requested, held, want string
	}{
		{"group_default", "", "", PlatformOpenAI},
		{"forced_platform", PlatformAntigravity, "", PlatformAntigravity},
		{"image_original_platform", PlatformGrok, PlatformGemini, PlatformGemini},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := &usageBillingParams{Platform: tc.requested, Cost: &CostBreakdown{TotalCost: 2, ActualCost: 1},
				APIKey: &APIKey{ID: 2, Group: &Group{Platform: PlatformOpenAI}}, User: &User{ID: 1}, Account: &Account{ID: 3}}
			if tc.held != "" {
				params.ImageReservation = &ImageBillingReservation{Hold: BatchImageBalanceHoldCommand{QuotaPlatform: tc.held}}
			}
			cmd := buildUsageBillingCommand("platform-original", nil, params)
			require.Equal(t, tc.want, cmd.QuotaPlatform)
			raw, err := json.Marshal(cmd)
			require.NoError(t, err)
			var restored UsageBillingCommand
			require.NoError(t, json.Unmarshal(raw, &restored))
			require.Equal(t, tc.want, restored.QuotaPlatform)
			require.Equal(t, cmd.RequestFingerprint, restored.RequestFingerprint)
		})
	}
}

// 扩展统计字段不能改变升级前的资金去重身份，否则同一历史任务会失去重放能力。
func TestPlatformQuotaCommandPreservesLegacyFinancialFingerprint(t *testing.T) {
	old := UsageBillingCommand{RequestID: "legacy", UserID: 1, APIKeyID: 2, AccountID: 3, BillableAmountUSD: 1}
	withPlatform := old
	withPlatform.QuotaPlatform = PlatformGrok
	old.Normalize()
	withPlatform.Normalize()
	require.Equal(t, old.RequestFingerprint, withPlatform.RequestFingerprint)
	oldHold := BatchImageBalanceHoldCommand{RequestID: "legacy-hold", UserID: 1, APIKeyID: 2, BatchID: "job", HoldAmount: 1}
	newHold := oldHold
	newHold.QuotaPlatform = PlatformGemini
	oldHold.Normalize()
	newHold.Normalize()
	require.Equal(t, oldHold.RequestFingerprint, newHold.RequestFingerprint)
}

// 批量作业的持久供应商协议本身决定平台，不依赖提交后已变动或删除的分组。
func TestBatchImageQuotaPlatformSurvivesGroupChanges(t *testing.T) {
	keyID := int64(2)
	for _, provider := range []string{BatchImageProviderGeminiAPI, BatchImageProviderVertex} {
		t.Run(provider, func(t *testing.T) {
			job := &BatchImageJob{BatchID: "job", UserID: 1, APIKeyID: &keyID, Provider: provider, EstimatedCost: 1}
			cmd, err := buildBatchImageHoldCommand(job, BatchImageCaptureRequestID(job.BatchID), 0.5, "")
			require.NoError(t, err)
			require.Equal(t, PlatformGemini, cmd.QuotaPlatform)
		})
	}
}
