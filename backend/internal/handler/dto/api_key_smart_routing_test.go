package dto

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeySmartRoutingDTO(t *testing.T) {
	// 智能路由详情保留候选顺序，内部占位前缀不得出现在公开复合映射中。
	key := &service.APIKey{SmartRouting: true, SmartRoutingCooldownSeconds: 60, CompositeGroups: []service.APIKeyCompositeGroup{
		{GroupID: 20, Prefix: "route_20", Group: &service.Group{ID: 20}},
		{GroupID: 3, Prefix: "route_3", Group: &service.Group{ID: 3}},
	}}
	out := APIKeyFromService(key)
	require.True(t, out.SmartRouting)
	require.Equal(t, 60, out.SmartRoutingCooldownSeconds)
	require.Equal(t, []int64{20, 3}, out.SmartRoutingGroupIDs)
	require.Equal(t, int64(20), out.SmartRoutingGroups[0].ID)
	require.Equal(t, int64(3), out.SmartRoutingGroups[1].ID)
	require.Empty(t, out.CompositeGroups)
}
