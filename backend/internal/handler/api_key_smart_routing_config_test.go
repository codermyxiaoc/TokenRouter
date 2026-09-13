package handler

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSmartRoutingCooldownRequestBinding(t *testing.T) {
	// HTTP 边界同时拒绝字符串、小数和越界整数，避免前端之外的调用绕过校验。
	for _, input := range []string{`-1`, `3601`, `0.5`, `"60"`, `true`, `9223372036854775808`} {
		payload := []byte(fmt.Sprintf(`{"name":"cooldown","smart_routing_cooldown_seconds":%s}`, input))
		var create CreateAPIKeyRequest
		err := json.Unmarshal(payload, &create)
		if err == nil {
			err = validateAPIKeyCreateRequest(create)
			require.ErrorIs(t, err, service.ErrSmartRoutingCooldownInvalid)
		}
		require.Error(t, err, input)
		var update UpdateAPIKeyRequest
		err = json.Unmarshal(payload, &update)
		if err == nil {
			err = validateAPIKeyUpdateRequest(update)
			require.ErrorIs(t, err, service.ErrSmartRoutingCooldownInvalid)
		}
		require.Error(t, err, input)
	}
	for _, input := range []string{`{}`, `{"smart_routing_cooldown_seconds":0}`, `{"smart_routing_cooldown_seconds":3600}`} {
		var create CreateAPIKeyRequest
		var update UpdateAPIKeyRequest
		require.NoError(t, json.Unmarshal([]byte(input), &create))
		require.NoError(t, json.Unmarshal([]byte(input), &update))
		require.NoError(t, validateAPIKeyCreateRequest(create))
		require.NoError(t, validateAPIKeyUpdateRequest(update))
		if input == `{}` {
			require.Nil(t, create.SmartRoutingCooldownSeconds)
			require.Nil(t, update.SmartRoutingCooldownSeconds)
		} else {
			require.NotNil(t, create.SmartRoutingCooldownSeconds)
			require.Equal(t, create.SmartRoutingCooldownSeconds, update.SmartRoutingCooldownSeconds)
		}
	}
}
