package dto

import (
	"encoding/json"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 模型广场只公开投影说明，旧模型省略字段；HTML仍保持普通JSON字符串。
func TestMarketplaceModelDescriptionDTO(t *testing.T) {
	output := ModelMarketplaceGroupsFromService([]service.ModelMarketplaceGroup{{Models: []service.ModelMarketplaceModel{
		{ID: "a", ModelDescription: "480p，5～30秒<script>"}, {ID: "old"},
	}}})
	encoded, err := json.Marshal(output[0].Models)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"model_description":"480p，5～30秒\u003cscript\u003e"`)
	legacy, err := json.Marshal(output[0].Models[1])
	require.NoError(t, err)
	require.NotContains(t, string(legacy), "model_description")
	require.NotContains(t, string(encoded), "model_details")
}
