package core

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSchemaFromStruct(t *testing.T) {
	type Nested struct {
		Enabled bool `json:"enabled" jsonschema:"Whether it is enabled"`
	}
	type Args struct {
		Query    string            `json:"query" jsonschema:"The search query"`
		Limit    int               `json:"limit,omitempty" jsonschema:"Max results"`
		Score    float64           `json:"score,omitempty"`
		Tags     []string          `json:"tags,omitempty"`
		Headers  map[string]string `json:"headers,omitempty"`
		Nested   *Nested           `json:"nested,omitempty"`
		Mode     string            `json:"mode,omitempty" enum:"fast,careful"`
		Ignored  string            `json:"-"`
	}
	s, err := SchemaFromStruct(Args{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Type != "object" {
		t.Fatalf("expected object type, got %s", s.Type)
	}
	if s.Properties["query"] == nil || s.Properties["query"].Type != "string" {
		t.Fatalf("query prop missing or wrong type: %+v", s.Properties["query"])
	}
	if s.Properties["limit"] == nil || s.Properties["limit"].Type != "integer" {
		t.Fatalf("limit prop wrong: %+v", s.Properties["limit"])
	}
	if s.Properties["tags"] == nil || s.Properties["tags"].Type != "array" || s.Properties["tags"].Items == nil || s.Properties["tags"].Items.Type != "string" {
		t.Fatalf("tags prop wrong: %+v", s.Properties["tags"])
	}
	if s.Properties["mode"] == nil || len(s.Properties["mode"].Enum) != 2 {
		t.Fatalf("mode enum wrong: %+v", s.Properties["mode"])
	}
	if s.Properties["nested"] == nil || s.Properties["nested"].Properties["enabled"] == nil {
		t.Fatalf("nested struct prop missing")
	}
	if _, ok := s.Properties["Ignored"]; ok {
		t.Fatal("json:\"-\" field must be excluded")
	}
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	if !required["query"] {
		t.Fatal("query must be required")
	}
	if required["limit"] {
		t.Fatal("limit must be optional (omitempty)")
	}
}

func TestSchemaJSONValid(t *testing.T) {
	type Args struct {
		URL string `json:"url"`
	}
	s, err := SchemaFromStruct(Args{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.SchemaJSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("schema must be valid JSON: %v", err)
	}
}

func TestEvalExpression(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"2 + 3", 5},
		{"(2 + 3) * 4", 20},
		{"10 / 4", 2.5},
		{"2 ^ 10", 1024},
		{"-5 + 3", -2},
		{"sqrt(16)", 4},
		{"max(1, 5, 3)", 5},
		{"min(1, 5, 3)", 1},
		{"abs(-7)", 7},
		{"round(2.5)", 3},
		{"10 % 3", 1},
		{"pow(2, 3) + 1", 9},
	}
	for _, c := range cases {
		got, err := EvalExpression(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%s = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestEvalExpressionErrors(t *testing.T) {
	bad := []string{"", "2 +", "(2+3", "1/0", "10 % 0", "sqrt(-1)", "foo(1)", "2 ++ 3", "unknown_fn(1)"}
	for _, in := range bad {
		if _, err := EvalExpression(in); err == nil {
			t.Fatalf("%q must fail", in)
		}
	}
}

func TestEvalExpressionNoCodeExecution(t *testing.T) {
	// The evaluator must reject anything that is not arithmetic.
	for _, in := range []string{"exec('x')", "os.system('dir')", "process.exit()", "true && false", "x = 1"} {
		if _, err := EvalExpression(in); err == nil {
			t.Fatalf("%q must be rejected", in)
		}
	}
}

func TestToolSchemaAndValidation(t *testing.T) {
	type Args struct {
		A int `json:"a"`
		B int `json:"b"`
	}
	add := MustTool[Args]("add", "add two numbers", func(ctx context.Context, tc *ToolContext, args Args) (any, error) {
		return map[string]int{"sum": args.A + args.B}, nil
	})
	// Schema is valid JSON with both fields required.
	var schema struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(add.Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" {
		t.Fatalf("schema type = %s", schema.Type)
	}
	if len(schema.Required) != 2 || schema.Properties["a"].Type != "integer" || schema.Properties["b"].Type != "integer" {
		t.Fatalf("unexpected schema: %s", add.Parameters)
	}
	// Execution decodes + validates arguments.
	out, err := add.Run(context.Background(), NewToolContext("t", "r"), json.RawMessage(`{"a":2,"b":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := out.(map[string]int); !ok || m["sum"] != 5 {
		t.Fatalf("add(2,3) = %v", out)
	}
	// Invalid JSON arguments fail cleanly.
	if _, err := add.Run(context.Background(), NewToolContext("t", "r"), json.RawMessage(`not json`)); err == nil {
		t.Fatal("invalid args must fail")
	}
	// Built-in tools exist and are nameable.
	idx := ToolsByName(BuiltinTools)
	for _, name := range []string{"http_fetch", "calculator", "now"} {
		if idx[name] == nil {
			t.Fatalf("builtin tool %s missing", name)
		}
	}
}
