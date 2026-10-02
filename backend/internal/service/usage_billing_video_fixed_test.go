//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/stretchr/testify/require"
)

// 零值新字段不改变历史指纹或分配 JSON，启用固定费用后才隔离不同计费命令。
func TestVideoFixedBillingFingerprintCompatibility(t *testing.T) {
	original := BatchImageBalanceHoldCommand{VideoEntity: true, PricingSnapshotVersion: 3, BaseAmountUSD: 10}
	before := buildBatchImageBalanceHoldFingerprint(&original)
	zero := original
	zero.VideoFixedAmountUSD, zero.VideoActualFixedAmountUSD = 0, 0
	require.Equal(t, before, buildBatchImageBalanceHoldFingerprint(&zero))
	zero.VideoFixedAmountUSD = 2
	require.NotEqual(t, before, buildBatchImageBalanceHoldFingerprint(&zero))
	first := buildBatchImageBalanceHoldFingerprint(&zero)
	zero.VideoActualFixedAmountUSD = 1
	require.NotEqual(t, first, buildBatchImageBalanceHoldFingerprint(&zero))
	encoded, err := json.Marshal(domain.BillingAllocation{Type: domain.BillingAllocationTypeBalance, AmountUSD: 1})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "component")
}

// 按秒预扣只有显式开启才改变指纹，时长变化必须与原任务幂等合同隔离。
func TestVideoTokenPrepayFingerprintCompatibility(t *testing.T) {
	legacy := BatchImageBalanceHoldCommand{VideoEntity: true, PricingSnapshotVersion: 3, BaseAmountUSD: 2.4}
	want := buildBatchImageBalanceHoldFingerprint(&legacy)
	legacy.VideoTokenPrepay = false
	legacy.VideoPrepayDurationSeconds = 0
	require.Equal(t, want, buildBatchImageBalanceHoldFingerprint(&legacy))
	legacy.VideoTokenPrepay, legacy.VideoPrepayDurationSeconds = true, 8
	first := buildBatchImageBalanceHoldFingerprint(&legacy)
	require.NotEqual(t, want, first)
	legacy.VideoPrepayDurationSeconds = 9
	require.NotEqual(t, first, buildBatchImageBalanceHoldFingerprint(&legacy))
}
