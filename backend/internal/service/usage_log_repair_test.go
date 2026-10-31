//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// usageLogRepairStub 只记录修复调用；其余用量仓储方法不会被使用。
type usageLogRepairStub struct {
	UsageLogRepository
	calls []*UsageLog
	err   error
}

func (s *usageLogRepairStub) RepairUnsettledUsageLog(_ context.Context, log *UsageLog) error {
	s.calls = append(s.calls, log)
	return s.err
}

// 扣费成功后只对正数金额的记录调用修复；修复失败不影响已正确的资金结算。
func TestRepairUnsettledUsageLogOnlyForPositiveAmounts(t *testing.T) {
	stub := &usageLogRepairStub{}
	repairUnsettledUsageLog(context.Background(), stub, &UsageLog{RequestID: "client:a", ActualCost: 0.01}, "test")
	require.Len(t, stub.calls, 1)
	repairUnsettledUsageLog(context.Background(), stub, &UsageLog{RequestID: "client:b", ActualCost: 0}, "test")
	repairUnsettledUsageLog(context.Background(), stub, nil, "test")
	require.Len(t, stub.calls, 1, "零金额或空记录不修复")
	stub.err = errors.New("db down")
	require.NotPanics(t, func() {
		repairUnsettledUsageLog(context.Background(), stub, &UsageLog{RequestID: "client:c", ActualCost: 0.01}, "test")
	})
	// 不支持修复的仓储直接跳过。
	require.NotPanics(t, func() {
		repairUnsettledUsageLog(context.Background(), nil, &UsageLog{RequestID: "client:d", ActualCost: 0.01}, "test")
	})
}
