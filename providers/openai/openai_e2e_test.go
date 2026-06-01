package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
)

var _ model.Model = (*Model)(nil)

func TestGenerateSendsToolsAndOptions(t *testing.T) {
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	weather, _ := tool.NewFunc("get_weather", "Get weather", func(ctx context.Context, in struct {
		City string `json:"city"`
	}) (string, error) {
		return "sunny", nil
	})
	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	_, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")},
		[]tool.Info{weather.Info()}, model.WithTemperature(0.5), model.WithMaxTokens(64))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(got.Tools) != 1 || got.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("tools = %+v, want get_weather", got.Tools)
	}
	if got.Temperature == nil || *got.Temperature != 0.5 {
		t.Fatalf("temperature = %v, want 0.5", got.Temperature)
	}
	if got.MaxTokens == nil || *got.MaxTokens != 64 {
		t.Fatalf("max_tokens = %v, want 64", got.MaxTokens)
	}
}

func TestWithReasoningEffortReachesRequest(t *testing.T) {
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	m, _ := New("deepseek-reasoner", WithBaseURL(srv.URL), WithAPIKey("k"))
	_, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil,
		WithReasoningEffort("high"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want high", got.ReasoningEffort)
	}
}

func TestSamplingAndExtraBodyReachRequest(t *testing.T) {
	var raw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &raw)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	m, _ := New("deepseek-chat", WithBaseURL(srv.URL), WithAPIKey("k"))
	_, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil,
		model.WithTopP(0.9), WithStop("STOP"), WithSeed(7),
		WithFrequencyPenalty(0.5), WithPresencePenalty(0.25),
		WithExtraBody("thinking", map[string]string{"type": "enabled"}))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, key := range []string{"top_p", "stop", "seed", "frequency_penalty", "presence_penalty", "thinking"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("request missing %q; body keys = %v", key, keysOf(raw))
		}
	}
	if string(raw["thinking"]) != `{"type":"enabled"}` {
		t.Fatalf("thinking = %s", raw["thinking"])
	}
	if string(raw["seed"]) != "7" {
		t.Fatalf("seed = %s", raw["seed"])
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestToOpenAIMessagesToolRoundtrip(t *testing.T) {
	msgs := []message.Message{
		message.UserText("weather?"),
		message.Assistant(message.NewToolUse("call_1", "get_weather", json.RawMessage(`{"city":"SF"}`))),
		message.ToolResults(message.NewToolResult("call_1", "get_weather", []message.Block{message.NewText("sunny")}, false)),
	}
	out := toOpenAIMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("got %d messages, want 3", len(out))
	}
	if out[1].Role != "assistant" || len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant message = %+v", out[1])
	}
	if out[2].Role != "tool" || out[2].ToolCallID != "call_1" || out[2].Content != "sunny" {
		t.Fatalf("tool message = %+v", out[2])
	}
}

func TestGenerateErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer srv.Close()
	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	_, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil)
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("error = %v, want status 500", err)
	}
}

func TestStreamParsesSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"echo\",\"arguments\":\"{\\\"text\\\":\"}}]}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"hi\\\"}\"}}]}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))

	var text string
	var final *message.Message
	for ev, err := range m.Stream(context.Background(), []message.Message{message.UserText("hi")}, nil) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		switch e := ev.(type) {
		case model.TextDelta:
			text += e.Text
		case model.FinalMessage:
			fm := e.Message
			final = &fm
		}
	}
	if text != "hello" {
		t.Fatalf("text = %q, want hello", text)
	}
	if final == nil {
		t.Fatal("no final message")
	}
	calls := final.ToolUses()
	if len(calls) != 1 || calls[0].Name != "echo" || string(calls[0].Input) != `{"text":"hi"}` {
		t.Fatalf("tool calls = %+v", calls)
	}
}

// TestRunnerToolLoopWithDeepSeekShape drives the full adapter <-> runner chain
// against a server emulating DeepSeek's OpenAI-compatible API: the first call
// returns a tool call, the second returns the final text after seeing the tool
// result echoed back.
func TestRunnerToolLoopWithDeepSeekShape(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		var req chatRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if n == 1 {
			io.WriteString(w, `{"choices":[{"message":{"tool_calls":[`+
				`{"id":"call_1","type":"function","function":{"name":"echo","arguments":"{\"text\":\"go\"}"}}]}}]}`)
			return
		}
		// Second call must carry the assistant tool_call and the tool result.
		if len(req.Messages) < 4 || req.Messages[3].Role != "tool" || req.Messages[3].Content != "echo: go" {
			t.Errorf("second request messages = %+v", req.Messages)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"final answer"}}]}`)
	}))
	defer srv.Close()

	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in struct {
		Text string `json:"text"`
	}) (string, error) {
		return "echo: " + in.Text, nil
	})
	mode, _ := agent.NewMode("default", "be useful", agent.WithTools(echo))
	a, _ := agent.New("assistant", agent.WithMode(mode), agent.WithDefaultMode("default"))
	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	r, _ := runner.New(m)

	result, err := r.Run(context.Background(), a, runner.Text("hi"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Text() != "final answer" {
		t.Fatalf("text = %q, want final answer", result.Text())
	}
	if calls != 2 {
		t.Fatalf("server calls = %d, want 2", calls)
	}
}
