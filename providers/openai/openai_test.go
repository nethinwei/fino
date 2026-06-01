package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nethinwei/fino/message"
)

func TestGenerateReturnsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`)
	}))
	defer srv.Close()

	m, err := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	msg, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := msg.Text(); got != "hello" {
		t.Fatalf("text = %q, want %q", got, "hello")
	}
	if msg.Role != message.RoleAssistant {
		t.Fatalf("role = %q, want assistant", msg.Role)
	}
}

func TestGenerateParsesToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[`+
			`{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}`+
			`]}}]}`)
	}))
	defer srv.Close()

	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	msg, err := m.Generate(context.Background(), []message.Message{message.UserText("weather?")}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	calls := msg.ToolUses()
	if len(calls) != 1 {
		t.Fatalf("got %d tool uses, want 1", len(calls))
	}
	if calls[0].ID != "call_1" || calls[0].Name != "get_weather" {
		t.Fatalf("call = %+v", calls[0])
	}
	if string(calls[0].Input) != `{"city":"SF"}` {
		t.Fatalf("input = %s, want {\"city\":\"SF\"}", calls[0].Input)
	}
}
