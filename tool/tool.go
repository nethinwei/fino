// Package tool defines the Tool interface, function-tool helpers, and JSON
// Schema inference for the fino Agent SDK.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/nethinwei/fino/message"
)

// Info holds static metadata about a tool, including its name, description,
// JSON Schema for its input, and optional user-defined metadata.
type Info struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Metadata    map[string]any
}

// Result is the output of a tool execution. Content uses message blocks so
// tools can return structured data, not just plain strings.
type Result struct {
	Content []message.Block
	IsError bool
}

// Text returns the concatenated text of all text-type blocks in the Result.
func (r Result) Text() string {
	out := ""
	for _, block := range r.Content {
		if block.Type == message.TypeText {
			out += block.Text
		}
	}
	return out
}

// Tool is the core interface for all tools. Implementations provide metadata
// via Info and execute via Run.
type Tool interface {
	// Info returns the static metadata for this tool.
	Info() Info
	// Run executes the tool with the given JSON input and returns a Result.
	Run(ctx context.Context, input json.RawMessage) (Result, error)
}

// FuncReturn constrains the return type of NewFunc to either string (auto-
// wrapped into a text block) or Result (passed through).
type FuncReturn interface {
	~string | Result
}

// Option configures a function tool.
type Option func(*funcConfig)

type funcConfig struct {
	schema   json.RawMessage
	metadata map[string]any
}

// WithSchema overrides the inferred JSON Schema for a function tool's input.
// The schema is deep-copied to prevent mutation by the caller.
func WithSchema(schema json.RawMessage) Option {
	return func(c *funcConfig) { c.schema = append(json.RawMessage(nil), schema...) }
}

// WithMetadata attaches a key-value pair to the tool's metadata.
func WithMetadata(key string, value any) Option {
	return func(c *funcConfig) {
		if c.metadata == nil {
			c.metadata = map[string]any{}
		}
		c.metadata[key] = value
	}
}

type funcTool[T any, R FuncReturn] struct {
	info Info
	fn   func(context.Context, T) (R, error)
}

// NewFunc creates a Tool from a Go function. The type parameter T is the input
// struct (JSON-decoded from the model's tool call), and R is the return type
// constrained by FuncReturn. When R is string, the return value is automatically
// wrapped into a text block; when R is Result, it is passed through.
func NewFunc[T any, R FuncReturn](name, description string, fn func(context.Context, T) (R, error), opts ...Option) (Tool, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("tool name is required")
	}
	if fn == nil {
		return nil, errors.New("tool function is required")
	}
	cfg := funcConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	schema := cfg.schema
	if len(schema) == 0 {
		generated, err := GenerateSchema[T]()
		if err != nil {
			return nil, fmt.Errorf("generate schema: %w", err)
		}
		schema = generated
	}
	return &funcTool[T, R]{
		info: Info{Name: name, Description: description, InputSchema: schema, Metadata: cfg.metadata},
		fn:   fn,
	}, nil
}

func (t *funcTool[T, R]) Info() Info { return t.info }

func (t *funcTool[T, R]) Run(ctx context.Context, input json.RawMessage) (Result, error) {
	var parsed T
	if len(input) > 0 {
		if err := json.Unmarshal(input, &parsed); err != nil {
			return Result{}, fmt.Errorf("parse tool input: %w", err)
		}
	}
	out, err := t.fn(ctx, parsed)
	if err != nil {
		return Result{}, err
	}
	return wrapResult[R](out), nil
}

func wrapResult[R FuncReturn](v R) Result {
	switch r := any(v).(type) {
	case string:
		return Result{Content: []message.Block{message.NewText(r)}}
	case Result:
		return r
	default:
		// Unreachable given the FuncReturn constraint. Panic to catch
		// future constraint extensions that are not handled here.
		panic(fmt.Sprintf("unhandled FuncReturn type: %T", v))
	}
}

// GenerateSchema produces a JSON Schema from the struct type T. It supports
// exported struct fields, json tag names, omitempty, basic scalar types,
// slices, maps, and nested structs. Complex constraints (enum, default, min/max,
// oneOf, recursive structures) are outside v1 scope; use WithSchema instead.
func GenerateSchema[T any]() (json.RawMessage, error) {
	var zero T
	typ := reflect.TypeOf(zero)
	if typ == nil {
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), nil
	}
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("tool input must be a struct, got %s", typ.Kind())
	}
	props := map[string]any{}
	required := []string{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name, omitempty := jsonName(field)
		if name == "" || name == "-" {
			continue
		}
		prop := map[string]any{"type": jsonType(field.Type)}
		if desc := schemaDescription(field.Tag.Get("jsonschema")); desc != "" {
			prop["description"] = desc
		}
		props[name] = prop
		if !omitempty {
			required = append(required, name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return json.Marshal(schema)
}

func jsonName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("json")
	if tag == "" {
		return field.Name, false
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	omitempty := false
	for _, part := range parts[1:] {
		if part == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty
}

func jsonType(typ reflect.Type) string {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Struct, reflect.Map:
		return "object"
	default:
		return "string"
	}
}

func schemaDescription(tag string) string {
	for _, part := range strings.Split(tag, ",") {
		if strings.HasPrefix(part, "description=") {
			return strings.TrimPrefix(part, "description=")
		}
	}
	return ""
}
