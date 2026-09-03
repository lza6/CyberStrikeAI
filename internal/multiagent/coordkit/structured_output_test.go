package coordkit

import (
	"strings"
	"testing"
)

func TestExtractJSON_Direct(t *testing.T) {
	v, err := extractJSON(`{"a":1,"b":"x"}`)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %T", v)
	}
	if obj["a"] != float64(1) {
		t.Errorf("a = %v, want 1", obj["a"])
	}
	if obj["b"] != "x" {
		t.Errorf("b = %v, want x", obj["b"])
	}
}

func TestExtractJSON_JSONFence(t *testing.T) {
	raw := "Here is the plan:\n```json\n[{\"title\":\"A\",\"description\":\"do A\"}]\n```\nDone."
	v, err := extractJSON(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	arr, ok := v.([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("expected 1-element array, got %T %v", v, v)
	}
}

func TestExtractJSON_BareFence(t *testing.T) {
	raw := "```\n{\"k\": 2}\n```"
	v, err := extractJSON(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if v.(map[string]any)["k"] != float64(2) {
		t.Errorf("k mismatch")
	}
}

func TestExtractJSON_BareObjectInProse(t *testing.T) {
	raw := "The answer is {\"result\": \"ok\"} as shown."
	v, err := extractJSON(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if v.(map[string]any)["result"] != "ok" {
		t.Errorf("result mismatch")
	}
}

func TestExtractJSON_BareArrayInProse(t *testing.T) {
	raw := "prefix [1, 2, 3] suffix"
	v, err := extractJSON(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	arr := v.([]any)
	if len(arr) != 3 {
		t.Errorf("len = %d, want 3", len(arr))
	}
}

func TestExtractJSON_Empty(t *testing.T) {
	if _, err := extractJSON(""); err == nil {
		t.Error("expected error for empty input")
	}
	if _, err := extractJSON("   "); err == nil {
		t.Error("expected error for whitespace input")
	}
}

func TestExtractJSON_NoJSON(t *testing.T) {
	if _, err := extractJSON("just prose, no json here"); err == nil {
		t.Error("expected ErrExtractJSON for pure prose")
	}
}

func TestValidate_ObjectRequiredMissing(t *testing.T) {
	schema := SchemaField{
		Type: "object",
		Fields: []SchemaField{
			{Name: "title", Type: "string", Required: true},
			{Name: "desc", Type: "string", Required: true},
		},
	}
	err := Validate(schema, map[string]any{"title": "T"})
	if err == nil {
		t.Fatal("expected validation error for missing desc")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if !strings.Contains(ve.Path, "desc") {
		t.Errorf("path should mention desc, got %q", ve.Path)
	}
}

func TestValidate_ArrayOfObjects(t *testing.T) {
	item := SchemaField{
		Type: "object",
		Fields: []SchemaField{
			{Name: "title", Type: "string", Required: true},
			{Name: "description", Type: "string", Required: true},
		},
	}
	schema := SchemaField{Type: "array", Fields: []SchemaField{item}}
	good := []any{
		map[string]any{"title": "A", "description": "do A"},
		map[string]any{"title": "B", "description": "do B"},
	}
	if err := Validate(schema, good); err != nil {
		t.Fatalf("good array should validate: %v", err)
	}
	bad := []any{
		map[string]any{"title": "A", "description": "do A"},
		map[string]any{"title": "B"}, // missing description
	}
	if err := Validate(schema, bad); err == nil {
		t.Fatal("bad array should fail validation")
	}
}

func TestValidate_TypeMismatch(t *testing.T) {
	schema := SchemaField{Type: "string"}
	if err := Validate(schema, 42); err == nil {
		t.Error("expected type mismatch error")
	}
	if err := Validate(schema, false); err == nil {
		t.Error("expected type mismatch error for bool")
	}
}

func TestExtractAndValidate_HappyPath(t *testing.T) {
	schema := SchemaField{
		Type: "array",
		Fields: []SchemaField{{
			Type: "object",
			Fields: []SchemaField{
				{Name: "title", Type: "string", Required: true},
				{Name: "description", Type: "string", Required: true},
			},
		}},
	}
	raw := "```json\n[{\"title\":\"A\",\"description\":\"x\"}]\n```"
	v, err := ExtractAndValidate(raw, schema)
	if err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
	arr, ok := v.([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("expected 1-element array, got %T", v)
	}
}

func TestExtractAndValidate_ValidationFailure(t *testing.T) {
	schema := SchemaField{
		Type: "array",
		Fields: []SchemaField{{
			Type: "object",
			Fields: []SchemaField{
				{Name: "title", Type: "string", Required: true},
				{Name: "description", Type: "string", Required: true},
			},
		}},
	}
	// missing description -> validation error
	raw := "```json\n[{\"title\":\"A\"}]\n```"
	if _, err := ExtractAndValidate(raw, schema); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestFormatValidationIssues(t *testing.T) {
	ve := &ValidationError{Path: "[0].title", Message: "missing required field"}
	out := FormatValidationIssues(ve)
	if !strings.Contains(out, "[0].title") || !strings.Contains(out, "missing required field") {
		t.Errorf("unexpected format: %q", out)
	}
	if out := FormatValidationIssues(ErrExtractJSON); !strings.Contains(out, "root") {
		t.Errorf("root label missing: %q", out)
	}
}

func TestMakeStructuredOutputInstruction(t *testing.T) {
	schema := SchemaField{
		Type: "array",
		Fields: []SchemaField{{
			Name: "task", Type: "object",
			Fields: []SchemaField{
				{Name: "title", Type: "string", Required: true},
			},
		}},
	}
	s := MakeStructuredOutputInstruction(schema)
	if !strings.Contains(s, "ONLY valid JSON") {
		t.Error("missing JSON directive")
	}
	if !strings.Contains(s, "title") {
		t.Error("schema not rendered")
	}
}
