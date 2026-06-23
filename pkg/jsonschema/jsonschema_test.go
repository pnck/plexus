package jsonschema

import (
	"encoding/json"
	"reflect"
	"testing"
)

func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	return m
}

func required(s map[string]any) map[string]bool {
	out := map[string]bool{}
	if list, ok := s["required"].([]any); ok {
		for _, e := range list {
			out[e.(string)] = true
		}
	}
	return out
}

func TestForRequiredOptionalDescEnum(t *testing.T) {
	type args struct {
		Path string `json:"path"`
		Mode string `json:"mode,omitempty" desc:"How to write." enum:"overwrite,append"`
		Skip string `json:"-"`
	}
	s := decode(t, For[args]())

	if s["type"] != "object" || s["additionalProperties"] != false {
		t.Fatalf("base = %v", s)
	}
	props := s["properties"].(map[string]any)
	if _, ok := props["Skip"]; ok {
		t.Fatal(`json:"-" field leaked into schema`)
	}
	req := required(s)
	if !req["path"] || req["mode"] {
		t.Fatalf("required = %v, want only path", s["required"])
	}
	mode := props["mode"].(map[string]any)
	if mode["description"] != "How to write." {
		t.Fatalf("mode description = %v", mode["description"])
	}
	if !reflect.DeepEqual(mode["enum"], []any{"overwrite", "append"}) {
		t.Fatalf("mode enum = %v", mode["enum"])
	}
}

func TestForSliceAndScalarKinds(t *testing.T) {
	type args struct {
		Items []string `json:"items"`
		Count int      `json:"count"`
		Flag  bool     `json:"flag"`
		Ratio float64  `json:"ratio"`
	}
	props := decode(t, For[args]())["properties"].(map[string]any)

	items := props["items"].(map[string]any)
	if items["type"] != "array" || items["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("items = %v, want array of string", items)
	}
	if props["count"].(map[string]any)["type"] != "integer" {
		t.Fatalf("count type = %v", props["count"])
	}
	if props["flag"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("flag type = %v", props["flag"])
	}
	if props["ratio"].(map[string]any)["type"] != "number" {
		t.Fatalf("ratio type = %v", props["ratio"])
	}
}

func TestForNoFields(t *testing.T) {
	type none struct{}
	s := decode(t, For[none]())
	if p, ok := s["properties"].(map[string]any); !ok || len(p) != 0 {
		t.Fatalf("properties = %v, want empty object", s["properties"])
	}
	if _, ok := s["required"]; ok {
		t.Fatalf("no fields should yield no required list, got %v", s["required"])
	}
}

func TestForIsDeterministic(t *testing.T) {
	type args struct {
		B string `json:"b"`
		A string `json:"a"`
	}
	first := string(For[args]())
	second := string(For[args]())
	if first != second {
		t.Fatalf("schema output is not deterministic:\n%s\n%s", first, second)
	}
}
