package dto

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// API 必须显式返回 false，避免客户端把关闭状态当作缺失字段沿用旧值。
func TestGroupDTOIncludesMediaSubscriptionPriority(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		group := &service.Group{ImageSubscriptionRatePriority: enabled, VideoSubscriptionRatePriority: enabled}
		for _, dto := range []any{GroupFromService(group), GroupFromServiceShallow(group), GroupFromServiceAdmin(group)} {
			encoded, err := json.Marshal(dto)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(encoded, &fields))
			require.Equal(t, enabled, fields["image_subscription_rate_priority"])
			require.Equal(t, enabled, fields["video_subscription_rate_priority"])
		}
	}
}
