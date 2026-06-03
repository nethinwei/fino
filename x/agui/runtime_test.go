package agui

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
)

// streamModel yields the same event list on every Stream call.
type streamModel struct{ events []model.Event }

func (m *streamModel) Generate(_ context.Context, _ []message.Message, _ []tool.Info, _ ...model.Option) (*message.Message, error) {
	msg := message.Assistant(message.NewText("ok"))
	return &msg, nil
}

func (m *streamModel) Stream(_ context.Context, _ []message.Message, _ []tool.Info, _ ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		for _, ev := range m.events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// multiTurnModel yields a different event list per Stream call.
type multiTurnModel struct {
	turns [][]model.Event
	idx   int
}

func (m *multiTurnModel) Generate(_ context.Context, _ []message.Message, _ []tool.Info, _ ...model.Option) (*message.Message, error) {
	msg := message.Assistant(message.NewText("ok"))
	return &msg, nil
}

func (m *multiTurnModel) Stream(_ context.Context, _ []message.Message, _ []tool.Info, _ ...model.Option) iter.Seq2[model.Event, error] {
	i := m.idx % len(m.turns)
	m.idx++
	events := m.turns[i]
	return func(yield func(model.Event, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// suspendAllPolicy suspends every tool call.
type suspendAllPolicy struct{}

func (suspendAllPolicy) Authorize(_ context.Context, _ policy.Request) (policy.Decision, error) {
	return policy.Decision{Kind: policy.DecisionSuspend, Reason: "needs approval"}, nil
}

// echoTool returns its input as the result.
type echoTool struct{}

func (e *echoTool) Info() tool.Info { return tool.Info{Name: "echo"} }

func (e *echoTool) Run(_ context.Context, input json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: []message.Block{message.NewText(string(input))}}, nil
}

func makeAgent(t *testing.T, tools ...tool.Tool) *agent.Agent {
	t.Helper()
	m, err := agent.NewMode("default", "you are helpful", agent.WithTools(tools...))
	if err != nil {
		t.Fatalf("NewMode: %v", err)
	}
	a, err := agent.New("test", agent.WithMode(m), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return a
}

func makeRuntime(t *testing.T, m model.Model, a *agent.Agent, opts ...runner.Option) *Runtime {
	t.Helper()
	r, err := runner.New(m, opts...)
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	rt, err := NewRuntime(r, a)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

func collectEvents(t *testing.T, rt *Runtime, input RunAgentInput) []Event {
	t.Helper()
	var events []Event
	for ev, err := range rt.Stream(context.Background(), input) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		events = append(events, ev)
	}
	return events
}

func TestRuntimeTextOnlyRun(t *testing.T) {
	m := &streamModel{events: []model.Event{
		model.TextDelta{Text: "hi"},
		model.TurnMessage{Message: message.Assistant(message.NewText("hi"))},
	}}
	rt := makeRuntime(t, m, makeAgent(t))

	events := collectEvents(t, rt, RunAgentInput{ThreadID: "t1", RunID: "r1"})

	if _, ok := events[0].(RunStartedEvent); !ok {
		t.Fatalf("events[0] = %T, want RunStartedEvent", events[0])
	}
	last := events[len(events)-1]
	if _, ok := last.(RunFinishedEvent); !ok {
		t.Fatalf("last event = %T, want RunFinishedEvent", last)
	}
	hasStart, hasContent, hasEnd := false, false, false
	for _, ev := range events {
		switch ev.(type) {
		case TextMessageStartEvent:
			hasStart = true
		case TextMessageContentEvent:
			hasContent = true
		case TextMessageEndEvent:
			hasEnd = true
		}
	}
	if !hasStart || !hasContent || !hasEnd {
		t.Fatalf("text lifecycle incomplete: start=%v content=%v end=%v", hasStart, hasContent, hasEnd)
	}
}

func TestRuntimeRunStartedCarriesIDs(t *testing.T) {
	parent := "parent-run"
	m := &streamModel{events: []model.Event{
		model.TurnMessage{Message: message.Assistant(message.NewText("ok"))},
	}}
	rt := makeRuntime(t, m, makeAgent(t))

	events := collectEvents(t, rt, RunAgentInput{ThreadID: "thread-1", RunID: "run-1", ParentRunID: &parent})

	started := events[0].(RunStartedEvent)
	if started.ThreadID != "thread-1" || started.RunID != "run-1" {
		t.Fatalf("started = %+v", started)
	}
	if started.ParentRunID == nil || *started.ParentRunID != "parent-run" {
		t.Fatalf("parentRunID = %v", started.ParentRunID)
	}
}

func TestRuntimeSuspensionEmitsInterruptOutcome(t *testing.T) {
	echo := &echoTool{}
	m := &streamModel{events: []model.Event{
		model.TurnMessage{Message: message.Assistant(
			message.NewToolUse("call_1", "echo", json.RawMessage(`{}`)),
		)},
	}}
	rt := makeRuntime(t, m, makeAgent(t, echo), runner.WithPolicy(suspendAllPolicy{}))

	events := collectEvents(t, rt, RunAgentInput{ThreadID: "t1", RunID: "r1"})

	last := events[len(events)-1]
	finished, ok := last.(RunFinishedEvent)
	if !ok {
		t.Fatalf("last event = %T, want RunFinishedEvent", last)
	}
	if finished.Outcome == nil || finished.Outcome.Type != RunFinishedOutcomeInterrupt {
		t.Fatalf("outcome = %+v, want interrupt", finished.Outcome)
	}
	if len(finished.Outcome.Interrupts) != 1 || finished.Outcome.Interrupts[0].ID != "call_1" {
		t.Fatalf("interrupts = %+v", finished.Outcome.Interrupts)
	}
}

func TestRuntimeToolCallAndResultLifecycle(t *testing.T) {
	echo := &echoTool{}
	m := &multiTurnModel{turns: [][]model.Event{
		// Turn 1: request tool call
		{
			model.TurnMessage{Message: message.Assistant(
				message.NewToolUse("call_1", "echo", json.RawMessage(`{"x":1}`)),
			)},
		},
		// Turn 2: final response after tool
		{
			model.TurnMessage{Message: message.Assistant(message.NewText("done"))},
		},
	}}
	rt := makeRuntime(t, m, makeAgent(t, echo))

	events := collectEvents(t, rt, RunAgentInput{ThreadID: "t1", RunID: "r1"})

	var hasToolStart, hasToolArgs, hasToolEnd, hasToolResult bool
	for _, ev := range events {
		switch ev.(type) {
		case ToolCallStartEvent:
			hasToolStart = true
		case ToolCallArgsEvent:
			hasToolArgs = true
		case ToolCallEndEvent:
			hasToolEnd = true
		case ToolCallResultEvent:
			hasToolResult = true
		}
	}
	if !hasToolStart || !hasToolArgs || !hasToolEnd || !hasToolResult {
		t.Fatalf("tool lifecycle incomplete: start=%v args=%v end=%v result=%v", hasToolStart, hasToolArgs, hasToolEnd, hasToolResult)
	}
}

func TestRuntimeCancellationEmitsRunError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	m := &streamModel{events: []model.Event{
		model.TurnMessage{Message: message.Assistant(message.NewText("ok"))},
	}}
	rt := makeRuntime(t, m, makeAgent(t))

	var gotError bool
	for ev, err := range rt.Stream(ctx, RunAgentInput{ThreadID: "t1", RunID: "r1"}) {
		if err != nil {
			gotError = true
			break
		}
		if _, ok := ev.(RunErrorEvent); ok {
			gotError = true
			break
		}
	}
	if !gotError {
		t.Fatal("expected RUN_ERROR or iterator error on cancellation")
	}
}

func TestRuntimeRejectsEmptyThreadID(t *testing.T) {
	m := &streamModel{events: []model.Event{
		model.TurnMessage{Message: message.Assistant(message.NewText("ok"))},
	}}
	rt := makeRuntime(t, m, makeAgent(t))

	var events []Event
	for ev, _ := range rt.Stream(context.Background(), RunAgentInput{RunID: "r1"}) {
		events = append(events, ev)
		break
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
	if _, ok := events[0].(RunErrorEvent); !ok {
		t.Fatalf("events[0] = %T, want RunErrorEvent", events[0])
	}
}

func TestRuntimeConvertMessages(t *testing.T) {
	msgs := []Message{
		{ID: "1", Role: RoleUser, Content: "hello"},
		{ID: "2", Role: RoleAssistant, ToolCalls: []ToolCall{{
			ID:       "call_1",
			Type:     ToolCallTypeFunction,
			Function: FunctionCall{Name: "echo", Arguments: `{"x":1}`},
		}}},
		{ID: "3", Role: RoleTool, ToolCallID: "call_1", Name: "echo", Content: "result"},
	}
	converted, err := convertMessages(msgs)
	if err != nil {
		t.Fatalf("convertMessages error: %v", err)
	}
	if len(converted) != 3 {
		t.Fatalf("len = %d, want 3", len(converted))
	}
	if converted[0].Role != message.RoleUser {
		t.Fatalf("msg[0].Role = %q, want user", converted[0].Role)
	}
	if converted[1].Role != message.RoleAssistant {
		t.Fatalf("msg[1].Role = %q, want assistant", converted[1].Role)
	}
	if converted[2].Role != message.RoleTool {
		t.Fatalf("msg[2].Role = %q, want tool", converted[2].Role)
	}
}

func TestRuntimeGroupsConsecutiveToolResults(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Type: ToolCallTypeFunction, Function: FunctionCall{Name: "echo", Arguments: "{}"}},
			{ID: "c2", Type: ToolCallTypeFunction, Function: FunctionCall{Name: "echo", Arguments: "{}"}},
		}},
		{Role: RoleTool, ToolCallID: "c1", Content: "res1"},
		{Role: RoleTool, ToolCallID: "c2", Content: "res2"},
	}
	converted, err := convertMessages(msgs)
	if err != nil {
		t.Fatalf("convertMessages error: %v", err)
	}
	// assistant message + one grouped tool results message
	if len(converted) != 2 {
		t.Fatalf("len = %d, want 2 (grouped tool results)", len(converted))
	}
	if converted[1].Role != message.RoleTool {
		t.Fatalf("msg[1].Role = %q, want tool", converted[1].Role)
	}
	if len(converted[1].Content) != 2 {
		t.Fatalf("tool result blocks = %d, want 2", len(converted[1].Content))
	}
}

func TestRuntimeConvertMessagesRejectsToolResultWithoutCallID(t *testing.T) {
	msgs := []Message{{Role: RoleTool, Content: "result"}}
	if _, err := convertMessages(msgs); err == nil {
		t.Fatal("convertMessages error = nil, want error")
	}
}

func TestRuntimeConvertMessagesSkipsSystem(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "be helpful"},
		{Role: RoleUser, Content: "hello"},
	}
	converted, err := convertMessages(msgs)
	if err != nil {
		t.Fatalf("convertMessages error: %v", err)
	}
	if len(converted) != 1 {
		t.Fatalf("len = %d, want 1 (system skipped)", len(converted))
	}
}

func TestRuntimeConvertMessagesSkipsDeveloper(t *testing.T) {
	msgs := []Message{
		{Role: RoleDeveloper, Content: "you are a financial assistant"},
		{Role: RoleUser, Content: "hello"},
	}
	converted, err := convertMessages(msgs)
	if err != nil {
		t.Fatalf("convertMessages error: %v", err)
	}
	if len(converted) != 1 {
		t.Fatalf("len = %d, want 1 (developer skipped)", len(converted))
	}
	if converted[0].Role != message.RoleUser {
		t.Fatalf("msg[0].Role = %q, want user", converted[0].Role)
	}
}

func TestBuildSuspendedRunPreservesRunID(t *testing.T) {
	echo := &echoTool{}
	a := makeAgent(t, echo)
	msgs := []message.Message{
		message.UserText("do it"),
		message.Assistant(message.NewToolUse("call_1", "echo", []byte(`{}`))),
	}
	suspended, err := buildSuspendedRun(a, msgs, "run-42")
	if err != nil {
		t.Fatalf("buildSuspendedRun error: %v", err)
	}
	if suspended.RunID != "run-42" {
		t.Fatalf("RunID = %q, want run-42", suspended.RunID)
	}
}

func TestRuntimeResumeApprovalEmitsRunFinished(t *testing.T) {
	echo := &echoTool{}
	a := makeAgent(t, echo)

	// History represents a suspended run: user turn + assistant tool-use with no
	// following tool result (dangling batch), exactly as a suspended Result leaves it.
	suspendedHistory := []Message{
		{Role: RoleUser, Content: "do it"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{
			ID:       "call_1",
			Type:     ToolCallTypeFunction,
			Function: FunctionCall{Name: "echo", Arguments: `{}`},
		}}},
	}

	input := RunAgentInput{
		ThreadID: "thread-1",
		RunID:    "run-1",
		Messages: suspendedHistory,
		Resume: []ResumeEntry{{
			InterruptID: "call_1",
			Status:      ResumeStatusResolved,
		}},
	}

	// ResumeApproved calls model.Generate (not Stream) for post-resume turns.
	m := &streamModel{events: []model.Event{
		model.TurnMessage{Message: message.Assistant(message.NewText("done"))},
	}}
	rt := makeRuntime(t, m, a)

	events := collectEvents(t, rt, input)

	if len(events) < 2 {
		t.Fatalf("events = %d, want >= 2 (RUN_STARTED + RUN_FINISHED)", len(events))
	}
	if _, ok := events[0].(RunStartedEvent); !ok {
		t.Fatalf("events[0] = %T, want RunStartedEvent", events[0])
	}
	last := events[len(events)-1]
	finished, ok := last.(RunFinishedEvent)
	if !ok {
		t.Fatalf("last event = %T, want RunFinishedEvent", last)
	}
	if finished.ThreadID != "thread-1" || finished.RunID != "run-1" {
		t.Fatalf("finished IDs: threadID=%q runID=%q", finished.ThreadID, finished.RunID)
	}
	if finished.Outcome != nil {
		t.Fatalf("outcome = %+v, want nil (success, not interrupt)", finished.Outcome)
	}
}

func TestNewRuntimeRejectsNilRunner(t *testing.T) {
	if _, err := NewRuntime(nil, makeAgent(t)); !errors.Is(err, ErrMissingRunner) {
		t.Fatalf("err = %v, want ErrMissingRunner", err)
	}
}

func TestNewRuntimeRejectsNilAgent(t *testing.T) {
	r, _ := runner.New(&streamModel{events: []model.Event{
		model.TurnMessage{Message: message.Assistant(message.NewText("ok"))},
	}})
	if _, err := NewRuntime(r, nil); !errors.Is(err, ErrMissingAgent) {
		t.Fatalf("err = %v, want ErrMissingAgent", err)
	}
}
