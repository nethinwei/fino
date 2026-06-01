package qwen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/providers/openai"
)

func TestQwenOptionsReachBody(t *testing.T) {
	var raw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &raw)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	m, err := New("qwen3-max", "k", openai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil,
		WithThinking(true), WithThinkingBudget(500), WithSearch(true))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if string(raw["enable_thinking"]) != "true" {
		t.Fatalf("enable_thinking = %s", raw["enable_thinking"])
	}
	if string(raw["thinking_budget"]) != "500" {
		t.Fatalf("thinking_budget = %s", raw["thinking_budget"])
	}
	if string(raw["enable_search"]) != "true" {
		t.Fatalf("enable_search = %s", raw["enable_search"])
	}
}
