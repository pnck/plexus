package effector

import (
	"encoding/json"
	"reflect"
	"testing"
)

// schemaOf decodes an effector's generated JSON schema into a map for inspection.
func schemaOf(t *testing.T, e Effector) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(e.Schema(), &m); err != nil {
		t.Fatalf("%s schema is not valid JSON: %v", e.Name(), err)
	}
	return m
}

func TestSchemaRequiredAndOptional(t *testing.T) {
	s := schemaOf(t, WriteFile())
	if s["type"] != "object" || s["additionalProperties"] != false {
		t.Fatalf("write_file schema base = %v", s)
	}
	// path & content are required (no omitempty); mode is optional with an enum.
	req := toStringSet(s["required"])
	if !req["path"] || !req["content"] {
		t.Fatalf("write_file required = %v, want path+content", s["required"])
	}
	if req["mode"] {
		t.Fatalf("mode should be optional (omitempty), got required: %v", s["required"])
	}
	props := s["properties"].(map[string]any)
	mode := props["mode"].(map[string]any)
	if !reflect.DeepEqual(mode["enum"], []any{"overwrite", "append"}) {
		t.Fatalf("mode enum = %v, want [overwrite append]", mode["enum"])
	}
	if mode["type"] != "string" {
		t.Fatalf("mode type = %v, want string", mode["type"])
	}
}

func TestSchemaSliceBecomesArray(t *testing.T) {
	s := schemaOf(t, RunCommand())
	props := s["properties"].(map[string]any)
	args := props["args"].(map[string]any)
	if args["type"] != "array" {
		t.Fatalf("run_command args type = %v, want array", args["type"])
	}
	items := args["items"].(map[string]any)
	if items["type"] != "string" {
		t.Fatalf("run_command args items = %v, want string", items)
	}
	// command is required; args (omitempty) is not.
	req := toStringSet(s["required"])
	if !req["command"] || req["args"] {
		t.Fatalf("run_command required = %v, want only command", s["required"])
	}
}

func TestSchemaBoolAndNoArgs(t *testing.T) {
	// edit_file.replace_all is a boolean, optional.
	props := schemaOf(t, EditFile())["properties"].(map[string]any)
	if props["replace_all"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("replace_all type = %v, want boolean", props["replace_all"])
	}
	// now takes no arguments: properties is an empty object.
	s := schemaOf(t, Now())
	if p, ok := s["properties"].(map[string]any); !ok || len(p) != 0 {
		t.Fatalf("now properties = %v, want empty object", s["properties"])
	}
	if _, hasReq := s["required"]; hasReq {
		t.Fatalf("now should have no required list, got %v", s["required"])
	}
}

func toStringSet(v any) map[string]bool {
	out := map[string]bool{}
	if list, ok := v.([]any); ok {
		for _, e := range list {
			if s, ok := e.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}
