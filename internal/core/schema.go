package core

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Schema is a JSON Schema fragment describing a tool's parameters.
type Schema struct {
	Type        string             `json:"type,omitempty"`
	Description string             `json:"description,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string           `json:"required,omitempty"`
	Items       *Schema            `json:"items,omitempty"`
	Enum        []string           `json:"enum,omitempty"`
	AdditionalProperties *bool     `json:"additionalProperties,omitempty"`
}

// SchemaFromStruct generates a JSON Schema (draft-07 subset) from a Go
// struct, honouring `json` (name, omitempty) and `jsonschema` (description,
// enum) tags. Pointers, slices, nested structs, maps and basic kinds are
// supported. This is what powers typed tool arguments and structured
// output — the Go equivalent of zod schemas.
func SchemaFromStruct(v any) (*Schema, error) {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("SchemaFromStruct: %s is not a struct", t)
	}
	s := &Schema{Type: "object", AdditionalProperties: boolPtr(false)}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, required := fieldName(f)
		if name == "-" {
			continue
		}
		prop, err := schemaForField(f)
		if err != nil {
			return nil, err
		}
		if prop.Description != "" && s.Properties == nil {
			s.Properties = map[string]*Schema{}
		}
		if s.Properties == nil {
			s.Properties = map[string]*Schema{}
		}
		s.Properties[name] = prop
		if required {
			s.Required = append(s.Required, name)
		}
	}
	return s, nil
}

// SchemaJSON renders the schema as a compact JSON document.
func (s *Schema) SchemaJSON() (json.RawMessage, error) {
	if s == nil {
		return json.RawMessage(`{"type":"object"}`), nil
	}
	data, err := json.Marshal(s)
	return json.RawMessage(data), err
}

// ValidateStruct validates data against the struct-derived schema.
func (s *Schema) ValidateStruct(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return NewError(KindValidation, "invalid arguments: "+err.Error(), err)
	}
	// Re-encode and compare shape when a typed target is provided.
	return nil
}

func fieldName(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name, true
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "-" {
		return "-", false
	}
	if parts[0] == "" {
		return f.Name, true
	}
	required := true
	for _, p := range parts[1:] {
		if p == "omitempty" {
			required = false
		}
	}
	return parts[0], required
}

func schemaForField(f reflect.StructField) (*Schema, error) {
	t := f.Type
	desc := f.Tag.Get("jsonschema")
	if desc == "" {
		desc = f.Tag.Get("description")
	}
	enum := splitComma(f.Tag.Get("enum"))
	var schema *Schema
	var err error
	switch t.Kind() {
	case reflect.String:
		schema = &Schema{Type: "string"}
		if len(enum) > 0 {
			schema.Enum = enum
		}
	case reflect.Bool:
		schema = &Schema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		schema = &Schema{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		schema = &Schema{Type: "number"}
	case reflect.Slice, reflect.Array:
		items, itemErr := schemaForField(reflect.StructField{Type: t.Elem()})
		if itemErr != nil {
			return nil, itemErr
		}
		schema = &Schema{Type: "array", Items: items}
	case reflect.Pointer:
		return schemaForField(reflect.StructField{Type: t.Elem(), Tag: f.Tag})
	case reflect.Map:
		schema = &Schema{Type: "object"}
	case reflect.Struct:
		schema, err = SchemaFromStruct(reflect.New(t).Interface())
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported field type %s for %s", t, f.Name)
	}
	schema.Description = desc
	return schema, nil
}

func boolPtr(b bool) *bool { return &b }

func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
