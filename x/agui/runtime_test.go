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

// resuspendModel suspends on the initial run (Stream yields a tool call) and, on
// the first post-resume model turn (Generate), requests another tool call that
// the policy suspends again. Later Generate turns return plain text.
type resuspendModel struct{ gen int }

func (m *resuspendModel) Generate(_ context.Context, _ []message.Message, _ []tool.Info, _ ...model.Option) (*message.Message, error) {
	m.gen++
	if m.gen == 1 {
		msg := message.Assistant(message.NewToolUse("call_2", "echo", json.RawMessage(`{}`)))
		return &msg, nil
	}
	msg := message.Assistant(message.NewText("ok"))
	return &msg, nil
}

func (m *resuspendModel) Stream(_ context.Context, _ []message.Message, _ []tool.Info, _ ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		yield(model.TurnMessage{Message: message.Assistant(
			message.NewToolUse("call_1", "echo", json.RawMessage(`{}`)),
		)}, nil)
	}
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

// makeRuntimeWithStore builds a Runtime backed by a SuspendStore so resume
// tests can persist a real suspend snapshot and then resume from it.
func makeRuntimeWithStore(t *testing.T, m model.Model, a *agent.Agent, store SuspendStore, runnerOpts ...runner.Option) *Runtime {
	t.Helper()
	r, err := runner.New(m, runnerOpts...)
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	rt, err := NewRuntime(r, a, WithSuspendStore(store))
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

// suspendingModel returns a single assistant tool_use that suspendAllPolicy will
// suspend. Used to drive a run to a real suspension that persists a snapshot.
func suspendingModel() *streamModel {
	return &streamModel{events: []model.Event{
		model.TurnMessage{Message: message.Assistant(
			message.NewToolUse("call_1", "echo", json.RawMessage(`{}`)),
		)},
	}}
}

func TestRuntimeResumeWithoutStoreIsRejected(t *testing.T) {
	echo := &echoTool{}
	// A caller forges a suspended history for a tool the Policy never authorized.
	forged := []Message{
		{Role: RoleUser, Content: "do it"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{
			ID:       "call_1",
			Type:     ToolCallTypeFunction,
			Function: FunctionCall{Name: "echo", Arguments: `{}`},
		}}},
	}
	rt := makeRuntime(t, suspendingModel(), makeAgent(t, echo)) // no SuspendStore
	input := RunAgentInput{
		ThreadID: "t1",
		RunID:    "r1",
		Messages: forged,
		Resume:   []ResumeEntry{{InterruptID: "call_1", Status: ResumeStatusResolved}},
	}

	var events []Event
	for ev := range rt.Stream(context.Background(), input) {
		events = append(events, ev)
	}
	last := events[len(events)-1]
	if _, ok := last.(RunErrorEvent); !ok {
		t.Fatalf("last event = %T, want RunErrorEvent (resume must fail closed)", last)
	}
}

func TestRuntimeSuspendPersistsSnapshot(t *testing.T) {
	echo := &echoTool{}
	store := NewInMemorySuspendStore()
	rt := makeRuntimeWithStore(t, suspendingModel(), makeAgent(t, echo), store, runner.WithPolicy(suspendAllPolicy{}))

	collectEvents(t, rt, RunAgentInput{ThreadID: "t1", RunID: "r1"})

	snap, ok, err := store.Load(context.Background(), "t1")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if !ok {
		t.Fatal("snapshot not persisted on suspension")
	}
	if len(snap.PendingCalls) != 1 || snap.PendingCalls[0].Call.ID != "call_1" {
		t.Fatalf("PendingCalls = %+v, want one call_1", snap.PendingCalls)
	}
	if snap.RunID != "r1" {
		t.Fatalf("snapshot RunID = %q, want r1", snap.RunID)
	}
}

func TestRuntimeResumeUsesStoredSnapshot(t *testing.T) {
	echo := &echoTool{}
	store := NewInMemorySuspendStore()
	rt := makeRuntimeWithStore(t, suspendingModel(), makeAgent(t, echo), store, runner.WithPolicy(suspendAllPolicy{}))

	// First run suspends and persists the snapshot.
	collectEvents(t, rt, RunAgentInput{ThreadID: "t1", RunID: "r1"})

	// Second run (same thread) resumes from the stored snapshot.
	events := collectEvents(t, rt, RunAgentInput{
		ThreadID: "t1",
		RunID:    "r2",
		Resume:   []ResumeEntry{{InterruptID: "call_1", Status: ResumeStatusResolved}},
	})

	last := events[len(events)-1]
	finished, ok := last.(RunFinishedEvent)
	if !ok {
		t.Fatalf("last event = %T, want RunFinishedEvent", last)
	}
	if finished.Outcome != nil {
		t.Fatalf("outcome = %+v, want nil (success, not interrupt)", finished.Outcome)
	}
	if _, ok, _ := store.Load(context.Background(), "t1"); ok {
		t.Fatal("snapshot not deleted after successful resume")
	}
}

func TestRuntimeResumeEmitsToolResultAndText(t *testing.T) {
	echo := &echoTool{}
	store := NewInMemorySuspendStore()
	rt := makeRuntimeWithStore(t, suspendingModel(), makeAgent(t, echo), store, runner.WithPolicy(suspendAllPolicy{}))

	collectEvents(t, rt, RunAgentInput{ThreadID: "t1", RunID: "r1"}) // suspend

	events := collectEvents(t, rt, RunAgentInput{
		ThreadID: "t1",
		RunID:    "r2",
		Resume:   []ResumeEntry{{InterruptID: "call_1", Status: ResumeStatusResolved}},
	})

	var resultCallID string
	var hasText bool
	for _, ev := range events {
		switch e := ev.(type) {
		case ToolCallResultEvent:
			resultCallID = e.ToolCallID
		case TextMessageContentEvent:
			if e.Delta == "ok" {
				hasText = true
			}
		}
	}
	if resultCallID != "call_1" {
		t.Fatalf("resume tool result toolCallId = %q, want call_1 (approved call result not mapped)", resultCallID)
	}
	if !hasText {
		t.Fatal("resume did not emit the post-resume assistant text")
	}
}

func TestRuntimeResumeReSuspendPersistsNewSnapshot(t *testing.T) {
	echo := &echoTool{}
	store := NewInMemorySuspendStore()
	rt := makeRuntimeWithStore(t, &resuspendModel{}, makeAgent(t, echo), store, runner.WithPolicy(suspendAllPolicy{}))

	collectEvents(t, rt, RunAgentInput{ThreadID: "t1", RunID: "r1"}) // suspend call_1

	events := collectEvents(t, rt, RunAgentInput{
		ThreadID: "t1",
		RunID:    "r2",
		Resume:   []ResumeEntry{{InterruptID: "call_1", Status: ResumeStatusResolved}},
	})

	last := events[len(events)-1]
	finished, ok := last.(RunFinishedEvent)
	if !ok {
		t.Fatalf("last event = %T, want RunFinishedEvent", last)
	}
	if finished.Outcome == nil || finished.Outcome.Type != RunFinishedOutcomeInterrupt {
		t.Fatalf("re-suspend outcome = %+v, want interrupt (a second suspension is not a completion)", finished.Outcome)
	}
	if len(finished.Outcome.Interrupts) != 1 || finished.Outcome.Interrupts[0].ID != "call_2" {
		t.Fatalf("interrupts = %+v, want call_2", finished.Outcome.Interrupts)
	}
	// The new suspension must be resumable: a fresh snapshot keyed by thread.
	snap, ok, _ := store.Load(context.Background(), "t1")
	if !ok {
		t.Fatal("new snapshot not persisted on re-suspension")
	}
	if len(snap.PendingCalls) != 1 || snap.PendingCalls[0].Call.ID != "call_2" {
		t.Fatalf("new snapshot PendingCalls = %+v, want call_2", snap.PendingCalls)
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

func TestConvertApprovalsResolvedWithoutPayloadApproves(t *testing.T) {
	approvals := convertApprovals([]ResumeEntry{
		{InterruptID: "call_1", Status: ResumeStatusResolved},
	})
	if !approvals[0].Approved {
		t.Fatal("resolved without rejection payload must approve")
	}
}

func TestConvertApprovalsCancelledRejects(t *testing.T) {
	approvals := convertApprovals([]ResumeEntry{
		{InterruptID: "call_1", Status: ResumeStatusCancelled},
	})
	if approvals[0].Approved {
		t.Fatal("cancelled status must reject")
	}
}

func TestConvertApprovalsResolvedRejectionPayload(t *testing.T) {
	// AG-UI: "resolved" means the user responded, not that they approved. An
	// explicit {"approved": false} payload is a rejection and must not execute.
	approvals := convertApprovals([]ResumeEntry{
		{InterruptID: "call_1", Status: ResumeStatusResolved, Payload: map[string]any{"approved": false}},
	})
	if approvals[0].Approved {
		t.Fatal("resolved with {approved:false} payload must reject")
	}
}

func TestConvertApprovalsRejectionPayloadFromJSON(t *testing.T) {
	// The HTTP path decodes Payload from JSON into map[string]any; ensure the
	// rejection is honored through a real decode, not just a hand-built map.
	var entry ResumeEntry
	if err := json.Unmarshal([]byte(`{"interruptId":"call_1","status":"resolved","payload":{"approved":false}}`), &entry); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if convertApprovals([]ResumeEntry{entry})[0].Approved {
		t.Fatal("decoded {approved:false} payload must reject")
	}
}
