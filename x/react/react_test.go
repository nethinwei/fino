package react_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/react"
)

// scriptedModel records calls and returns scripted responses.
type scriptedModel struct {
	responses []message.Message
	calls     [][]message.Message
}

func (m *scriptedModel) Generate(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) (*message.Message, error) {
	m.calls = append(m.calls, append([]message.Message(nil), messages...))
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
		t.Fatalf("NewMode: %v", err)
	}
	a, err := agent.New("assistant", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func echoTool(t *testing.T) tool.Tool {
	t.Helper()
	echo, err := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "echo: " + in.Text, nil
	})
	if err != nil {
		t.Fatalf("NewFunc: %v", err)
	}
	return echo
}

type suspendPolicy struct{}

func (suspendPolicy) Authorize(ctx context.Context, req policy.Request) (policy.Decision, error) {
	return policy.Decision{Kind: policy.DecisionSuspend, Reason: "need approval"}, nil
}

type denyPolicy struct{}

func (denyPolicy) Authorize(ctx context.Context, req policy.Request) (policy.Decision, error) {
	return policy.Decision{Allow: false, Reason: "blocked"}, nil
}

func TestLoopRunReturnsFinalText(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("hello"))}}
	r, _ := runner.New(m)
	l, _ := react.New(r)
	result, err := l.Run(context.Background(), testAgent(t), runner.Text("hi"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Text() != "hello" {
		t.Fatalf("text = %q, want hello", result.Text())
	}
}

func TestLoopRunExecutesToolAndContinues(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
		message.Assistant(message.NewText("final")),
	}}
	r, _ := runner.New(m)
	l, _ := react.New(r)
	result, err := l.Run(context.Background(), testAgent(t, echoTool(t)), runner.Text("hi"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Text() != "final" {
		t.Fatalf("text = %q, want final", result.Text())
	}
}

func TestLoopRunMultipleToolCallsBatchResult(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(
			message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"a"}`)),
			message.NewToolUse("call_2", "echo", json.RawMessage(`{"text":"b"}`)),
		),
		message.Assistant(message.NewText("done")),
	}}
	r, _ := runner.New(m)
	l, _ := react.New(r)
	result, err := l.Run(context.Background(), testAgent(t, echoTool(t)), runner.Text("hi"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	toolMsgs := 0
	for _, msg := range result.Messages {
		if msg.Role == message.RoleTool {
			toolMsgs++
		}
	}
	if toolMsgs != 1 {
		t.Fatalf("tool messages = %d, want 1 (batched)", toolMsgs)
	}
}

func TestLoopRunMaxTurns(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("c1", "echo", json.RawMessage(`{"text":"a"}`))),
		message.Assistant(message.NewToolUse("c2", "echo", json.RawMessage(`{"text":"b"}`))),
		message.Assistant(message.NewToolUse("c3", "echo", json.RawMessage(`{"text":"c"}`))),
	}}
	r, _ := runner.New(m)
	l, _ := react.New(r, react.WithMaxTurns(2))
	_, err := l.Run(context.Background(), testAgent(t, echoTool(t)), runner.Text("hi"))
	if !errors.Is(err, runner.ErrMaxTurns) {
		t.Fatalf("error = %v, want ErrMaxTurns", err)
	}
}

func TestLoopRunPolicyDenial(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
	}}
	r, _ := runner.New(m, runner.WithPolicy(denyPolicy{}))
	l, _ := react.New(r)
	_, err := l.Run(context.Background(), testAgent(t, echoTool(t)), runner.Text("hi"))
	var tde *runner.ToolDeniedError
	if !errors.As(err, &tde) {
		t.Fatalf("error = %v, want ToolDeniedError", err)
	}
}

func TestLoopRunSuspendAndResume(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
		message.Assistant(message.NewText("done")),
	}}
	r, _ := runner.New(m, runner.WithPolicy(suspendPolicy{}))
	l, _ := react.New(r)
	result, err := l.Run(context.Background(), testAgent(t, echoTool(t)), runner.Text("hi"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Suspended || len(result.PendingCalls) != 1 {
		t.Fatalf("result = %+v, want suspended with 1 pending", result)
	}
	suspended, err := result.SuspendedRun()
	if err != nil {
		t.Fatalf("SuspendedRun: %v", err)
	}
	result2, err := l.ResumeApproved(context.Background(), testAgent(t, echoTool(t)), suspended,
		[]runner.Approval{{CallID: "call_1", Approved: true}})
	if err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	if result2.Text() != "done" {
		t.Fatalf("text = %q, want done", result2.Text())
	}
}

func TestLoopStreamToolEvents(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
		message.Assistant(message.NewText("done")),
	}}
	r, _ := runner.New(m)
	l, _ := react.New(r)
	var calls, results, finals, turns int
	for ev, err := range l.Stream(context.Background(), testAgent(t, echoTool(t)), runner.Text("hi")) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch ev.(type) {
		case model.TurnMessage:
			turns++
		case model.ToolCall:
			calls++
		case model.ToolResult:
			results++
		case model.FinalMessage:
			finals++
		}
	}
	if turns != 2 || calls != 1 || results != 1 || finals != 1 {
		t.Fatalf("events: turns=%d calls=%d results=%d finals=%d, want 2/1/1/1", turns, calls, results, finals)
	}
}

func TestLoopStreamSuspend(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
	}}
	r, _ := runner.New(m, runner.WithPolicy(suspendPolicy{}))
	l, _ := react.New(r)
	var sawSuspended bool
	var pending []model.SuspendedCall
	for ev, err := range l.Stream(context.Background(), testAgent(t, echoTool(t)), runner.Text("hi")) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if s, ok := ev.(model.Suspended); ok {
			sawSuspended = true
			pending = s.PendingCalls
		}
	}
	if !sawSuspended {
		t.Fatalf("did not see model.Suspended event")
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
}

func TestNewRejectsNonPositiveMaxTurns(t *testing.T) {
	m := &scriptedModel{}
	r, _ := runner.New(m)
	for _, n := range []int{0, -1} {
		if _, err := react.New(r, react.WithMaxTurns(n)); err == nil {
			t.Fatalf("react.New(WithMaxTurns(%d)) error = nil, want non-nil", n)
		}
	}
}

func TestNewRejectsNilRunner(t *testing.T) {
	if _, err := react.New(nil); err == nil {
		t.Fatal("react.New(nil) error = nil, want non-nil")
	}
}
