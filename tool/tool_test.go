package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nethinwei/fino/message"
)

type searchInput struct {
	Query string `json:"query" jsonschema:"description=Search query"`
}

func TestNewFuncStringResult(t *testing.T) {
	search, err := NewFunc("search", "Search docs", func(ctx context.Context, in searchInput) (string, error) {
		return "found: " + in.Query, nil
	})
	if err != nil {
		t.Fatalf("NewFunc error: %v", err)
	}
	result, err := search.Run(context.Background(), json.RawMessage(`{"query":"go"}`))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := result.Text(); got != "found: go" {
		t.Fatalf("Text() = %q", got)
	}
}

func TestNewFuncResultReturn(t *testing.T) {
	custom, err := NewFunc("custom", "Custom result", func(ctx context.Context, in searchInput) (Result, error) {
		return Result{Content: []message.Block{message.NewText("block")}}, nil
	})
	if err != nil {
		t.Fatalf("NewFunc error: %v", err)
	}
	result, err := custom.Run(context.Background(), json.RawMessage(`{"query":"go"}`))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := result.Text(); got != "block" {
		t.Fatalf("Text() = %q", got)
	}
}

func TestWithEffectsPopulatesInfo(t *testing.T) {
	want := Effects{
		ReadOnly:         true,
		Idempotent:       true,
		ParallelSafe:     true,
		Destructive:      true,
		ExternalWrite:    true,
		RequiresApproval: true,
		SensitiveInput:   true,
		SensitiveOutput:  true,
	}
	search, err := NewFunc("search", "Search docs",
		func(ctx context.Context, in searchInput) (string, error) { return "", nil },
		WithEffects(want))
	if err != nil {
		t.Fatalf("NewFunc error: %v", err)
	}
	if got := search.Info().Effects; got != want {
		t.Fatalf("Effects = %+v, want %+v", got, want)
	}
}

func TestEffectsZeroValueWhenUnspecified(t *testing.T) {
	search, err := NewFunc("search", "Search docs",
		func(ctx context.Context, in searchInput) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("NewFunc error: %v", err)
	}
	if got := search.Info().Effects; got != (Effects{}) {
		t.Fatalf("Effects = %+v, want zero value", got)
	}
}

func TestNewFuncRejectsMissingName(t *testing.T) {
	_, err := NewFunc("", "Search docs", func(ctx context.Context, in searchInput) (string, error) { return "", nil })
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSchemaIncludesField(t *testing.T) {
	search, err := NewFunc("search", "Search docs", func(ctx context.Context, in searchInput) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("NewFunc error: %v", err)
	}
	schema := string(search.Info().InputSchema)
	if !strings.Contains(schema, `"query"`) {
		t.Fatalf("schema missing query: %s", schema)
	}
}

// TestGenerateSchemaTypeMapping exercises jsonType across every Go kind it
// maps, including pointer indirection, so an inference regression is caught.
func TestGenerateSchemaTypeMapping(t *testing.T) {
	type nested struct {
		X int `json:"x"`
	}
	type allTypes struct {
		Flag   bool              `json:"flag"`
		Count  int               `json:"count"`
		Big    uint64            `json:"big"`
		Ratio  float64           `json:"ratio"`
		Tags   []string          `json:"tags"`
		Arr    [2]int            `json:"arr"`
		Meta   map[string]string `json:"meta"`
		Nested nested            `json:"nested"`
		Ptr    *int              `json:"ptr"`
		Name   string            `json:"name"`
	}
	raw, err := GenerateSchema[allTypes]()
	if err != nil {
		t.Fatalf("GenerateSchema error: %v", err)
	}
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	want := map[string]string{
		"flag":   "boolean",
		"count":  "number",
		"big":    "number",
		"ratio":  "number",
		"tags":   "array",
		"arr":    "array",
		"meta":   "object",
		"nested": "object",
		"ptr":    "number",
		"name":   "string",
	}
	for field, wantType := range want {
		if got := schema.Properties[field].Type; got != wantType {
			t.Errorf("%s type = %q, want %q", field, got, wantType)
		}
	}
}

// TestGenerateSchemaRejectsNonStruct verifies the non-struct guard.
func TestGenerateSchemaRejectsNonStruct(t *testing.T) {
	if _, err := GenerateSchema[int](); err == nil {
		t.Fatal("expected error for non-struct input")
	}
}
