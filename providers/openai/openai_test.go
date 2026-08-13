package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
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

func TestUsageNormalizationOpenAI(t *testing.T) {
	u := usageToMessage(&chatUsage{
		PromptTokens:        1000,
		CompletionTokens:    200,
		PromptTokensDetails: promptTokensDetails{CachedTokens: 800},
	})
	if u == nil {
		t.Fatal("usageToMessage returned nil")
	}
	if u.InputTokens != 1000 || u.OutputTokens != 200 || u.CacheReadTokens != 800 || u.CacheWriteTokens != 0 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestUsageNormalizationDeepSeek(t *testing.T) {
	u := usageToMessage(&chatUsage{
		PromptTokens:         1000,
		CompletionTokens:     200,
		PromptCacheHitTokens: 800,
	})
	if u == nil {
		t.Fatal("usageToMessage returned nil")
	}
	if u.InputTokens != 1000 || u.OutputTokens != 200 || u.CacheReadTokens != 800 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestUsageNormalizationNil(t *testing.T) {
	if u := usageToMessage(nil); u != nil {
		t.Fatalf("usageToMessage(nil) = %+v, want nil", u)
	}
}

func TestGenerateReturnsUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hi"}}],`+
			`"usage":{"prompt_tokens":1000,"completion_tokens":200,"prompt_tokens_details":{"cached_tokens":800}}}`)
	}))
	defer srv.Close()

	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	msg, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if msg.Usage == nil {
		t.Fatal("usage was not populated")
	}
	if msg.Usage.InputTokens != 1000 || msg.Usage.CacheReadTokens != 800 {
		t.Fatalf("usage = %+v", msg.Usage)
	}
}

func TestStreamCapturesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":200,\"prompt_tokens_details\":{\"cached_tokens\":800}}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	var final *message.Message
	for ev, err := range m.Stream(context.Background(), []message.Message{message.UserText("hi")}, nil) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		if tm, ok := ev.(model.TurnMessage); ok {
			msg := tm.Message
			final = &msg
		}
	}
	if final == nil || final.Usage == nil {
		t.Fatal("stream did not capture usage")
	}
	if final.Usage.InputTokens != 1000 || final.Usage.CacheReadTokens != 800 {
		t.Fatalf("usage = %+v", final.Usage)
	}
}

func TestStreamRequestIncludesStreamOptions(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	for ev, err := range m.Stream(context.Background(), []message.Message{message.UserText("hi")}, nil) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		_ = ev
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
		t.Fatalf("stream_options.include_usage not set: %s", body)
	}
}
