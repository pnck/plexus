// Package jsonschema derives a JSON Schema object from a Go struct type by
// reflection, so an LLM tool's arguments can be declared once — as a Go type
// with struct tags — instead of as a hand-written schema literal. Both the
// effector layer (built-in primitives) and the brain (the delegate tool)
// describe their tool arguments this way.
package jsonschema

import (
	"encoding/json"
	"reflect"
	"strings"
)

// For builds a JSON Schema object for a flat argument struct T from its struct
// tags:
//
//   - `json:"name,omitempty"` sets the property name and whether it is required
//     (omitempty => optional); `json:"-"` skips the field.
//   - `desc:"..."` adds a description; `enum:"a,b"` adds an enum.
//   - slices become array schemas; the scalar kinds map to
//     string / integer / number / boolean.
//
// encoding/json sorts map keys, so the output is deterministic — safe for
// prompt-prefix caching. The schema is computed by reflection once, at the call
// site (typically tool construction), not on every invocation.
func For[T any]() json.RawMessage {
	t := reflect.TypeOf((*T)(nil)).Elem()
	props := map[string]any{}
	var required []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, optional, ok := jsonField(f)
		if !ok {
			continue
		}
		p := prop(f.Type)
		if d := f.Tag.Get("desc"); d != "" {
			p["description"] = d
		}
		if e := f.Tag.Get("enum"); e != "" {
			p["enum"] = strings.Split(e, ",")
		}
		props[name] = p
		if !optional {
			required = append(required, name)
		}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	out, err := json.Marshal(schema)
	if err != nil {
		// Argument shapes are static, so a failure here is a programming error.
		panic("jsonschema: marshal: " + err.Error())
	}
	return out
}

// prop renders the type schema for one field: an array schema for slices, a
// scalar schema otherwise.
func prop(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Slice {
		return map[string]any{"type": "array", "items": map[string]any{"type": scalar(t.Elem().Kind())}}
	}
	return map[string]any{"type": scalar(t.Kind())}
}

// jsonField returns a field's property name, whether it is optional (omitempty),
// and ok=false if the field is skipped (json:"-").
func jsonField(f reflect.StructField) (name string, optional, ok bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false, false
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = f.Name
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			optional = true
		}
	}
	return name, optional, true
}

// scalar maps a Go kind to its JSON Schema scalar type name.
func scalar(k reflect.Kind) string {
	switch k {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	default:
		return "string"
	}
}
