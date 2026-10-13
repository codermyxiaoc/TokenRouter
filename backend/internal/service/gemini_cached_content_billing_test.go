//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

// 输入计费保留渠道模式，只有已确认的存储有效期可缩减冻结报价。
func TestGeminiCacheFrozenCostPreservesPerRequestAndStorage(t *testing.T) {
	h := &GeminiCacheBillingHandle{InputCost: &CostBreakdown{OutputCost: 2, TotalCost: 2, ActualCost: 12, BillingMode: string(BillingModePerRequest)}, StorageCost: &CostBreakdown{TotalCost: 4, ActualCost: 24}, InputTokens: 100, StorageTokenHours: 100}
	got := h.FrozenCost(100, 50)
	require.Equal(t, string(BillingModePerRequest), got.BillingMode)
	require.Equal(t, 2.0, got.OutputCost)
	require.Equal(t, 4.0, got.TotalCost)
	require.Equal(t, 24.0, got.ActualCost)
	require.Equal(t, 2.0, h.FrozenStorageCost(50))
	require.Equal(t, 2.0, h.FrozenCost(100, 0).TotalCost)
	require.Equal(t, 4.0, h.FrozenCost(0, 100).TotalCost)
	require.Equal(t, 6.0, h.FrozenCost(100, 200).TotalCost, "不能超过已冻结时长")
	require.Zero(t, h.FrozenCost(0, -1).TotalCost)
	require.Equal(t, 2.0, h.InputCost.TotalCost, "构造最终费用不得改写原始报价")
}
