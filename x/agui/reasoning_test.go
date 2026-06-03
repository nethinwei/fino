package agui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
)

func eventTypes(events []Event) []EventType {
	types := make([]EventType, len(events))
	for i, e := range events {
		switch e.(type) {
		case ReasoningStartEvent:
			types[i] = EventReasoningStart
		case ReasoningMessageStartEvent:
			types[i] = EventReasoningMessageStart
		case ReasoningMessageContentEvent:
			types[i] = EventReasoningMessageContent
		case ReasoningMessageEndEvent:
			types[i] = EventReasoningMessageEnd
		case ReasoningEndEvent:
			types[i] = EventReasoningEnd
		case TextMessageStartEvent:
			types[i] = EventTextMessageStart
		case TextMessageContentEvent:
			types[i] = EventTextMessageContent
		case TextMessageEndEvent:
			types[i] = EventTextMessageEnd
		default:
			types[i] = "OTHER"
		}
	}
	return types
}

// TestMapperMapsThinkingBlocksToReasoning pins that a TurnMessage carrying a
// fino thinking block is surfaced as a REASONING_* sequence, emitted before the
// assistant text, and that the reasoning content is exactly the thinking text.
func TestMapperMapsThinkingBlocksToReasoning(t *testing.T) {
	m, err := NewMapper("t1", "r1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}
	msg := message.Assistant(
		message.NewThinking("weighing options"),
		message.NewText("the answer"),
	)
	events, err := m.Map(model.TurnMessage{Message: msg})
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	got := eventTypes(events)
	want := []EventType{
		EventReasoningStart,
		EventReasoningMessageStart,
		EventReasoningMessageContent,
		EventReasoningMessageEnd,
		EventReasoningEnd,
		EventTextMessageStart,
		EventTextMessageContent,
		EventTextMessageEnd,
	}
	if len(got) != len(want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
	content, ok := events[2].(ReasoningMessageContentEvent)
	if !ok {
		t.Fatalf("event[2] type = %T, want ReasoningMessageContentEvent", events[2])
	}
	if content.Delta != "weighing options" {
		t.Fatalf("reasoning delta = %q, want %q", content.Delta, "weighing options")
	}
}

// TestMapperOmitsReasoningWithoutThinking pins success criterion 3: with no
// thinking block, the mapper invents no reasoning events.
func TestMapperOmitsReasoningWithoutThinking(t *testing.T) {
	m, err := NewMapper("t1", "r1")
	if err != nil {
		t.Fatalf("NewMapper error: %v", err)
	}
	msg := message.Assistant(message.NewText("just an answer"))
	events, err := m.Map(model.TurnMessage{Message: msg})
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	for _, e := range events {
		switch e.(type) {
		case ReasoningStartEvent, ReasoningMessageStartEvent,
			ReasoningMessageContentEvent, ReasoningMessageEndEvent, ReasoningEndEvent:
			t.Fatalf("unexpected reasoning event %T with no thinking block", e)
		}
	}
}

// TestMapperMultipleThinkingBlocksGetDistinctMessageIDs pins that each thinking
// block in one turn gets its own REASONING_MESSAGE_START/END lifecycle with a
// unique messageID so the client state machine never sees the same ID opened
// twice without closing.
func TestMapperMultipleThinkingBlocksGetDistinctMessageIDs(t *testing.T) {
	m, err := NewMapper("t1", "r1")
	if err != nil {
		t.Fatalf("NewMapper: %v", err)
	}
	msg := message.Assistant(
		message.NewThinking("first thought"),
		message.NewThinking("second thought"),
	)
	events, err := m.Map(model.TurnMessage{Message: msg})
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	// Expected sequence: START, MSG_START(id-A), CONTENT(id-A), MSG_END(id-A),
	//                          MSG_START(id-B), CONTENT(id-B), MSG_END(id-B), END
	var starts, ends []string
	for _, e := range events {
		switch v := e.(type) {
		case ReasoningMessageStartEvent:
			starts = append(starts, v.MessageID)
		case ReasoningMessageEndEvent:
			ends = append(ends, v.MessageID)
		}
	}
	if len(starts) != 2 {
		t.Fatalf("got %d REASONING_MESSAGE_START, want 2", len(starts))
	}
	if starts[0] == starts[1] {
		t.Fatalf("both REASONING_MESSAGE_START have same messageId %q; each block needs a unique ID", starts[0])
	}
	if len(ends) != 2 {
		t.Fatalf("got %d REASONING_MESSAGE_END, want 2", len(ends))
	}
	if ends[0] == ends[1] {
		t.Fatalf("both REASONING_MESSAGE_END have same messageId %q; each block needs a unique ID", ends[0])
	}
}

func TestReasoningEventsJSONShape(t *testing.T) {
	events := []Event{
		ReasoningStartEvent{BaseEvent: BaseEvent{Type: EventReasoningStart}, MessageID: "r_1"},
		ReasoningMessageStartEvent{BaseEvent: BaseEvent{Type: EventReasoningMessageStart}, MessageID: "r_1", Role: "reasoning"},
		ReasoningMessageContentEvent{BaseEvent: BaseEvent{Type: EventReasoningMessageContent}, MessageID: "r_1", Delta: "step"},
		ReasoningMessageEndEvent{BaseEvent: BaseEvent{Type: EventReasoningMessageEnd}, MessageID: "r_1"},
		ReasoningEndEvent{BaseEvent: BaseEvent{Type: EventReasoningEnd}, MessageID: "r_1"},
	}
	data, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"type":"REASONING_START"`,
		`"type":"REASONING_MESSAGE_START"`,
		`"role":"reasoning"`,
		`"type":"REASONING_MESSAGE_CONTENT"`,
		`"delta":"step"`,
		`"type":"REASONING_MESSAGE_END"`,
		`"type":"REASONING_END"`,
		`"messageId":"r_1"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}
