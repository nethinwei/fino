package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/tool"
)

// scriptedModel records calls and returns scripted responses.
type scriptedModel struct {
	responses []message.Message
	calls     [][]message.Message
	opts      [][]model.Option
}

func (m *scriptedModel) Generate(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) (*message.Message, error) {
	m.calls = append(m.calls, append([]message.Message(nil), messages...))
	m.opts = append(m.opts, append([]model.Option(nil), opts...))
	if len(m.responses) == 0 {
		return nil, errors.New("no scripted response")
	}
	msg := m.responses[0]
	m.responses = m.responses[1:]
	return &msg, nil
}

func (m *scriptedModel) Stream(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		msg, err := m.Generate(ctx, messages, tools, opts...)
		if err != nil {
			yield(model.StreamError{Err: err}, err)
			return
		}
		yield(model.TurnMessage{Message: *msg}, nil)
	}
}

type echoInput struct {
	Text string `json:"text"`
}

func testAgent(t *testing.T, tools ...tool.Tool) *agent.Agent {
	t.Helper()
	mode, err := agent.NewMode("default", "be useful", agent.WithTools(tools...))
	if err != nil {
		t.Fatalf("NewMode error: %v", err)
	}
	a, err := agent.New("assistant", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	return a
}

func TestRunReturnsFinalText(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("hello"))}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	result, err := r.Run(context.Background(), testAgent(t), Text("hi"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Text() != "hello" {
		t.Fatalf("text = %q, want hello", result.Text())
	}
}

func TestRunExecutesToolAndContinues(t *testing.T) {
	echo, err := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "echo: " + in.Text, nil
	})
	if err != nil {
		t.Fatalf("NewFunc error: %v", err)
	}
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
		message.Assistant(message.NewText("final")),
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	result, err := r.Run(context.Background(), testAgent(t, echo), Text("hi"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Text() != "final" {
		t.Fatalf("text = %q, want final", result.Text())
	}
}

func TestRunMultipleToolCallsBatchResult(t *testing.T) {
	echo, err := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "echo: " + in.Text, nil
	})
	if err != nil {
		t.Fatalf("NewFunc error: %v", err)
	}
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(
			message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"a"}`)),
			message.NewToolUse("call_2", "echo", json.RawMessage(`{"text":"b"}`)),
		),
		message.Assistant(message.NewText("done")),
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	result, err := r.Run(context.Background(), testAgent(t, echo), Text("hi"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	// The tool message should be a single RoleTool message with 2 tool_result blocks.
	// Messages: [system, user, assistant(tool_uses), tool(results), assistant(final)]
	toolMsgs := 0
	for _, msg := range result.Messages {
		if msg.Role == message.RoleTool {
			toolMsgs++
			if len(msg.Content) != 2 {
				t.Fatalf("tool message has %d blocks, want 2", len(msg.Content))
			}
		}
	}
	if toolMsgs != 1 {
		t.Fatalf("expected 1 tool message, got %d", toolMsgs)
	}
}

func TestRunRejectsSystemMessageInHistory(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("ok"))}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	history := []message.Message{message.SystemText("should fail")}
	_, err = r.Run(context.Background(), testAgent(t), Messages(history))
	if !errors.Is(err, ErrSystemMessageInHistory) {
		t.Fatalf("error = %v, want ErrSystemMessageInHistory", err)
	}
}

func TestRunMergesModelOptions(t *testing.T) {
	mode, err := agent.NewMode("default", "be useful", agent.WithModelOptions(model.WithTemperature(0)))
	if err != nil {
		t.Fatalf("NewMode error: %v", err)
	}
	a, err := agent.New("assistant", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("ok"))}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	_, err = r.Run(context.Background(), a, Text("hi"), WithModelOptions(model.WithMaxTokens(100)))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(m.opts) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(m.opts))
	}
	if len(m.opts[0]) != 2 {
		t.Fatalf("expected 2 opts (mode + run), got %d", len(m.opts[0]))
	}
}

func TestRunDefaultMaxTurns(t *testing.T) {
	responses := make([]message.Message, 11)
	for i := range responses {
		responses[i] = message.Assistant(message.NewToolUse(fmt.Sprintf("call_%d", i), "echo", json.RawMessage(`{"text":"x"}`)))
	}
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "ok", nil
	})
	m := &scriptedModel{responses: responses}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	_, err = r.Run(context.Background(), testAgent(t, echo), Text("hi"))
	if !errors.Is(err, ErrMaxTurns) {
		t.Fatalf("error = %v, want ErrMaxTurns", err)
	}
}

func TestRunWithMaxTurns(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("ok"))}}
	r, err := New(m, WithMaxTurns(20))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	_, err = r.Run(context.Background(), testAgent(t), Text("hi"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
}

func TestRunMissingTool(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "nonexistent", json.RawMessage(`{}`))),
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	_, err = r.Run(context.Background(), testAgent(t), Text("hi"))
	if !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("error = %v, want ErrToolNotFound", err)
	}
}

type denyPolicy struct{}

func (denyPolicy) Authorize(ctx context.Context, req policy.Request) (policy.Decision, error) {
	return policy.Decision{Allow: false, Reason: "blocked"}, nil
}

func TestRunPolicyDenial(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "should not run", nil
	})
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
	}}
	r, err := New(m, WithPolicy(denyPolicy{}))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	_, err = r.Run(context.Background(), testAgent(t, echo), Text("hi"))
	var tde *ToolDeniedError
	if !errors.As(err, &tde) {
		t.Fatalf("error = %v, want ToolDeniedError", err)
	}
}

type failPolicy struct{}

func (failPolicy) Authorize(ctx context.Context, req policy.Request) (policy.Decision, error) {
	return policy.Decision{}, errors.New("policy service down")
}

func TestRunPolicyFailure(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "should not run", nil
	})
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
	}}
	r, err := New(m, WithPolicy(failPolicy{}))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	_, err = r.Run(context.Background(), testAgent(t, echo), Text("hi"))
	if err == nil || err.Error() != "policy service down" {
		t.Fatalf("error = %v, want policy service down", err)
	}
}

func TestRunHandoffSwitchesAgent(t *testing.T) {
	targetMode, err := agent.NewMode("default", "I am the target")
	if err != nil {
		t.Fatalf("NewMode error: %v", err)
	}
	target, err := agent.New("target", agent.WithMode(targetMode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	handoff, err := agent.NewHandoffTool(target)
	if err != nil {
		t.Fatalf("NewHandoffTool error: %v", err)
	}
	sourceMode, err := agent.NewMode("default", "I am the source", agent.WithTools(handoff))
	if err != nil {
		t.Fatalf("NewMode error: %v", err)
	}
	source, err := agent.New("source", agent.WithMode(sourceMode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "handoff_to_target", json.RawMessage(`{}`))),
		message.Assistant(message.NewText("target response")),
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	result, err := r.Run(context.Background(), source, Text("hi"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Text() != "target response" {
		t.Fatalf("text = %q, want target response", result.Text())
	}
	if result.LastAgent != target {
		t.Fatalf("LastAgent = %v, want target", result.LastAgent)
	}
	if result.LastMode != "default" {
		t.Fatalf("LastMode = %q, want default", result.LastMode)
	}
}

func TestRunContextCancellation(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "ok", nil
	})
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
		message.Assistant(message.NewText("ok")),
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.Run(ctx, testAgent(t, echo), Text("hi"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// --- Stream tests ---

func TestStreamForwardsModelEvents(t *testing.T) {
	m := &streamOnlyModel{
		events: []model.Event{
			model.ContentBlockStart{Index: 0, Block: message.NewText("")},
			model.TextDelta{Text: "hel"},
			model.TextDelta{Text: "lo"},
			model.ContentBlockStop{Index: 0, Block: message.NewText("hello")},
			model.TurnMessage{Message: message.Assistant(message.NewText("hello"))},
		},
	}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var got []string
	for event, err := range r.Stream(context.Background(), testAgent(t), Text("hi")) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		switch e := event.(type) {
		case model.TextDelta:
			got = append(got, e.Text)
		case model.FinalMessage:
			got = append(got, "final")
		}
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[0] != "hel" || got[1] != "lo" || got[2] != "final" {
		t.Fatalf("got = %v", got)
	}
}

func TestStreamToolCallAndResult(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "echo: " + in.Text, nil
	})
	// Stream model: first turn has tool call, second turn is final text.
	m := &streamOnlyModel{
		turns: [][]model.Event{
			{
				model.ContentBlockStart{Index: 0, Block: message.NewText("")},
				model.TextDelta{Text: "calling echo"},
				model.ContentBlockStop{Index: 0, Block: message.NewText("calling echo")},
				model.TurnMessage{Message: message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`)))},
			},
			{
				model.ContentBlockStart{Index: 0, Block: message.NewText("")},
				model.TextDelta{Text: "done"},
				model.ContentBlockStop{Index: 0, Block: message.NewText("done")},
				model.TurnMessage{Message: message.Assistant(message.NewText("done"))},
			},
		},
	}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var toolCalls, toolResults, finals int
	for event, err := range r.Stream(context.Background(), testAgent(t, echo), Text("hi")) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		switch event.(type) {
		case model.ToolCall:
			toolCalls++
		case model.ToolResult:
			toolResults++
		case model.FinalMessage:
			finals++
		}
	}
	if toolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", toolCalls)
	}
	if toolResults != 1 {
		t.Fatalf("tool results = %d, want 1", toolResults)
	}
	if finals != 1 {
		t.Fatalf("finals = %d, want 1", finals)
	}
}

func TestStreamHandoff(t *testing.T) {
	targetMode, _ := agent.NewMode("default", "target")
	target, _ := agent.New("target", agent.WithMode(targetMode), agent.WithDefaultMode("default"))
	handoff, _ := agent.NewHandoffTool(target)
	sourceMode, _ := agent.NewMode("default", "source", agent.WithTools(handoff))
	source, _ := agent.New("source", agent.WithMode(sourceMode), agent.WithDefaultMode("default"))

	m := &streamOnlyModel{
		turns: [][]model.Event{
			{
				model.TurnMessage{Message: message.Assistant(message.NewToolUse("call_1", "handoff_to_target", json.RawMessage(`{}`)))},
			},
			{
				model.TextDelta{Text: "from target"},
				model.TurnMessage{Message: message.Assistant(message.NewText("from target"))},
			},
		},
	}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var handoffs int
	for event, err := range r.Stream(context.Background(), source, Text("hi")) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		if _, ok := event.(model.Handoff); ok {
			handoffs++
		}
	}
	if handoffs != 1 {
		t.Fatalf("handoffs = %d, want 1", handoffs)
	}
}

func TestStreamPolicyDenial(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "ok", nil
	})
	m := &streamOnlyModel{
		turns: [][]model.Event{
			{
				model.TurnMessage{Message: message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`)))},
			},
		},
	}
	r, err := New(m, WithPolicy(denyPolicy{}))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var gotErr error
	for _, err := range r.Stream(context.Background(), testAgent(t, echo), Text("hi")) {
		if err != nil {
			gotErr = err
			break
		}
	}
	var tde *ToolDeniedError
	if !errors.As(gotErr, &tde) {
		t.Fatalf("error = %v, want ToolDeniedError", gotErr)
	}
}

func TestStreamHooks(t *testing.T) {
	m := &streamOnlyModel{
		events: []model.Event{
			model.TurnMessage{Message: message.Assistant(message.NewText("ok"))},
		},
	}
	var before, after bool
	r, err := New(m, WithHooks(&hooks.Hooks{
		BeforeModel: func(ctx context.Context, call hooks.ModelCall) context.Context { before = true; return ctx },
		AfterModel:  func(ctx context.Context, result hooks.ModelResult) { after = true },
	}))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	for _, err := range r.Stream(context.Background(), testAgent(t), Text("hi")) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
	}
	if !before || !after {
		t.Fatalf("hooks called before=%v after=%v", before, after)
	}
}

func TestStreamMaxTurns(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "ok", nil
	})
	// Each turn returns a tool call, never final answer
	m := &streamOnlyModel{
		repeatTurn: []model.Event{
			model.TurnMessage{Message: message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"x"}`)))},
		},
	}
	r, err := New(m, WithMaxTurns(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var gotErr error
	for _, err := range r.Stream(context.Background(), testAgent(t, echo), Text("hi")) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if !errors.Is(gotErr, ErrMaxTurns) {
		t.Fatalf("error = %v, want ErrMaxTurns", gotErr)
	}
}

func TestStreamNilAgent(t *testing.T) {
	m := &streamOnlyModel{events: []model.Event{model.TurnMessage{Message: message.Assistant(message.NewText("x"))}}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var gotErr error
	for _, err := range r.Stream(context.Background(), nil, Text("hi")) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if gotErr == nil || gotErr.Error() != "agent is required" {
		t.Fatalf("error = %v, want agent is required", gotErr)
	}
}

func TestStreamRejectsSystemMessageInHistory(t *testing.T) {
	m := &streamOnlyModel{events: []model.Event{model.TurnMessage{Message: message.Assistant(message.NewText("x"))}}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	history := []message.Message{message.SystemText("should fail")}
	var gotErr error
	for _, err := range r.Stream(context.Background(), testAgent(t), Messages(history)) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if !errors.Is(gotErr, ErrSystemMessageInHistory) {
		t.Fatalf("error = %v, want ErrSystemMessageInHistory", gotErr)
	}
}

func TestStreamMissingTool(t *testing.T) {
	m := &streamOnlyModel{
		turns: [][]model.Event{
			{model.TurnMessage{Message: message.Assistant(message.NewToolUse("call_1", "nonexistent", json.RawMessage(`{}`)))}},
		},
	}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var gotErr error
	for _, err := range r.Stream(context.Background(), testAgent(t), Text("hi")) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if !errors.Is(gotErr, ErrToolNotFound) {
		t.Fatalf("error = %v, want ErrToolNotFound", gotErr)
	}
}

func TestStreamPolicyFailure(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "should not run", nil
	})
	m := &streamOnlyModel{
		turns: [][]model.Event{
			{model.TurnMessage{Message: message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`)))}},
		},
	}
	r, err := New(m, WithPolicy(failPolicy{}))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var gotErr error
	for _, err := range r.Stream(context.Background(), testAgent(t, echo), Text("hi")) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if gotErr == nil || gotErr.Error() != "policy service down" {
		t.Fatalf("error = %v, want policy service down", gotErr)
	}
}

func TestStreamContextCancellation(t *testing.T) {
	m := &streamOnlyModel{events: []model.Event{model.TurnMessage{Message: message.Assistant(message.NewText("ok"))}}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var gotErr error
	for _, err := range r.Stream(ctx, testAgent(t), Text("hi")) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if !errors.Is(gotErr, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", gotErr)
	}
}

func TestStreamNoTurnMessage(t *testing.T) {
	m := &streamOnlyModel{events: []model.Event{model.TextDelta{Text: "partial"}}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var gotErr error
	for _, err := range r.Stream(context.Background(), testAgent(t), Text("hi")) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if !errors.Is(gotErr, ErrStreamContract) {
		t.Fatalf("error = %v, want ErrStreamContract", gotErr)
	}
}

func TestStreamProviderFinalMessageIsContractError(t *testing.T) {
	// A provider that yields FinalMessage (instead of TurnMessage) violates the
	// stream contract; the Runner must surface it, not silently tolerate it.
	m := &streamOnlyModel{events: []model.Event{
		model.FinalMessage{Message: message.Assistant(message.NewText("x"))},
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var gotErr error
	for _, err := range r.Stream(context.Background(), testAgent(t), Text("hi")) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if !errors.Is(gotErr, ErrStreamContract) {
		t.Fatalf("error = %v, want ErrStreamContract", gotErr)
	}
}

// TestRunToolHooksFire covers the tool-lifecycle hooks not exercised by
// TestStreamHooks: BeforeTool and AfterTool around a successful tool call.
func TestRunToolHooksFire(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "ok", nil
	})
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"x"}`))),
		message.Assistant(message.NewText("done")),
	}}
	var got []string
	r, err := New(m, WithHooks(&hooks.Hooks{
		BeforeTool: func(ctx context.Context, c hooks.ToolCall) context.Context {
			got = append(got, "before:"+c.Tool.Name)
			return ctx
		},
		AfterTool: func(ctx context.Context, res hooks.ToolResult) {
			got = append(got, "after:"+res.Tool.Name+":"+res.Result.Text())
		},
	}))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	if _, err := r.Run(context.Background(), testAgent(t, echo), Text("hi")); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	want := []string{"before:echo", "after:echo:ok"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("hook order = %v, want %v", got, want)
	}
}

// TestRunOnErrorFires verifies OnError is invoked with the terminal error.
func TestRunOnErrorFires(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "missing", json.RawMessage(`{}`))),
	}}
	var gotErr error
	r, err := New(m, WithHooks(&hooks.Hooks{
		OnError: func(ctx context.Context, err error) { gotErr = err },
	}))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	if _, err := r.Run(context.Background(), testAgent(t), Text("hi")); err == nil {
		t.Fatal("Run error = nil, want ErrToolNotFound")
	}
	if !errors.Is(gotErr, ErrToolNotFound) {
		t.Fatalf("OnError got %v, want ErrToolNotFound", gotErr)
	}
}

// TestRunEmptyHooksStructSafe ensures an all-nil Hooks struct does not panic
// and the run still completes through a tool call.
func TestRunEmptyHooksStructSafe(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "ok", nil
	})
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"x"}`))),
		message.Assistant(message.NewText("done")),
	}}
	r, err := New(m, WithHooks(&hooks.Hooks{}))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	result, err := r.Run(context.Background(), testAgent(t, echo), Text("hi"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Text() != "done" {
		t.Fatalf("result = %q, want done", result.Text())
	}
}

// streamOnlyModel implements model.Model with explicit stream events.
type streamOnlyModel struct {
	events     []model.Event   // single-turn events (used if turns is nil)
	turns      [][]model.Event // multi-turn events
	repeatTurn []model.Event   // repeated every turn (for infinite loop tests)
	callIdx    int
}

func (m *streamOnlyModel) Generate(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) (*message.Message, error) {
	return nil, errors.New("Generate not implemented for streamOnlyModel")
}

func (m *streamOnlyModel) Stream(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) iter.Seq2[model.Event, error] {
	var events []model.Event
	if m.repeatTurn != nil {
		events = m.repeatTurn
	} else if len(m.turns) > 0 {
		if m.callIdx >= len(m.turns) {
			return func(yield func(model.Event, error) bool) {
				yield(model.StreamError{Err: errors.New("no more turns")}, errors.New("no more turns"))
			}
		}
		events = m.turns[m.callIdx]
		m.callIdx++
	} else {
		events = m.events
	}
	return func(yield func(model.Event, error) bool) {
		for _, e := range events {
			if !yield(e, nil) {
				return
			}
		}
	}
}
