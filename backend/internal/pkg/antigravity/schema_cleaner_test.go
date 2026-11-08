package antigravity

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanJSONSchema_ArrayPrefixItems(t *testing.T) {
	// 模拟 Claude Code 2.1 Artifact 工具的 query.where 参数 Schema (Draft 2020-12 prefixItems 元组)
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"where": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "array",
							"prefixItems": []any{
								map[string]any{"type": "string"},
								map[string]any{"type": "string", "enum": []any{"==", "!=", ">", "<"}},
								map[string]any{},
							},
						},
						"maxItems": float64(10),
					},
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	query, ok := cleaned["properties"].(map[string]any)["query"].(map[string]any)
	require.True(t, ok)
	where, ok := query["properties"].(map[string]any)["where"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "array", where["type"])

	whereItems, ok := where["items"].(map[string]any)
	require.True(t, ok, "where.items must be an object")
	assert.Equal(t, "array", whereItems["type"])
	// prefixItems 应在 whereItems 中被彻底移除
	assert.Nil(t, whereItems["prefixItems"])

	// 关键验证：where.items.items 必须存在且有效，不能缺失导致 Gemini 400
	innerItems, ok := whereItems["items"].(map[string]any)
	require.True(t, ok, "where.items.items must be an object")
	assert.Equal(t, "string", innerItems["type"])
}

func TestCleanJSONSchema_ArrayMissingItemsFallback(t *testing.T) {
	// 针对任何缺少 items 的 array，必须兜底注入 items: {type: string}
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tags": map[string]any{
				"type": "array",
			},
			"nested_empty_array": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "array",
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	tags, ok := props["tags"].(map[string]any)
	require.True(t, ok)
	tagsItems, ok := tags["items"].(map[string]any)
	require.True(t, ok, "tags.items must be an object")
	assert.Equal(t, "string", tagsItems["type"])

	nested, ok := props["nested_empty_array"].(map[string]any)
	require.True(t, ok)
	nestedItems, ok := nested["items"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "array", nestedItems["type"])
	nestedInnerItems, ok := nestedItems["items"].(map[string]any)
	require.True(t, ok, "nested items.items must be an object")
	assert.Equal(t, "string", nestedInnerItems["type"])
}

func TestCleanJSONSchema_ArrayExistingItemsPreserved(t *testing.T) {
	// 正常的 array items 不受影响
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"numbers": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "integer",
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	numbers, ok := props["numbers"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "array", numbers["type"])
	items, ok := numbers["items"].(map[string]any)
	require.True(t, ok, "numbers.items must be an object")
	assert.Equal(t, "integer", items["type"])
}

func TestCleanJSONSchema_EnumOnlySchemaInTuple(t *testing.T) {
	// 测试 enum-only schema 不会被误判为 object，也不应被注入 reason 属性
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"choice": map[string]any{
				"enum": []any{"option_a", "option_b"},
			},
			"tuple_with_enum": map[string]any{
				"type": "array",
				"prefixItems": []any{
					map[string]any{
						"enum": []any{"read", "write"},
					},
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	choice, ok := props["choice"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", choice["type"])
	assert.Nil(t, choice["properties"], "enum-only schema must NOT be treated as object with reason property")
	assert.Equal(t, []any{"option_a", "option_b"}, choice["enum"])

	tupleArray, ok := props["tuple_with_enum"].(map[string]any)
	require.True(t, ok)
	tupleItems, ok := tupleArray["items"].(map[string]any)
	require.True(t, ok, "tuple_with_enum.items must be an object")
	assert.Equal(t, "string", tupleItems["type"])
	assert.Nil(t, tupleItems["properties"])
	assert.Equal(t, []any{"read", "write"}, tupleItems["enum"])
}

func TestCleanJSONSchema_ConstKeywordConversion(t *testing.T) {
	// 测试 const 关键字自动转换为 Gemini 兼容的 enum: [const] 且具有合法 type
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"const": "ping",
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	action, ok := props["action"].(map[string]any)
	require.True(t, ok)
	assert.Nil(t, action["const"], "const keyword must be removed")
	assert.Equal(t, "string", action["type"])
	assert.Equal(t, []any{"ping"}, action["enum"])
}

func TestCleanJSONSchema_AnyOfNestedPrefixItems(t *testing.T) {
	// 测试 anyOf 分支合并进来的嵌套 prefixItems 也被完整深度清洗
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{"type": "string"},
		},
		"anyOf": []any{
			map[string]any{
				"properties": map[string]any{
					"extra_tuple": map[string]any{
						"type": "array",
						"prefixItems": []any{
							map[string]any{"type": "integer"},
						},
					},
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	require.NotNil(t, props["extra_tuple"])
	extraTuple, ok := props["extra_tuple"].(map[string]any)
	require.True(t, ok)
	assert.Nil(t, extraTuple["prefixItems"], "nested prefixItems from anyOf merge must be cleaned")
	extraItems, ok := extraTuple["items"].(map[string]any)
	require.True(t, ok, "extra_tuple.items must be an object")
	assert.Equal(t, "integer", extraItems["type"])
}

func TestCleanJSONSchema_EmptyPrefixItems(t *testing.T) {
	// 验证空 prefixItems: [] 也被安全删除，不遗留非法关键字
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"empty_tuple": map[string]any{
				"type":        "array",
				"prefixItems": []any{},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	emptyTuple, ok := props["empty_tuple"].(map[string]any)
	require.True(t, ok)
	assert.Nil(t, emptyTuple["prefixItems"], "empty prefixItems array must be removed")
	emptyTupleItems, ok := emptyTuple["items"].(map[string]any)
	require.True(t, ok, "empty_tuple.items must be an object")
	assert.Equal(t, "string", emptyTupleItems["type"])
}

// 自引用的定义（链表 Node.next）展开到循环处收敛为对象占位，不会无限递归。
func TestCleanJSONSchema_SelfReferenceTerminates(t *testing.T) {
	schema := map[string]any{
		"$defs": map[string]any{
			"Node": map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string"}, "next": map[string]any{"$ref": "#/$defs/Node"}},
			},
		},
		"$ref": "#/$defs/Node",
	}
	cleaned := CleanJSONSchema(schema)
	require.NotNil(t, cleaned)
	props := cleaned["properties"].(map[string]any)
	require.Equal(t, "string", props["value"].(map[string]any)["type"])
	next := props["next"].(map[string]any)
	require.Equal(t, "object", next["type"])
	_, hasRef := next["$ref"]
	require.False(t, hasRef)
}

// 两个定义互相引用（A→B→A）同样在循环处收敛。
func TestCleanJSONSchema_MutualReferenceTerminates(t *testing.T) {
	schema := map[string]any{
		"$defs": map[string]any{
			"A": map[string]any{"type": "object", "properties": map[string]any{"b": map[string]any{"$ref": "#/$defs/B"}}},
			"B": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"$ref": "#/$defs/A"}}},
		},
		"$ref": "#/$defs/A",
	}
	require.NotNil(t, CleanJSONSchema(schema))
}

// 非递归的复用定义（同一定义在兄弟节点出现多次）仍完整展开，保持原有兼容。
func TestCleanJSONSchema_SiblingReuseStillExpands(t *testing.T) {
	schema := map[string]any{
		"$defs": map[string]any{"Addr": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}},
		"type":  "object",
		"properties": map[string]any{
			"home": map[string]any{"$ref": "#/$defs/Addr"},
			"work": map[string]any{"$ref": "#/$defs/Addr"},
		},
	}
	cleaned := CleanJSONSchema(schema)
	props := cleaned["properties"].(map[string]any)
	for _, key := range []string{"home", "work"} {
		city := props[key].(map[string]any)["properties"].(map[string]any)["city"].(map[string]any)
		require.Equal(t, "string", city["type"], key)
	}
}

// 宽而深的引用组合（每层两个分支互相引用不同定义）受节点预算约束，有界完成。
func TestCleanJSONSchema_WideReferenceGraphIsBounded(t *testing.T) {
	defs := map[string]any{}
	for i := 0; i < 30; i++ {
		next := fmt.Sprintf("#/$defs/D%d", i+1)
		defs[fmt.Sprintf("D%d", i)] = map[string]any{"type": "object", "properties": map[string]any{
			"left": map[string]any{"$ref": next}, "right": map[string]any{"$ref": next},
		}}
	}
	defs["D30"] = map[string]any{"type": "string"}
	schema := map[string]any{"$defs": defs, "$ref": "#/$defs/D0"}
	require.NotNil(t, CleanJSONSchema(schema))
}
