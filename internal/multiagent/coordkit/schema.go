// Package coordkit
package coordkit

import (
	"errors"
	"strings"
)

// SchemaField describes a single field expected in the structured output.
//
// Migrated from open-multi-agent-main's Zod-based outputSchema. Go has no Zod;
// we define a minimal structural schema that covers the cases the coordinator
// (a JSON array of task specs) and structured worker outputs need: type,
// required, and (for objects) nested fields. This keeps validation explicit
// and avoids pulling a JSON-Schema validator dependency into the isolated
// coordkit package.
type SchemaField struct {
	Name     string
	Type     string // "string" | "array" | "object" | "bool" | "number" | "any"
	Required bool
	// Fields applies when Type == "object" or Type == "array" (item shape).
	Fields []SchemaField
}

// ValidationError describes why a parsed value failed to match the schema.
// It is intentionally human-readable so it can be fed back to the LLM as the
// retry prompt (mirroring structured-output.ts validateOutput's issue list).
type ValidationError struct {
	Path    string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Path == "" {
		return "validation error: " + e.Message
	}
	return "validation error at " + e.Path + ": " + e.Message
}

// Validate checks a parsed JSON value (any) against a structural schema. The
// schema is applied positionally to arrays (each element must match the item
// field) and by name to objects. Unknown object fields are ignored (tolerant,
// matching the reference project's lenient parse). Missing required fields
// produce a ValidationError. Returns nil when the value matches.
//
// For the top-level coordinator output, callers pass SchemaField{Type:"array",
// Fields: []SchemaField{{Name:"task", Type:"object", Fields: ...}}}.
func Validate(schema SchemaField, value any) error {
	return validateField(schema, value, "")
}

func validateField(field SchemaField, value any, path string) error {
	switch field.Type {
	case "any":
		return nil
	case "string":
		if _, ok := value.(string); !ok {
			return &ValidationError{Path: path, Message: "expected string, got " + typeName(value)}
		}
		return nil
	case "bool":
		if _, ok := value.(bool); !ok {
			return &ValidationError{Path: path, Message: "expected bool, got " + typeName(value)}
		}
		return nil
	case "number":
		switch value.(type) {
		case float64, int, int64, float32:
			return nil
		}
		return &ValidationError{Path: path, Message: "expected number, got " + typeName(value)}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			return &ValidationError{Path: path, Message: "expected array, got " + typeName(value)}
		}
		itemField := SchemaField{Type: "any"}
		if len(field.Fields) > 0 {
			itemField = field.Fields[0]
		}
		for i, v := range arr {
			ip := appendIndex(path, i)
			if err := validateField(itemField, v, ip); err != nil {
				return err
			}
		}
		return nil
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return &ValidationError{Path: path, Message: "expected object, got " + typeName(value)}
		}
		for _, f := range field.Fields {
			v, present := obj[f.Name]
			if !present {
				if f.Required {
					return &ValidationError{Path: joinPath(path, f.Name), Message: "missing required field"}
				}
				continue
			}
			if err := validateField(f, v, joinPath(path, f.Name)); err != nil {
				return err
			}
		}
		return nil
	default:
		return &ValidationError{Path: path, Message: "unsupported schema type " + field.Type}
	}
}

func joinPath(base, field string) string {
	if base == "" {
		return field
	}
	return base + "." + field
}

func appendIndex(base string, i int) string {
	if base == "" {
		return "[" + itoa(i) + "]"
	}
	return base + "[" + itoa(i) + "]"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "bool"
	case float64, float32:
		return "number"
	case int, int8, int16, int32, int64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

// ExtractAndValidate is the convenience wrapper used by the retry loop: it
// extracts JSON from raw LLM text and immediately validates it against the
// schema. On success it returns the parsed value; on failure it returns the
// underlying error (ErrExtractJSON or a *ValidationError) so the caller can
// format it into the retry feedback message.
func ExtractAndValidate(raw string, schema SchemaField) (any, error) {
	v, err := extractJSON(raw)
	if err != nil {
		return nil, err
	}
	if err := Validate(schema, v); err != nil {
		return nil, err
	}
	return v, nil
}

// FormatValidationIssues renders the issues from a *ValidationError (and
// ErrExtractJSON) into a multi-line, human-readable block suitable for feeding
// back to the LLM as a retry prompt. Mirrors structured-output.ts
// validateOutput's "  - path: message" formatting.
func FormatValidationIssues(err error) string {
	if err == nil {
		return ""
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		return "  - " + pathLabel(ve.Path) + ": " + ve.Message
	}
	return "  - (root): " + err.Error()
}

func pathLabel(p string) string {
	if p == "" {
		return "(root)"
	}
	return p
}

// MakeStructuredOutputInstruction renders the system-prompt instruction block
// appended to an agent whose response must conform to schema. Mirrors
// structured-output.ts buildStructuredOutputInstruction, minus the JSON-Schema
// rendering (we emit a compact prose description of the SchemaField, which is
// sufficient guidance and avoids a schema serializer dependency).
func MakeStructuredOutputInstruction(schema SchemaField) string {
	var b strings.Builder
	b.WriteString("\n## Output Format (REQUIRED)\n")
	b.WriteString("You MUST respond with ONLY valid JSON that conforms to the following schema.\n")
	b.WriteString("Do NOT include any text, markdown fences, or explanation outside the JSON.\n")
	b.WriteString("Do NOT wrap the JSON in ```json code fences.\n\n")
	b.WriteString("Schema:\n")
	renderSchemaField(&b, "", schema)
	return b.String()
}

func renderSchemaField(b *strings.Builder, path string, f SchemaField) {
	label := f.Name
	if label == "" {
		label = path
		if label == "" {
			label = "root"
		}
	}
	req := ""
	if f.Required {
		req = " (required)"
	}
	b.WriteString("- ")
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(f.Type)
	b.WriteString(req)
	b.WriteString("\n")
	for _, sf := range f.Fields {
		renderSchemaField(b, label, sf)
	}
}
