package eval

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToolResultExpectation checks one tool execution observed during the
// run: that it failed (or succeeded) and, when it succeeded, that its
// JSON payload has the expected shape. Shape checks catch the "silent
// 200 OK" failure mode — a tool returns an empty array or a bare null
// and the agent confidently builds on the void.
type ToolResultExpectation struct {
	Name string `json:"name"`
	// ErrorContains, when set, asserts the tool FAILED with an error
	// containing this substring. Empty means "must have succeeded".
	ErrorContains string `json:"errorContains,omitempty"`
	// Shape validates the tool's JSON payload when set. A shape check
	// on a failed tool is a failure (nothing to validate).
	Shape *ShapeSpec `json:"shape,omitempty"`
}

// ShapeSpec is the structural contract of a tool result payload.
// Only the fields that are set are checked; anything else passes.
type ShapeSpec struct {
	// RequiredFields: the payload must be an object containing these
	// keys. Catches tools that silently drop fields.
	RequiredFields []string `json:"requiredFields,omitempty"`
	// FieldTypes: JSON type per field — "string" | "number" | "int" |
	// "bool" | "array" | "object". "int" is a number without a
	// fractional part.
	FieldTypes map[string]string `json:"fieldTypes,omitempty"`
	// MinItems: payload must be an array with at least this many
	// items. The empty-array-200-OK catcher.
	MinItems int `json:"minItems,omitempty"`
	// MinLength: payload must be a string of at least this many
	// characters. The empty-string-200-OK catcher.
	MinLength int `json:"minLength,omitempty"`
}

// checkShape validates a tool result payload against the spec and
// returns human-readable failures (empty when it passes).
func checkShape(content json.RawMessage, spec *ShapeSpec) []string {
	if spec == nil {
		return nil
	}
	var out []string
	value, wrapped := decodeContent(content)
	if value == nil {
		if wrapped != "" {
			out = append(out, "result is not valid JSON: "+wrapped)
		} else {
			out = append(out, "result is empty")
		}
		return out
	}
	if len(spec.RequiredFields) > 0 {
		obj, ok := value.(map[string]any)
		if !ok {
			out = append(out, fmt.Sprintf("expected object with %v, got %s", spec.RequiredFields, typeName(value)))
		} else {
			for _, f := range spec.RequiredFields {
				if _, ok := obj[f]; !ok {
					out = append(out, fmt.Sprintf("missing required field %q", f))
				}
			}
		}
	}
	for f, want := range spec.FieldTypes {
		obj, ok := value.(map[string]any)
		if !ok {
			out = append(out, fmt.Sprintf("field %q: expected %s, but result is %s", f, want, typeName(value)))
			break
		}
		got, ok := obj[f]
		if !ok {
			out = append(out, fmt.Sprintf("field %q missing (expected %s)", f, want))
			continue
		}
		if !typeMatches(got, want) {
			out = append(out, fmt.Sprintf("field %q: expected %s, got %s", f, want, typeName(got)))
		}
	}
	if spec.MinItems > 0 {
		arr, ok := value.([]any)
		if !ok {
			out = append(out, fmt.Sprintf("expected array with >= %d items, got %s", spec.MinItems, typeName(value)))
		} else if len(arr) < spec.MinItems {
			out = append(out, fmt.Sprintf("array has %d items, want >= %d", len(arr), spec.MinItems))
		}
	}
	if spec.MinLength > 0 {
		s, ok := value.(string)
		if !ok {
			out = append(out, fmt.Sprintf("expected string of length >= %d, got %s", spec.MinLength, typeName(value)))
		} else if len(s) < spec.MinLength {
			out = append(out, fmt.Sprintf("string has length %d, want >= %d", len(s), spec.MinLength))
		}
	}
	return out
}

// decodeContent decodes a tool payload. Tools sometimes double-encode
// (a JSON string containing JSON): unwrap one level so shapes see the
// real structure. Returns the decoded value and, on failure, an error
// description.
func decodeContent(content json.RawMessage) (any, string) {
	if len(content) == 0 || strings.TrimSpace(string(content)) == "" {
		return nil, ""
	}
	var v any
	if err := json.Unmarshal(content, &v); err != nil {
		return nil, err.Error()
	}
	if s, ok := v.(string); ok && strings.HasPrefix(strings.TrimSpace(s), "{") {
		var inner any
		if err := json.Unmarshal([]byte(s), &inner); err == nil {
			return inner, ""
		}
	}
	return v, ""
}

// typeName describes a decoded JSON value by its JSON type.
func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "bool"
	case float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// typeMatches reports whether the decoded value matches the wanted
// shape type name ("string" | "number" | "int" | "bool" | "array" |
// "object").
func typeMatches(v any, want string) bool {
	switch want {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "int":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	case "bool":
		_, ok := v.(bool)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	}
	return false
}
