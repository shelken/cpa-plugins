package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// shapeTools 后取出第一个工具的 parameters (无工具时 t.Fatal)。
func firstParams(t *testing.T, tools []any) map[string]any {
	t.Helper()
	shapeTools(tools)
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatal("tool 不是 map")
	}
	fn, ok := tool["function"].(map[string]any)
	if !ok {
		t.Fatal("function 不存在")
	}
	params, _ := fn["parameters"].(map[string]any)
	return params
}

// 无类型 enum 补类型 (magpie fitEnum; kimi 实测 11133 → 200)。
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

// 混合类型 enum 拆 anyOf, integer 并入 number, null 拆出 (magpie fitEnum; kimi 实测 11133)。
func TestShapeMixedTypeEnumSplit(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "pick",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"v": map[string]any{"enum": []any{float64(1), "a"}}},
			},
		},
	}})
	v := params["properties"].(map[string]any)["v"].(map[string]any)
	if _, has := v["enum"]; has {
		t.Errorf("混合类型 enum 应删除原 enum 字段")
	}
	anyOf, ok := v["anyOf"].([]any)
	if !ok || len(anyOf) != 2 {
		t.Fatalf("混合类型 enum 应拆 anyOf 两支, got %v", v)
	}
	first := anyOf[0].(map[string]any)
	if first["type"] != "integer" || !reflect.DeepEqual(first["enum"], []any{float64(1)}) {
		t.Errorf("第一支应为 {type:integer, enum:[1]}, got %v", first)
	}
	second := anyOf[1].(map[string]any)
	if second["type"] != "string" || !reflect.DeepEqual(second["enum"], []any{"a"}) {
		t.Errorf("第二支应为 {type:string, enum:[\"a\"]}, got %v", second)
	}
}

// 含 null 的单类型 enum → type 列表 [T, "null"] (magpie fitEnum; kimi 只拒两个非 null 类型)。
func TestShapeEnumWithNullGetsTypeList(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "pick",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"v": map[string]any{"enum": []any{float64(1), nil}}},
			},
		},
	}})
	v := params["properties"].(map[string]any)["v"].(map[string]any)
	want := []any{"integer", "null"}
	if !reflect.DeepEqual(v["type"], want) {
		t.Errorf("含 null 的单类型 enum 应得 type=[integer,null], got %v", v["type"])
	}
	if _, has := v["anyOf"]; has {
		t.Errorf("不应拆 anyOf")
	}
}

// 空 enum → 删 enum 字段, 位置/分支保留为空 schema (magpie fitEnum), 无哨兵字段泄漏。
func TestShapeEmptyEnumDeleted(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "f3",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"b": map[string]any{"enum": []any{}},
					"u": map[string]any{"anyOf": []any{map[string]any{"enum": []any{}}, map[string]any{"type": "string"}}},
				},
			},
		},
	}})
	props := params["properties"].(map[string]any)
	b, has := props["b"].(map[string]any)
	if !has {
		t.Fatalf("空 enum 的位置应保留为空 schema, got %v", props["b"])
	}
	if len(b) != 0 {
		t.Errorf("b 应为空 schema, got %v", b)
	}
	u := props["u"].(map[string]any)
	anyOf := u["anyOf"].([]any)
	if len(anyOf) != 2 {
		t.Fatalf("分支数不变, got %v", anyOf)
	}
	if first := anyOf[0].(map[string]any); len(first) != 0 {
		t.Errorf("空 enum 支应变空 schema, got %v", first)
	}
	// 序列化后不得有哨兵字段
	out, _ := json.Marshal(params)
	if strings.Contains(string(out), "__emptyEnum") {
		t.Errorf("哨兵字段不得出现在序列化结果: %s", out)
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

// oneOf 分支经本地 $ref 指向 object 定义同样补 type=object (magpie objectBranch)。
func TestShapeRefObjectOneOf(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "make",
			"parameters": map[string]any{
				"oneOf": []any{
					map[string]any{"$ref": "#/$defs/a"},
					map[string]any{"type": "object", "properties": map[string]any{"y": map[string]any{"type": "string"}}},
				},
				"$defs": map[string]any{
					"a": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "number"}}},
				},
			},
		},
	}})
	if params["type"] != "object" {
		t.Errorf("$ref 指向 object 的 oneOf 应补 type=object, got %v", params["type"])
	}
}

// bool 当 schema 只在 schema 位置替换 (magpie subSchema); uniqueItems 等布尔关键字保留。
func TestShapeBoolSchemaOnlyAtSchemaPositions(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "f2",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": true, "b": false},
				"required":   []any{"a"},
			},
		},
	}})
	props := params["properties"].(map[string]any)
	a, ok := props["a"].(map[string]any)
	if !ok || len(a) != 0 {
		t.Errorf("properties.a 的 true 应替换为 {}, got %v", props["a"])
	}
	b, ok := props["b"].(map[string]any)
	if !ok {
		t.Fatalf("properties.b 的 false 应替换为 map, got %v", props["b"])
	}
	not, ok := b["not"].(map[string]any)
	if !ok || len(not) != 0 {
		t.Errorf("false 应替换为 {\"not\": {}}, got %v", b)
	}
	arr := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "f2b",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"xs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "uniqueItems": true},
				},
			},
		},
	}})
	xs := arr["properties"].(map[string]any)["xs"].(map[string]any)
	if xs["uniqueItems"] != true {
		t.Errorf("uniqueItems: true 是合法布尔关键字, 必须原样保留, got %v", xs["uniqueItems"])
	}
	items, ok := xs["items"].(map[string]any)
	if !ok || items["type"] != "string" {
		t.Errorf("items schema 不应被破坏, got %v", xs["items"])
	}
}

// required: 非列表丢弃; 列表内非字符串过滤 (magpie fitShape)。
func TestShapeRequiredForms(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "f3",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "string", "required": "x"},
					"b": map[string]any{"type": "number", "required": []any{"q", float64(1)}},
				},
			},
		},
	}})
	props := params["properties"].(map[string]any)
	a := props["a"].(map[string]any)
	if _, has := a["required"]; has {
		t.Errorf("required 非列表应丢弃")
	}
	b := props["b"].(map[string]any)
	if !reflect.DeepEqual(b["required"], []any{"q"}) {
		t.Errorf("required 列表内非字符串应过滤, got %v", b["required"])
	}
}

// type 归一: 小写化保留; 多类型列表保留 (除 enum 推导外不拆 anyOf, magpie fitShape)。
func TestShapeTypeForms(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "f4",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "String"},
					"b": map[string]any{"type": []any{"string", "null"}},
					"c": map[string]any{"type": "float"},
				},
			},
		},
	}})
	props := params["properties"].(map[string]any)
	if props["a"].(map[string]any)["type"] != "string" {
		t.Errorf("type 应小写化保留, got %v", props["a"].(map[string]any)["type"])
	}
	if !reflect.DeepEqual(props["b"].(map[string]any)["type"], []any{"string", "null"}) {
		t.Errorf("type 列表应原样保留, got %v", props["b"].(map[string]any)["type"])
	}
	if _, has := props["c"].(map[string]any)["type"]; has {
		t.Errorf("未知类型应丢弃")
	}
}

// 元组 items → anyOf 或单 schema, additionalItems 删除 (magpie fitSchema)。
func TestShapeTupleItems(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "pair",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"p": map[string]any{
						"type":            "array",
						"items":           []any{map[string]any{"type": "string"}, map[string]any{"type": "number"}},
						"additionalItems": false,
					},
					"q": map[string]any{"type": "array", "items": []any{map[string]any{"type": "string"}}},
				},
			},
		},
	}})
	props := params["properties"].(map[string]any)
	p := props["p"].(map[string]any)
	items, ok := p["items"].(map[string]any)
	if !ok {
		t.Fatalf("元组 items 应改为 schema, got %v", p["items"])
	}
	anyOf, ok := items["anyOf"].([]any)
	if !ok || len(anyOf) != 2 {
		t.Fatalf("多支元组应变 anyOf, got %v", items)
	}
	if _, has := p["additionalItems"]; has {
		t.Errorf("additionalItems 应删除")
	}
	q := props["q"].(map[string]any)
	if q["items"] != "single" {
		if m, isMap := q["items"].(map[string]any); !isMap || m["type"] != "string" {
			t.Errorf("单支元组应变单个 schema, got %v", q["items"])
		}
	}
}

// pattern: \0 改写 \u0000; \01 八进制转义保留 (magpie NUL_ESCAPE)。
func TestShapePatternNul(t *testing.T) {
	params := firstParams(t, []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "find_path",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"p": map[string]any{"type": "string", "pattern": "^a\\0b$"},
					"o": map[string]any{"type": "string", "pattern": "^a\\01b$"},
				},
			},
		},
	}})
	props := params["properties"].(map[string]any)
	p := props["p"].(map[string]any)["pattern"]
	if p != "^a\\u0000b$" {
		t.Errorf("\\0 应改写 \\u0000, got %v", p)
	}
	o := props["o"].(map[string]any)["pattern"]
	if o != "^a\\01b$" {
		t.Errorf("\\01 是八进制转义, 应保留, got %v", o)
	}
}

// 工具名非法字符换 _ 并去重 (magpie safeName; kimi 实测 11152), renames 给出 safe→原名。
func TestShapeToolName(t *testing.T) {
	shaped := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "mcp.server.tool", "parameters": map[string]any{"type": "object"}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "mcp:server:tool", "parameters": map[string]any{"type": "object"}}},
	}
	renames := shapeTools(shaped)
	n1 := shaped[0].(map[string]any)["function"].(map[string]any)["name"]
	n2 := shaped[1].(map[string]any)["function"].(map[string]any)["name"]
	if n1 != "mcp_server_tool" {
		t.Errorf("点应换 _, got %v", n1)
	}
	if n2 != "mcp_server_tool_2" {
		t.Errorf("重名应加序号, got %v", n2)
	}
	if renames["mcp_server_tool"] != "mcp.server.tool" || renames["mcp_server_tool_2"] != "mcp:server:tool" {
		t.Errorf("renames 应给出 safe→original 映射, got %v", renames)
	}
}

// 首尾下划线是合法字符, 不裁剪 (magpie safeName 无 Trim); 合法名原样保留 (合法
// 重名上游收, magpie 只对非法名改名)。
func TestShapeNameNotTrimmed(t *testing.T) {
	shaped := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "_foo_"}},
		map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
		map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
	}
	renames := shapeTools(shaped)
	n1 := shaped[0].(map[string]any)["function"].(map[string]any)["name"]
	if n1 != "_foo_" {
		t.Errorf("合法名 _foo_ 不应改动, got %v", n1)
	}
	n2 := shaped[1].(map[string]any)["function"].(map[string]any)["name"]
	n3 := shaped[2].(map[string]any)["function"].(map[string]any)["name"]
	if n2 != "get_weather" || n3 != "get_weather" {
		t.Errorf("合法名原样保留, got %v %v", n2, n3)
	}
	if len(renames) != 0 {
		t.Errorf("合法名不进 renames, got %v", renames)
	}
}

// parameters 缺失跳过; null/非对象替换为空 object (magpie fitTools)。
func TestShapeParametersForms(t *testing.T) {
	shaped := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "no_params"}},
		map[string]any{"type": "function", "function": map[string]any{"name": "null_params", "parameters": nil}},
	}
	shapeTools(shaped)
	fn1, ok := shaped[0].(map[string]any)["function"].(map[string]any)
	if !ok {
		t.Fatal("fn1 形态不对")
	}
	if _, has := fn1["parameters"]; has {
		t.Errorf("缺失 parameters 不应补建, got %v", fn1)
	}
	fn2, ok := shaped[1].(map[string]any)["function"].(map[string]any)
	if !ok {
		t.Fatal("fn2 形态不对")
	}
	p2, ok := fn2["parameters"].(map[string]any)
	if !ok || p2["type"] != "object" {
		t.Errorf("null parameters 应替换为 {type:object,properties:{}}, got %v", fn2["parameters"])
	}
}

// 历史 tool_calls、tool 消息 name、tool_choice 一并改名 (magpie fitTools)。
func TestShapeHistoryAndToolChoiceRenamed(t *testing.T) {
	orig2safe := map[string]string{"a.b": "a_b"}
	messages := []chatMessage{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []toolCall{{ID: "c1", Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "a.b", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "c1", Name: "a.b", Content: "ok"},
	}
	applyHistoryRenames(messages, orig2safe)
	if messages[1].ToolCalls[0].Function.Name != "a_b" {
		t.Errorf("历史 tool_calls 应改名, got %v", messages[1].ToolCalls[0].Function.Name)
	}
	if messages[2].Name != "a_b" {
		t.Errorf("tool 消息 name 应改名, got %v", messages[2].Name)
	}

	toolChoice := any(map[string]any{
		"type":     "function",
		"function": map[string]any{"name": "a.b"},
	})
	renameToolChoice(toolChoice, orig2safe)
	if toolChoice.(map[string]any)["function"].(map[string]any)["name"] != "a_b" {
		t.Errorf("tool_choice 应改名, got %v", toolChoice.(map[string]any)["function"])
	}
}

// 响应侧回映射: safe 名换回客户端原名 (magpie namedBack)。
func TestShapeRenameBackResponse(t *testing.T) {
	renames := map[string]string{"a_b": "a.b"}
	resp := map[string]any{
		"id":     "x",
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index": float64(0),
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id":       "c1",
					"type":     "function",
					"function": map[string]any{"name": "a_b", "arguments": "{}"},
				}},
			},
		}},
	}
	raw, _ := json.Marshal(resp)
	out := renameBackChunk(raw, renames)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	choice := got["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	tc := msg["tool_calls"].([]any)[0].(map[string]any)
	if tc["function"].(map[string]any)["name"] != "a.b" {
		t.Errorf("响应里应回映射原名 a.b, got %v", tc["function"].(map[string]any)["name"])
	}
	if got["id"] != "x" || got["object"] != "chat.completion" {
		t.Errorf("其余字段不得丢失, got %v", got)
	}
}

// 无改名时响应帧原样透传 (零开销路径)。
func TestShapeRenameBackNoop(t *testing.T) {
	raw := []byte(`{"id":"x","choices":[{"delta":{"content":"hi"}}]}`)
	out := renameBackChunk(raw, map[string]string{})
	if string(out) != string(raw) {
		t.Errorf("无改名时帧应原样返回, got %s", out)
	}
	out = renameBackChunk(raw, map[string]string{"a_b": "a.b"})
	if string(out) != string(raw) {
		t.Errorf("帧内无改名工具名应原样返回, got %s", out)
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
