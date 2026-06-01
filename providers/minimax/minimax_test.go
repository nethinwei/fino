package minimax

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

func TestMiniMaxOptionsReachBody(t *testing.T) {
	var raw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &raw)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	m, err := New("MiniMax-M2", "k", openai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil,
		WithThinking(true), WithReasoningSplit(true))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if string(raw["thinking"]) != `{"type":"adaptive"}` {
		t.Fatalf("thinking = %s, want {\"type\":\"adaptive\"}", raw["thinking"])
	}
	if string(raw["reasoning_split"]) != "true" {
		t.Fatalf("reasoning_split = %s, want true", raw["reasoning_split"])
	}
}
