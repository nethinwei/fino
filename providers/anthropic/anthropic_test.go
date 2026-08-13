package anthropic

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
	"github.com/nethinwei/fino/x/react"
)

var _ model.Model = (*Model)(nil)

func TestGenerateReturnsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"content":[{"type":"text","text":"hello"}]}`)
	}))
	defer srv.Close()
	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	msg, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if msg.Text() != "hello" || msg.Role != message.RoleAssistant {
		t.Fatalf("msg = %+v", msg)
	}
}

func TestGenerateParsesToolUseAndThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"content":[`+
			`{"type":"thinking","thinking":"hmm"},`+
			`{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"SF"}}`+
			`]}`)
	}))
	defer srv.Close()
	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	msg, err := m.Generate(context.Background(), []message.Message{message.UserText("weather?")}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	calls := msg.ToolUses()
	if len(calls) != 1 || calls[0].ID != "toolu_1" || calls[0].Name != "get_weather" {
		t.Fatalf("calls = %+v", calls)
	}
	if string(calls[0].Input) != `{"city":"SF"}` {
		t.Fatalf("input = %s", calls[0].Input)
	}
}

func TestBuildRequestSystemToolsAndOptions(t *testing.T) {
	var got msgRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("headers = %v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()

	weather, _ := tool.NewFunc("get_weather", "Get weather", func(ctx context.Context, in struct {
		City string `json:"city"`
	}) (string, error) {
		return "sunny", nil
	})
	msgs := []message.Message{message.SystemText("be terse"), message.UserText("hi")}
	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	_, err := m.Generate(context.Background(), msgs, []tool.Info{weather.Info()},
		model.WithTemperature(0.3), model.WithMaxTokens(128))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.System != "be terse" {
		t.Fatalf("system = %q, want be terse", got.System)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v (system must not be in array)", got.Messages)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "get_weather" {
		t.Fatalf("tools = %+v", got.Tools)
	}
	if got.MaxTokens != 128 || got.Temperature == nil || *got.Temperature != 0.3 {
		t.Fatalf("maxTokens=%d temp=%v", got.MaxTokens, got.Temperature)
	}
}

func TestTopPAndStopSequencesReachRequest(t *testing.T) {
	var got msgRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()
	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	_, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil,
		model.WithTopP(0.8), WithStopSequences("END", "STOP"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.TopP == nil || *got.TopP != 0.8 {
		t.Fatalf("top_p = %v, want 0.8", got.TopP)
	}
	if len(got.StopSequences) != 2 || got.StopSequences[0] != "END" {
		t.Fatalf("stop_sequences = %v", got.StopSequences)
	}
}

func TestWithThinkingAndTopKReachRequest(t *testing.T) {
	var got msgRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()
	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	_, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil,
		WithThinking(2048), WithTopK(40))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Thinking == nil || got.Thinking.Type != "enabled" || got.Thinking.BudgetTokens != 2048 {
		t.Fatalf("thinking = %+v, want enabled/2048", got.Thinking)
	}
	if got.TopK == nil || *got.TopK != 40 {
		t.Fatalf("top_k = %v, want 40", got.TopK)
	}
}

func TestToAnthropicMessagesToolRoundtrip(t *testing.T) {
	msgs := []message.Message{
		message.SystemText("sys"),
		message.UserText("weather?"),
		message.Assistant(message.NewToolUse("toolu_1", "get_weather", json.RawMessage(`{"city":"SF"}`))),
		message.ToolResults(message.NewToolResult("toolu_1", "get_weather", []message.Block{message.NewText("sunny")}, false)),
	}
	system, out := toAnthropicMessages(msgs)
	if system != "sys" {
		t.Fatalf("system = %q", system)
	}
	if len(out) != 3 {
		t.Fatalf("got %d messages, want 3", len(out))
	}
	if out[1].Role != "assistant" || out[1].Content[0].Type != "tool_use" || out[1].Content[0].ID != "toolu_1" {
		t.Fatalf("assistant = %+v", out[1])
	}
	if out[2].Role != "user" || out[2].Content[0].Type != "tool_result" ||
		out[2].Content[0].ToolUseID != "toolu_1" || string(out[2].Content[0].Content) != `"sunny"` {
		t.Fatalf("tool_result message = %+v", out[2])
	}
}

func TestGenerateErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"message":"bad"}}`)
	}))
	defer srv.Close()
	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))
	_, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil)
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("error = %v, want status 400", err)
	}
}

func TestStreamParsesSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hel\"}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n")
		io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"echo\"}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"text\\\":\"}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"hi\\\"}\"}}\n\n")
		io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
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
		case model.TurnMessage:
			fm := e.Message
			final = &fm
		}
	}
	if text != "hello" {
		t.Fatalf("text = %q, want hello", text)
	}
	calls := final.ToolUses()
	if len(calls) != 1 || calls[0].Name != "echo" || string(calls[0].Input) != `{"text":"hi"}` {
		t.Fatalf("tool calls = %+v", calls)
	}
}

// TestRunnerToolLoopWithDeepSeekShape drives the adapter <-> runner chain
// against a server emulating DeepSeek's Anthropic-compatible API.
func TestRunnerToolLoopWithDeepSeekShape(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		var req msgRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if n == 1 {
			io.WriteString(w, `{"content":[{"type":"tool_use","id":"toolu_1","name":"echo","input":{"text":"go"}}]}`)
			return
		}
		// Second call must carry the tool_result in a user message.
		last := req.Messages[len(req.Messages)-1]
		if last.Role != "user" || last.Content[0].Type != "tool_result" || string(last.Content[0].Content) != `"echo: go"` {
			t.Errorf("second request last message = %+v", last)
		}
		io.WriteString(w, `{"content":[{"type":"text","text":"final answer"}]}`)
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
	l, _ := react.New(r)

	result, err := l.Run(context.Background(), a, runner.Text("hi"))
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

func TestUsageNormalization(t *testing.T) {
	u := usageToMessage(&anthropicUsage{
		InputTokens:              100,
		OutputTokens:             50,
		CacheCreationInputTokens: 20,
		CacheReadInputTokens:     80,
	})
	if u == nil {
		t.Fatal("usageToMessage returned nil")
	}
	if u.InputTokens != 200 || u.OutputTokens != 50 || u.CacheReadTokens != 80 || u.CacheWriteTokens != 20 {
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
		io.WriteString(w, `{"content":[{"type":"text","text":"hi"}],`+
			`"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":20,"cache_read_input_tokens":80}}`)
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
	if msg.Usage.InputTokens != 200 || msg.Usage.CacheReadTokens != 80 || msg.Usage.CacheWriteTokens != 20 {
		t.Fatalf("usage = %+v", msg.Usage)
	}
}

func TestStreamCapturesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"cache_creation_input_tokens\":20,\"cache_read_input_tokens\":80}}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
		io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":50}}\n\n")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
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
	if final.Usage.InputTokens != 200 || final.Usage.OutputTokens != 50 || final.Usage.CacheReadTokens != 80 || final.Usage.CacheWriteTokens != 20 {
		t.Fatalf("usage = %+v", final.Usage)
	}
}
