package agui

import (
	"encoding/json"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/tool"
)

func TestMapperMapsTextLifecycle(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	first := mustMap(t, m, model.TextDelta{Text: "hel"})
	second := mustMap(t, m, model.TextDelta{Text: "lo"})
	end := mustMap(t, m, model.TurnMessage{Message: message.Assistant(message.NewText("hello"))})

	start, ok := first[0].(TextMessageStartEvent)
	if !ok {
		t.Fatalf("first event = %T, want TextMessageStartEvent", first[0])
	}
	content1 := first[1].(TextMessageContentEvent)
	content2 := second[0].(TextMessageContentEvent)
	stop := end[0].(TextMessageEndEvent)
	if start.MessageID == "" {
		t.Fatal("start message ID is empty")
	}
	if content1.MessageID != start.MessageID || content2.MessageID != start.MessageID || stop.MessageID != start.MessageID {
		t.Fatalf("message IDs = %q, %q, %q, %q; want stable ID", start.MessageID, content1.MessageID, content2.MessageID, stop.MessageID)
	}
}

func TestMapperIgnoresEmptyTextDelta(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	events := mustMap(t, m, model.TextDelta{})
	if len(events) != 0 {
		t.Fatalf("events = %d, want 0", len(events))
	}
}

func TestMapperCompletesMissingTextDeltaSuffix(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	first := mustMap(t, m, model.TextDelta{Text: "hel"})
	end := mustMap(t, m, model.TurnMessage{Message: message.Assistant(message.NewText("hello"))})
	start := first[0].(TextMessageStartEvent)
	suffix := end[0].(TextMessageContentEvent)
	stop := end[1].(TextMessageEndEvent)
	if suffix.MessageID != start.MessageID || stop.MessageID != start.MessageID {
		t.Fatalf("message IDs = %q, %q, %q; want stable ID", start.MessageID, suffix.MessageID, stop.MessageID)
	}
	if suffix.Delta != "lo" {
		t.Fatalf("suffix = %q, want lo", suffix.Delta)
	}
}

func TestMapperRejectsTextDeltaMismatch(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	mustMap(t, m, model.TextDelta{Text: "hello"})
	if _, err := m.Map(model.TurnMessage{Message: message.Assistant(message.NewText("goodbye"))}); err == nil {
		t.Fatal("Map error = nil, want error")
	}
}

func TestMapperMapsTextFromTurnMessageWithoutDeltas(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	events := mustMap(t, m, model.TurnMessage{Message: message.Assistant(message.NewText("hello"))})
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	start := events[0].(TextMessageStartEvent)
	content := events[1].(TextMessageContentEvent)
	end := events[2].(TextMessageEndEvent)
	if start.MessageID == "" || content.MessageID != start.MessageID || end.MessageID != start.MessageID {
		t.Fatalf("message IDs = %q, %q, %q; want stable ID", start.MessageID, content.MessageID, end.MessageID)
	}
	if content.Delta != "hello" {
		t.Fatalf("delta = %q, want hello", content.Delta)
	}
}

func TestMapperMapsEmptyToolInputAsObject(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}
	call := message.NewToolUse("call_1", "search", nil)

	events := mustMap(t, m, model.TurnMessage{Message: message.Assistant(call)})
	args := events[1].(ToolCallArgsEvent)
	if args.Delta != "{}" {
		t.Fatalf("args delta = %q, want {}", args.Delta)
	}
}

func TestMapperRejectsInvalidToolCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		call message.ToolUse
	}{
		{name: "empty ID", call: message.ToolUse{Name: "search", Input: json.RawMessage(`{}`)}},
		{name: "empty name", call: message.ToolUse{ID: "call_1", Input: json.RawMessage(`{}`)}},
		{name: "invalid input", call: message.ToolUse{ID: "call_1", Name: "search", Input: json.RawMessage(`{`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewMapper("thread_1", "run_1")
			if err != nil {
				t.Fatalf("NewMapper error: %v", err)
			}
			if _, err := m.Map(model.TurnMessage{Message: message.Assistant(message.Block{
				Type:  message.TypeToolUse,
				ID:    tc.call.ID,
				Name:  tc.call.Name,
				Input: tc.call.Input,
			})}); err == nil {
				t.Fatal("Map error = nil, want error")
			}
		})
	}
}

func TestMapperRejectsDuplicateToolCallID(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}
	call := message.NewToolUse("call_1", "search", json.RawMessage(`{}`))

	if _, err := m.Map(model.TurnMessage{Message: message.Assistant(call, call)}); err == nil {
		t.Fatal("Map error = nil, want error")
	}
}

func TestMapperRejectsNonAssistantTurnMessage(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	if _, err := m.Map(model.TurnMessage{Message: message.UserText("hello")}); err == nil {
		t.Fatal("Map error = nil, want error")
	}
}

func TestMapperRejectsUnsupportedAssistantBlock(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}
	msg := message.Assistant(message.NewToolResult("call_1", "search", nil, false))

	if _, err := m.Map(model.TurnMessage{Message: msg}); err == nil {
		t.Fatal("Map error = nil, want error")
	}
}

func TestMapperKeepsTextOpenWhenToolCallValidationFails(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}
	first := mustMap(t, m, model.TextDelta{Text: "partial"})
	invalid := message.Block{Type: message.TypeToolUse, Name: "search", Input: json.RawMessage(`{}`)}

	if _, err := m.Map(model.TurnMessage{Message: message.Assistant(message.NewText("partial"), invalid)}); err == nil {
		t.Fatal("Map error = nil, want error")
	}
	failed := mustMap(t, m, model.StreamError{Err: errTest})
	start := first[0].(TextMessageStartEvent)
	end := failed[0].(TextMessageEndEvent)
	if end.MessageID != start.MessageID {
		t.Fatalf("end message ID = %q, want %q", end.MessageID, start.MessageID)
	}
}

func TestMapperMapsToolCallAndResult(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}
	call := message.NewToolUse("call_1", "search", json.RawMessage(`{"query":"go"}`))

	events := mustMap(t, m, model.TurnMessage{Message: message.Assistant(call)})
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	start := events[0].(ToolCallStartEvent)
	args := events[1].(ToolCallArgsEvent)
	end := events[2].(ToolCallEndEvent)
	if start.ToolCallID != "call_1" || start.ToolCallName != "search" || start.ParentMessageID != nil {
		t.Fatalf("start = %+v", start)
	}
	if args.ToolCallID != "call_1" || args.Delta != `{"query":"go"}` {
		t.Fatalf("args = %+v", args)
	}
	if end.ToolCallID != "call_1" {
		t.Fatalf("end = %+v", end)
	}

	resultEvents := mustMap(t, m, model.ToolResult{
		CallID: "call_1",
		Name:   "search",
		Result: tool.Result{Content: []message.Block{message.NewText("done")}},
	})
	result := resultEvents[0].(ToolCallResultEvent)
	if result.ToolCallID != "call_1" || result.MessageID == "" || result.Content != "done" || result.Role == nil || *result.Role != string(RoleTool) {
		t.Fatalf("result = %+v", result)
	}
}

func TestMapperRejectsToolResultWithoutCallID(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	if _, err := m.Map(model.ToolResult{}); err == nil {
		t.Fatal("Map error = nil, want error")
	}
}

func TestMapperMapsRunTerminalEvents(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	finished := mustMap(t, m, model.FinalMessage{Message: message.Assistant(message.NewText("done"))})
	gotFinished := finished[0].(RunFinishedEvent)
	if gotFinished.ThreadID != "thread_1" || gotFinished.RunID != "run_1" {
		t.Fatalf("finished = %+v", gotFinished)
	}

	failed := mustMap(t, m, model.StreamError{Err: errTest})
	gotFailed := failed[0].(RunErrorEvent)
	if gotFailed.Message != errTest.Error() {
		t.Fatalf("error message = %q, want %q", gotFailed.Message, errTest.Error())
	}
	if gotFailed.RunID != "run_1" {
		t.Fatalf("error run ID = %q, want run_1", gotFailed.RunID)
	}
}

func TestMapperClosesTextBeforeRunError(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	first := mustMap(t, m, model.TextDelta{Text: "partial"})
	failed := mustMap(t, m, model.StreamError{Err: errTest})
	start := first[0].(TextMessageStartEvent)
	end := failed[0].(TextMessageEndEvent)
	runErr := failed[1].(RunErrorEvent)
	if end.MessageID != start.MessageID {
		t.Fatalf("end message ID = %q, want %q", end.MessageID, start.MessageID)
	}
	if runErr.Message != errTest.Error() {
		t.Fatalf("error message = %q, want %q", runErr.Message, errTest.Error())
	}
}

func TestMapperMapsSuspensionAsInterruptOutcome(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	events := mustMap(t, m, model.Suspended{PendingCalls: []model.SuspendedCall{{
		Tool:   tool.Info{Name: "write_file"},
		Call:   message.ToolUse{ID: "call_1", Name: "write_file"},
		Reason: "approval required",
	}}})
	finished := events[0].(RunFinishedEvent)
	if finished.Outcome == nil || finished.Outcome.Type != RunFinishedOutcomeInterrupt {
		t.Fatalf("outcome = %+v, want interrupt", finished.Outcome)
	}
	if len(finished.Outcome.Interrupts) != 1 {
		t.Fatalf("interrupts = %d, want 1", len(finished.Outcome.Interrupts))
	}
	interrupt := finished.Outcome.Interrupts[0]
	if interrupt.ID != "call_1" || interrupt.ToolCallID != "call_1" || interrupt.Message != "approval required" {
		t.Fatalf("interrupt = %+v", interrupt)
	}
}

func TestMapperRejectsSuspensionWithoutPendingCalls(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	if _, err := m.Map(model.Suspended{}); err == nil {
		t.Fatal("Map error = nil, want error")
	}
}

func TestMapperRejectsSuspensionWithEmptyCallID(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	if _, err := m.Map(model.Suspended{PendingCalls: []model.SuspendedCall{{}}}); err == nil {
		t.Fatal("Map error = nil, want error")
	}
}

func TestMapperRejectsSuspensionWithDuplicateCallID(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}
	pending := model.SuspendedCall{Call: message.ToolUse{ID: "call_1"}}

	if _, err := m.Map(model.Suspended{PendingCalls: []model.SuspendedCall{pending, pending}}); err == nil {
		t.Fatal("Map error = nil, want error")
	}
}

func TestMapperRejectsNilStreamError(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	if _, err := m.Map(model.StreamError{}); err == nil {
		t.Fatal("Map error = nil, want error")
	}
}

func TestMapperRejectsEmptyStreamErrorMessage(t *testing.T) {
	m, err := NewMapper("thread_1", "run_1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}

	if _, err := m.Map(model.StreamError{Err: &testError{}}); err == nil {
		t.Fatal("Map error = nil, want error")
	}
}

func TestToolResultContentDoesNotSilentlyBecomeEmpty(t *testing.T) {
	block := message.Block{Type: message.TypeText, Text: "ok"}
	block.Content = []message.Block{{Type: message.TypeText, Text: string([]byte{0xff})}}

	if got := toolResultContent([]message.Block{block}); got == "" {
		t.Fatal("toolResultContent returned empty content")
	}
}

func TestToolResultContentIsNonEmptyForEmptyResult(t *testing.T) {
	if got := toolResultContent(nil); got == "" {
		t.Fatal("toolResultContent returned empty content")
	}
	if got := toolResultContent([]message.Block{message.NewText("")}); got == "" {
		t.Fatal("toolResultContent returned empty text content")
	}
}

func TestNewMapperRejectsEmptyIDs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		threadID string
		runID    string
	}{
		{name: "thread", runID: "run_1"},
		{name: "run", threadID: "thread_1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewMapper(tc.threadID, tc.runID); err == nil {
				t.Fatal("NewMapper error = nil, want error")
			}
		})
	}
}

func mustMap(t *testing.T, m *Mapper, ev model.Event) []Event {
	t.Helper()
	events, err := m.Map(ev)
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	return events
}

var errTest = &testError{"boom"}

type testError struct{ text string }

func (e *testError) Error() string { return e.text }
