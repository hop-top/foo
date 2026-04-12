package schema

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Supported DSL types and their JSON Schema mappings.
var typeMap = map[string]map[string]any{
	"str":   {"type": "string"},
	"int":   {"type": "integer"},
	"float": {"type": "number"},
	"bool":  {"type": "boolean"},
	"[]str": {"type": "array", "items": map[string]any{"type": "string"}},
	"[]int": {"type": "array", "items": map[string]any{"type": "integer"}},
}

// CompileDSL compiles a shorthand DSL string into a JSON Schema object.
//
// Format: "field1, field2 type, field3 type, ..."
// Default type is "str". All fields are required.
//
// Examples:
//
//	"name, age int, email"       → 3 required string/int/string fields
//	"tags []str, score float"    → array of strings, number
func CompileDSL(dsl string) (map[string]any, error) {
	dsl = strings.TrimSpace(dsl)
	if dsl == "" {
		return nil, fmt.Errorf("empty DSL")
	}

	parts := strings.Split(dsl, ",")
	properties := make(map[string]any)
	var required []string

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		tokens := strings.Fields(part)
		name := tokens[0]
		typeName := "str"
		if len(tokens) > 1 {
			typeName = tokens[1]
		}

		schema, ok := typeMap[typeName]
		if !ok {
			return nil, fmt.Errorf("unknown type %q for field %q", typeName, name)
		}

		properties[name] = schema
		required = append(required, name)
	}

	if len(properties) == 0 {
		return nil, fmt.Errorf("no fields in DSL")
	}

	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}, nil
}

// CompileDSLJSON compiles DSL and returns the JSON Schema as pretty JSON.
func CompileDSLJSON(dsl string) (string, error) {
	schema, err := CompileDSL(dsl)
	if err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
