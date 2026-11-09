package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// shapeTools 后取出第一个工具的 parameters (无工具时 t.Fatal)。
func firstParams(t *testing.T, tools []any) map[string]any {
	t.Helper()
	shaped := shapeTools(tools)
	fn, ok := shaped[0].(map[string]any)["function"].(map[string]any)
	if !ok {
		t.Fatal("function 不存在")
	}
	params, _ := fn["parameters"].(map[string]any)
	return params
}

// 无类型 enum 补类型 (magpie fitShape; kimi 实测 11133 → 200)。
func TestShapeEnumGetsType(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "set_color",
			"description": "c",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"color": map[string]any{"enum": []any{"red", "green", "blue"}}},
			},
		},
	}})
	color := params["properties"].(map[string]any)["color"].(map[string]any)
	if color["type"] != "string" {
		t.Errorf("无类型 enum 应补 type=string, got %v", color["type"])
	}
	if !reflect.DeepEqual(color["enum"], []any{"red", "green", "blue"}) {
		t.Errorf("enum 值应原样保留, got %v", color["enum"])
	}
}

// 全 object 分支的无类型 oneOf 补 type=object (magpie #378; kimi 实测 11133)。
func TestShapeUntypedObjectOneOf(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "make",
			"parameters": map[string]any{
				"oneOf": []any{
					map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "number"}}},
					map[string]any{"type": "object", "properties": map[string]any{"y": map[string]any{"type": "string"}}},
				},
			},
		},
	}})
	if params["type"] != "object" {
		t.Errorf("全 object 的 oneOf 应补 type=object, got %v", params["type"])
	}
}

// bool 当 schema: true → {}, false → {"not": {}} (magpie fitShape; 插件此前直接 500)。
func TestShapeBoolSchema(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "f2",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": true, "b": false},
			},
		},
	}})
	props := params["properties"].(map[string]any)
	a, ok := props["a"].(map[string]any)
	if !ok || len(a) != 0 {
		t.Errorf("true 应替换为 {}, got %v", props["a"])
	}
	b, ok := props["b"].(map[string]any)
	if !ok {
		t.Fatalf("false 应替换为 map, got %v", props["b"])
	}
	not, ok := b["not"].(map[string]any)
	if !ok || len(not) != 0 {
		t.Errorf("false 应替换为 {\"not\": {}}, got %v", b)
	}
}

// 工具名非法字符换 _ 并去重 (magpie safeName; kimi 实测 11152)。
func TestShapeToolName(t *testing.T) {
	shaped := shapeTools([]any{
		map[string]any{"type": "function", "function": map[string]any{"name": "mcp.server.tool", "parameters": map[string]any{"type": "object"}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "mcp:server:tool", "parameters": map[string]any{"type": "object"}}},
	})
	n1 := shaped[0].(map[string]any)["function"].(map[string]any)["name"]
	n2 := shaped[1].(map[string]any)["function"].(map[string]any)["name"]
	if n1 != "mcp_server_tool" {
		t.Errorf("点应换 _, got %v", n1)
	}
	if n2 != "mcp_server_tool_2" {
		t.Errorf("重名应加序号, got %v", n2)
	}
}

// required 非列表丢弃; 空 enum 位置删除; 未知类型丢弃。
func TestShapeDegenerateForms(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "f3",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "String", "required": "x"},
					"b": map[string]any{"enum": []any{}},
				},
			},
		},
	}})
	props := params["properties"].(map[string]any)
	a := props["a"].(map[string]any)
	if _, has := a["type"]; has {
		t.Errorf("未知类型 String 应丢弃, got %v", a["type"])
	}
	if _, has := a["required"]; has {
		t.Errorf("required 非列表应丢弃")
	}
	if _, has := props["b"]; has {
		t.Errorf("空 enum 的 schema 位置应删除")
	}
}

// pattern 的 \0 改写 \u0000 (deepseek 校验器不认 \0, 匹配语义不变)。
func TestShapePatternNul(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "find_path",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"p": map[string]any{"type": "string", "pattern": "^a\\0b$"}},
			},
		},
	}})
	p := params["properties"].(map[string]any)["p"].(map[string]any)["pattern"]
	if p != "^a\\u0000b$" {
		t.Errorf("\\0 应改写 \\u0000, got %v", p)
	}
}

// 正常 schema 不动。
func TestShapeLeavesWellFormedAlone(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "get_weather",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string", "description": "城市"},
				},
				"required": []any{"city"},
			},
		},
	}})
	if !reflect.DeepEqual(params, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string", "description": "城市"},
		},
		"required": []any{"city"},
	}) {
		b, _ := json.Marshal(params)
		t.Errorf("合法 schema 应原样保留, got %s", b)
	}
}
