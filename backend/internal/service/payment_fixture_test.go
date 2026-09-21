//go:build unit

package service

import (
	"strconv"
	"sync/atomic"
)

var paymentFixtureSequence atomic.Uint64

// nextPaymentFixtureID 为测试数据生成进程内唯一标识，避免依赖系统时钟精度。
func nextPaymentFixtureID() string {
	return strconv.FormatUint(paymentFixtureSequence.Add(1), 10)
}
