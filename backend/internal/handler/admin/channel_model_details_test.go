//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 管理端创建、编辑回显与复制均携带独立说明，不共享可变映射。
func TestChannelModelDetailsDTO(t *testing.T) {
	var request channelModelPricingRequest
	require.NoError(t, json.Unmarshal([]byte(`{"models":["a","b"],"platform":"video","model_details":{"a":{"enabled":true,"description":"480p，5～30秒"},"b":{"enabled":false,"description":"草稿"}}}`), &request))
	entries := pricingRequestToService([]channelModelPricingRequest{request})
	response := pricingToResponse(&entries[0])
	require.Equal(t, request.ModelDetails, response.ModelDetails)
	response.ModelDetails["a"] = service.ModelDetails{}
	request.ModelDetails["b"] = service.ModelDetails{}
	require.True(t, entries[0].ModelDetails["a"].Enabled)
	require.Equal(t, "草稿", entries[0].ModelDetails["b"].Description)
	encoded, err := json.Marshal(pricingToResponse(&entries[0]))
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"enabled":false,"description":"草稿"`)
	legacy := pricingRequestToService([]channelModelPricingRequest{{Models: []string{"old"}}})
	encoded, err = json.Marshal(pricingToResponse(&legacy[0]))
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "model_details")
}
