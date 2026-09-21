package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireObjectInputSchema(t *testing.T, schema json.RawMessage) map[string]json.RawMessage {
	t.Helper()

	require.NotEmpty(t, schema)

	var parsed map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(schema, &parsed))
	require.JSONEq(t, `"object"`, string(parsed["type"]))
	require.Contains(t, parsed, "properties")

	var properties map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(parsed["properties"], &properties))

	return parsed
}

func TestResponsesToAnthropic_CustomGrammarToolUsesObjectSchema(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.2",
		"input": "apply this patch",
		"tools": [{
			"type": "custom",
			"name": "apply_patch",
			"description": "Apply a patch to the working tree",
			"format": {
				"type": "grammar",
				"syntax": "lark",
				"definition": "start: /.+/"
			}
		}]
	}`)

	var req ResponsesRequest
	require.NoError(t, json.Unmarshal(body, &req))

	anthropicReq, err := ResponsesToAnthropicRequest(&req)
	require.NoError(t, err)
	require.Len(t, anthropicReq.Tools, 1)

	tool := anthropicReq.Tools[0]
	assert.Empty(t, tool.Type)
	assert.Equal(t, "apply_patch", tool.Name)
	assert.Equal(t, "Apply a patch to the working tree", tool.Description)
	requireObjectInputSchema(t, tool.InputSchema)
	assert.JSONEq(t, `{"type":"object","properties":{}}`, string(tool.InputSchema))

	wire, err := json.Marshal(tool)
	require.NoError(t, err)
	assert.NotContains(t, string(wire), `"type":"custom"`)
	assert.NotContains(t, string(wire), `"format"`)
	assert.NotContains(t, string(wire), `"grammar"`)
}

func TestResponsesToAnthropic_CustomToolPreservesSchemaParameters(t *testing.T) {
	tools := convertResponsesToAnthropicTools([]ResponsesTool{{
		Type:        "custom",
		Name:        "edit_file",
		Description: "Edit a file",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"patch":{"type":"string"}},"required":["patch"]}`),
	}})

	require.Len(t, tools, 1)
	assert.Empty(t, tools[0].Type)
	assert.Equal(t, "edit_file", tools[0].Name)

	schema := requireObjectInputSchema(t, tools[0].InputSchema)
	assert.JSONEq(t, `{"patch":{"type":"string"}}`, string(schema["properties"]))
	assert.JSONEq(t, `["patch"]`, string(schema["required"]))
}

func TestResponsesToAnthropic_FunctionToolSchemaUnchanged(t *testing.T) {
	parameters := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)
	tools := convertResponsesToAnthropicTools([]ResponsesTool{{
		Type:        "function",
		Name:        "get_weather",
		Description: "Get weather",
		Parameters:  parameters,
	}})

	require.Len(t, tools, 1)
	assert.Empty(t, tools[0].Type)
	assert.Equal(t, "get_weather", tools[0].Name)
	assert.Equal(t, "Get weather", tools[0].Description)
	assert.JSONEq(t, string(parameters), string(tools[0].InputSchema))
}

func TestResponsesToAnthropic_MixedToolsProduceValidAnthropicTools(t *testing.T) {
	tools := convertResponsesToAnthropicTools([]ResponsesTool{
		{
			Type:       "function",
			Name:       "read_file",
			Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		},
		{
			Type: "custom",
			Name: "apply_patch",
		},
		{
			Type: "web_search",
		},
	})

	require.Len(t, tools, 3)
	assert.Empty(t, tools[0].Type)
	assert.Equal(t, "read_file", tools[0].Name)
	requireObjectInputSchema(t, tools[0].InputSchema)

	assert.Empty(t, tools[1].Type)
	assert.Equal(t, "apply_patch", tools[1].Name)
	assert.JSONEq(t, `{"type":"object","properties":{}}`, string(tools[1].InputSchema))

	assert.Equal(t, "web_search_20250305", tools[2].Type)
	assert.Equal(t, "web_search", tools[2].Name)
	assert.Empty(t, tools[2].InputSchema)
	serverToolWire, err := json.Marshal(tools[2])
	require.NoError(t, err)
	assert.NotContains(t, string(serverToolWire), `"input_schema"`)
}

func TestResponsesToAnthropic_DefaultToolNormalizesInputSchema(t *testing.T) {
	tools := convertResponsesToAnthropicTools([]ResponsesTool{{
		Type: "local_shell",
		Name: "shell",
	}})

	require.Len(t, tools, 1)
	assert.Equal(t, "local_shell", tools[0].Type)
	assert.Equal(t, "shell", tools[0].Name)
	assert.JSONEq(t, `{"type":"object","properties":{}}`, string(tools[0].InputSchema))
}

func TestResponsesToAnthropic_FlattensRootObjectUnionSchema(t *testing.T) {
	parameters := json.RawMessage(`{
		"oneOf": [
			{"type":"object","properties":{"path":{"type":"string"},"mode":{"type":"string"}},"required":["path"]},
			{"type":"object","properties":{"path":{"type":"string"},"line":{"type":"integer"}},"required":["path"]}
		]
	}`)
	tools := convertResponsesToAnthropicTools([]ResponsesTool{
		{Type: "function", Name: "read_file", Parameters: parameters},
	})

	require.Len(t, tools, 1)
	schema := requireObjectInputSchema(t, tools[0].InputSchema)
	var properties map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(schema["properties"], &properties))
	assert.Contains(t, properties, "path")
	assert.Contains(t, properties, "mode")
	assert.Contains(t, properties, "line")
	assert.JSONEq(t, `["path"]`, string(schema["required"]))
}

func TestResponsesToAnthropic_FlattensRootAllOfRequiredUnion(t *testing.T) {
	parameters := json.RawMessage(`{
		"allOf": [
			{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},
			{"type":"object","properties":{"line":{"type":"integer"}},"required":["line"]}
		]
	}`)
	tools := convertResponsesToAnthropicTools([]ResponsesTool{
		{Type: "function", Name: "read_file", Parameters: parameters},
	})

	require.Len(t, tools, 1)
	schema := requireObjectInputSchema(t, tools[0].InputSchema)
	assert.JSONEq(t, `["path","line"]`, string(schema["required"]))
}

// 同名参数的上下界必须同时满足；不能把 allOf 转成任何一侧通过即可的 anyOf。
func TestResponsesToAnthropic_AllOfPreservesPropertyConjunction(t *testing.T) {
	parameters := json.RawMessage(`{
		"allOf": [
			{"type":"object","properties":{"line":{"type":"integer","minimum":1}},"required":["line"]},
			{"type":"object","properties":{"line":{"maximum":10}}}
		]
	}`)
	tools := convertResponsesToAnthropicTools([]ResponsesTool{
		{Type: "function", Name: "read_line", Parameters: parameters},
	})

	require.Len(t, tools, 1)
	schema := requireObjectInputSchema(t, tools[0].InputSchema)
	assert.NotContains(t, schema, "allOf")
	assert.JSONEq(t, `{"line":{"allOf":[{"type":"integer","minimum":1},{"maximum":10}]}}`, string(schema["properties"]))
	assert.JSONEq(t, `["line"]`, string(schema["required"]))
}

// 根级参数约束对所有备选分支生效，但联合分支内部仍按或合并。
func TestResponsesToAnthropic_RootPropertiesConjoinUnionAlternatives(t *testing.T) {
	parameters := json.RawMessage(`{
		"type":"object",
		"properties":{"path":{"type":"string","minLength":1},"mode":{"type":"string"}},
		"oneOf": [
			{"type":"object","properties":{"path":{"pattern":"^src/"}},"required":["path"]},
			{"type":"object","properties":{"path":{"pattern":"^tests/"}},"required":["path"]}
		]
	}`)
	tools := convertResponsesToAnthropicTools([]ResponsesTool{
		{Type: "function", Name: "read_file", Parameters: parameters},
	})

	require.Len(t, tools, 1)
	schema := requireObjectInputSchema(t, tools[0].InputSchema)
	assert.NotContains(t, schema, "oneOf")
	assert.JSONEq(t, `{
		"path":{"allOf":[{"type":"string","minLength":1},{"anyOf":[{"pattern":"^src/"},{"pattern":"^tests/"}]}]},
		"mode":{"type":"string"}
	}`, string(schema["properties"]))
	assert.JSONEq(t, `["path"]`, string(schema["required"]))
}

// 不同根级关键字同时生效；各联合内部只保留共有必填项，再与其它约束取并集。
func TestResponsesToAnthropic_MultipleRootUnionsPreserveRequiredConjunction(t *testing.T) {
	parameters := json.RawMessage(`{
		"type":"object","properties":{"id":{"type":"string"}},"required":["id"],
		"oneOf": [
			{"type":"object","properties":{"path":{"type":"string"},"line":{"type":"integer"}},"required":["path","line"]},
			{"type":"object","properties":{"path":{"type":"string"},"range":{"type":"string"}},"required":["path","range"]}
		],
		"anyOf": [
			{"type":"object","properties":{"mode":{"type":"string"},"offset":{"type":"integer"}},"required":["mode","offset"]},
			{"type":"object","properties":{"mode":{"type":"string"},"limit":{"type":"integer"}},"required":["mode","limit"]}
		],
		"allOf": [
			{"type":"object","properties":{"token":{"type":"string"}},"required":["id","token"]}
		]
	}`)
	tools := convertResponsesToAnthropicTools([]ResponsesTool{
		{Type: "function", Name: "read_file", Parameters: parameters},
	})

	require.Len(t, tools, 1)
	schema := requireObjectInputSchema(t, tools[0].InputSchema)
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		assert.NotContains(t, schema, keyword)
	}
	var required []string
	require.NoError(t, json.Unmarshal(schema["required"], &required))
	assert.ElementsMatch(t, []string{"id", "path", "mode", "token"}, required)
}
