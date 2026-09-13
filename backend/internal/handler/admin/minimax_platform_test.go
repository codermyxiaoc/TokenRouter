package admin

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin/binding"
	"github.com/stretchr/testify/require"
)

func TestMiniMaxGroupRequestValidation(t *testing.T) {
	// 管理表单和直接调用 API 的分组平台校验采用相同允许列表。
	require.NoError(t, binding.Validator.ValidateStruct(&CreateGroupRequest{Name: "MiniMax", Platform: service.PlatformMiniMax}))
	require.NoError(t, binding.Validator.ValidateStruct(&UpdateGroupRequest{Platform: service.PlatformMiniMax}))
	require.Equal(t, "minimax", platformToLiteLLMProvider[service.PlatformMiniMax])
}

func TestMiniMaxAccountImportPayload(t *testing.T) {
	// 通用备份导入保持原格式，账号类型与协议组合继续由 CreateAccount 校验。
	require.NoError(t, validateDataAccount(DataAccount{
		Name: "MiniMax", Platform: service.PlatformMiniMax, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "minimax-key", "account_mode": service.AccountModeCoding, "api_protocol": service.APIProtocolAdaptive},
	}))
}
