package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// AddTool registers a tool with lenient input validation.
// Unlike mcp.AddTool, string values are coerced to their schema-declared types
// before validation and unmarshaling. LLMs frequently send "true" instead of
// true or "5" instead of 5 — this function handles both forms transparently.
//
// The input schema is inferred from In (same as mcp.AddTool).
// Output schema is not supported (Out must be any).
//
// If the schema cannot be inferred (e.g. unsupported jsonschema tags), the tool
// is registered with an open schema and a warning is logged instead of panicking.
func AddTool[In any](s *mcp.Server, t *mcp.Tool, h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, error)) {
	schema, err := jsonschema.ForType(reflect.TypeFor[In](), &jsonschema.ForOptions{})
	if err != nil {
		slog.Warn("AddTool: schema inference failed, using open schema",
			slog.String("tool", t.Name), slog.Any("error", err))
		registerOpenSchema(s, t, h)
		return
	}

	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		slog.Warn("AddTool: schema resolve failed, using open schema",
			slog.String("tool", t.Name), slog.Any("error", err))
		registerOpenSchema(s, t, h)
		return
	}

	tt := *t
	tt.InputSchema = schema

	s.AddTool(&tt, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.Params.Arguments
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}

		// Unmarshal into map, coerce types, validate, re-marshal.
		var m map[string]any
		if err := json.Unmarshal(args, &m); err != nil {
			return toolError(fmt.Sprintf("invalid arguments: %v", err)), nil
		}

		coerceStringTypes(m, schema)

		if err := resolved.Validate(&m); err != nil {
			return toolError(fmt.Sprintf("validating arguments: %v", err)), nil
		}

		coerced, err := json.Marshal(m)
		if err != nil {
			return toolError(fmt.Sprintf("marshal: %v", err)), nil
		}

		var in In
		if err := json.Unmarshal(coerced, &in); err != nil {
			return toolError(fmt.Sprintf("unmarshal input: %v", err)), nil
		}

		res, err := h(ctx, req, in)
		if err != nil {
			return toolError(err.Error()), nil
		}
		return res, nil
	})
}

const (
	strTrue  = "true"
	strFalse = "false"
)

// coerceStringTypes converts string values to their schema-declared types.
// Only converts when the schema unambiguously declares a non-string type.
func coerceStringTypes(m map[string]any, schema *jsonschema.Schema) {
	if schema == nil || schema.Properties == nil {
		return
	}
	for key, prop := range schema.Properties {
		val, ok := m[key]
		if !ok {
			continue
		}
		s, isStr := val.(string)
		if !isStr {
			continue
		}
		typ := propType(prop)
		switch typ {
		case "boolean":
			switch strings.ToLower(s) {
			case strTrue, "1":
				m[key] = true
			case strFalse, "0":
				m[key] = false
			}
		case "integer":
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				m[key] = n
			}
		case "number":
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				m[key] = f
			}
		}
	}
}

// propType returns the effective non-null type for a schema property.
// Handles both Type ("boolean") and Types (["null", "boolean"]) forms.
func propType(s *jsonschema.Schema) string {
	if s.Type != "" {
		return s.Type
	}
	for _, t := range s.Types {
		if t != "null" {
			return t
		}
	}
	return ""
}

// registerOpenSchema registers the tool with a permissive schema (accepts any object).
// Used as fallback when the typed schema cannot be inferred from struct tags.
func registerOpenSchema[In any](s *mcp.Server, t *mcp.Tool, h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, error)) {
	tt := *t
	tt.InputSchema = &jsonschema.Schema{Type: "object"}

	s.AddTool(&tt, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.Params.Arguments
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}

		var in In
		if err := json.Unmarshal(args, &in); err != nil {
			return toolError(fmt.Sprintf("unmarshal input: %v", err)), nil
		}

		res, err := h(ctx, req, in)
		if err != nil {
			return toolError(err.Error()), nil
		}
		return res, nil
	})
}

func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}
