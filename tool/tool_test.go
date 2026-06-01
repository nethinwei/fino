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
