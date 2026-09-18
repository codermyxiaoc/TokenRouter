//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"testing"
)

func TestAntigravityTokenCacheAccountIsolation(t *testing.T) {
	a := &Account{ID: 51, Credentials: map[string]any{"project_id": "shared-project"}}
	b := &Account{ID: 52, Credentials: map[string]any{"project_id": "shared-project"}}
	require.NotEqual(t, AntigravityTokenCacheKey(a), AntigravityTokenCacheKey(b))
	before := AntigravityTokenCacheKey(a)
	a.Credentials["project_id"] = "new-project"
	require.Equal(t, before, AntigravityTokenCacheKey(a))
}

// 原生入口保留客户端函数与未知字段；只清理不兼容内置工具和对应过时开关。
func TestAntigravityMixedToolsPreserveNativePayload(t *testing.T) {
	for _, builtin := range []string{"googleSearch", "codeExecution"} {
		body := []byte(`{"tools":[{"functionDeclarations":[{"name":"functions.spawn_agent"}],"` + builtin + `":{}},{"` + builtin + `":{}},{"unknownTool":{"enabled":true}}],"toolConfig":{"includeServerSideToolInvocations":true,"include_server_side_tool_invocations":true,"functionCallingConfig":{"mode":"AUTO"}},"seed":9007199254740993}`)
		out, err := enableMixedGeminiToolInvocations(body)
		require.NoError(t, err)
		require.Len(t, gjson.GetBytes(out, "tools").Array(), 2)
		require.Equal(t, "functions.spawn_agent", gjson.GetBytes(out, "tools.0.functionDeclarations.0.name").String())
		require.False(t, gjson.GetBytes(out, "tools.0."+builtin).Exists())
		require.True(t, gjson.GetBytes(out, "tools.1.unknownTool.enabled").Bool())
		require.Equal(t, "9007199254740993", gjson.GetBytes(out, "seed").Raw)
		require.Equal(t, "AUTO", gjson.GetBytes(out, "toolConfig.functionCallingConfig.mode").String())
		require.False(t, gjson.GetBytes(out, "toolConfig.includeServerSideToolInvocations").Exists())
	}
	pure := []byte(` {"tools":[{"googleSearch":{}}]} `)
	out, err := enableMixedGeminiToolInvocations(pure)
	require.NoError(t, err)
	require.Equal(t, pure, out)
}
