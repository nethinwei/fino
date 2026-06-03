package agui

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEventLogRoundTrip pins success criterion 1: serialised events restore
// their concrete Go types so consumers can type-switch on the decoded values.
func TestEventLogRoundTrip(t *testing.T) {
	events := []Event{
		RunStartedEvent{BaseEvent: BaseEvent{Type: EventRunStarted}, ThreadID: "t1", RunID: "r1"},
		TextMessageStartEvent{BaseEvent: BaseEvent{Type: EventTextMessageStart}, MessageID: "m1"},
		TextMessageContentEvent{BaseEvent: BaseEvent{Type: EventTextMessageContent}, MessageID: "m1", Delta: "hello"},
		TextMessageEndEvent{BaseEvent: BaseEvent{Type: EventTextMessageEnd}, MessageID: "m1"},
		ToolCallStartEvent{BaseEvent: BaseEvent{Type: EventToolCallStart}, ToolCallID: "c1", ToolCallName: "search"},
		ToolCallArgsEvent{BaseEvent: BaseEvent{Type: EventToolCallArgs}, ToolCallID: "c1", Delta: `{}`},
		ToolCallEndEvent{BaseEvent: BaseEvent{Type: EventToolCallEnd}, ToolCallID: "c1"},
		ToolCallResultEvent{BaseEvent: BaseEvent{Type: EventToolCallResult}, ToolCallID: "c1", Content: "ok"},
		StateSnapshotEvent{BaseEvent: BaseEvent{Type: EventStateSnapshot}, Snapshot: map[string]any{"n": float64(1)}},
		StateDeltaEvent{BaseEvent: BaseEvent{Type: EventStateDelta}, Delta: []JSONPatchOp{{Op: "replace", Path: "/n", Value: float64(2)}}},
		ReasoningStartEvent{BaseEvent: BaseEvent{Type: EventReasoningStart}, MessageID: "reasoning_1"},
		ReasoningEndEvent{BaseEvent: BaseEvent{Type: EventReasoningEnd}, MessageID: "reasoning_1"},
		CustomEvent{BaseEvent: BaseEvent{Type: EventCustom}, Name: "done", Value: nil},
		RunFinishedEvent{BaseEvent: BaseEvent{Type: EventRunFinished}, ThreadID: "t1", RunID: "r1"},
	}

	data, err := MarshalEventLog(events)
	if err != nil {
		t.Fatalf("MarshalEventLog: %v", err)
	}

	decoded, err := UnmarshalEventLog(data)
	if err != nil {
		t.Fatalf("UnmarshalEventLog: %v", err)
	}

	if len(decoded) != len(events) {
		t.Fatalf("decoded %d events, want %d", len(decoded), len(events))
	}
	for i, orig := range events {
		if decoded[i] == nil {
			t.Fatalf("decoded[%d] is nil, want %T", i, orig)
		}
		// Type assertions verify the concrete type was restored.
		switch orig.(type) {
		case RunStartedEvent:
			if _, ok := decoded[i].(RunStartedEvent); !ok {
				t.Fatalf("decoded[%d] = %T, want RunStartedEvent", i, decoded[i])
			}
		case TextMessageStartEvent:
			if _, ok := decoded[i].(TextMessageStartEvent); !ok {
				t.Fatalf("decoded[%d] = %T, want TextMessageStartEvent", i, decoded[i])
			}
		case TextMessageContentEvent:
			v, ok := decoded[i].(TextMessageContentEvent)
			if !ok {
				t.Fatalf("decoded[%d] = %T, want TextMessageContentEvent", i, decoded[i])
			}
			if v.Delta != "hello" {
				t.Fatalf("decoded[%d] delta = %q, want %q", i, v.Delta, "hello")
			}
		case ToolCallStartEvent:
			v, ok := decoded[i].(ToolCallStartEvent)
			if !ok {
				t.Fatalf("decoded[%d] = %T, want ToolCallStartEvent", i, decoded[i])
			}
			if v.ToolCallName != "search" {
				t.Fatalf("decoded[%d] toolCallName = %q, want %q", i, v.ToolCallName, "search")
			}
		case StateSnapshotEvent:
			if _, ok := decoded[i].(StateSnapshotEvent); !ok {
				t.Fatalf("decoded[%d] = %T, want StateSnapshotEvent", i, decoded[i])
			}
		case ReasoningStartEvent:
			if _, ok := decoded[i].(ReasoningStartEvent); !ok {
				t.Fatalf("decoded[%d] = %T, want ReasoningStartEvent", i, decoded[i])
			}
		case RunFinishedEvent:
			v, ok := decoded[i].(RunFinishedEvent)
			if !ok {
				t.Fatalf("decoded[%d] = %T, want RunFinishedEvent", i, decoded[i])
			}
			if v.ThreadID != "t1" {
				t.Fatalf("decoded[%d] threadId = %q, want t1", i, v.ThreadID)
			}
		}
	}
}

// TestUnmarshalEventLogUnknownTypeProducesRawEvent pins that unknown event
// types in a log do not cause UnmarshalEventLog to fail; instead they come back
// as RawEvent so the caller can decide how to handle them.
func TestUnmarshalEventLogUnknownTypeProducesRawEvent(t *testing.T) {
	raw := `[{"type":"FUTURE_EVENT","someField":"x"}]`
	events, err := UnmarshalEventLog([]byte(raw))
	if err != nil {
		t.Fatalf("UnmarshalEventLog: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if _, ok := events[0].(RawEvent); !ok {
		t.Fatalf("unknown type decoded as %T, want RawEvent", events[0])
	}
}

// TestLineageFieldsAreAdapterOwned pins success criterion 2: threadId and
// runId survive a JSON round-trip without modification.
func TestLineageFieldsAreAdapterOwned(t *testing.T) {
	l := Lineage{ThreadID: "thread-abc", RunID: "run-xyz", ParentRunID: stringPtr("run-parent")}
	data, err := json.Marshal(l)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"threadId":"thread-abc"`,
		`"runId":"run-xyz"`,
		`"parentRunId":"run-parent"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
	var back Lineage
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if back.ThreadID != l.ThreadID || back.RunID != l.RunID {
		t.Fatalf("round-trip: got %+v, want %+v", back, l)
	}
}

// TestCompactExtractsLatestMessagesSnapshot pins success criterion 4: Compact
// returns only the slice of events after the last MESSAGES_SNAPSHOT, which is
// also included, and does not introduce a session store.
func TestCompactExtractsLatestMessagesSnapshot(t *testing.T) {
	snap1 := MessagesSnapshotEvent{
		BaseEvent: BaseEvent{Type: EventMessagesSnapshot},
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}
	snap2 := MessagesSnapshotEvent{
		BaseEvent: BaseEvent{Type: EventMessagesSnapshot},
		Messages:  []Message{{Role: RoleUser, Content: "hi"}, {Role: RoleAssistant, Content: "hello"}},
	}
	started := RunStartedEvent{BaseEvent: BaseEvent{Type: EventRunStarted}, ThreadID: "t1", RunID: "r2"}
	finished := RunFinishedEvent{BaseEvent: BaseEvent{Type: EventRunFinished}, ThreadID: "t1", RunID: "r2"}

	events := []Event{
		RunStartedEvent{BaseEvent: BaseEvent{Type: EventRunStarted}, ThreadID: "t1", RunID: "r1"},
		snap1,
		RunFinishedEvent{BaseEvent: BaseEvent{Type: EventRunFinished}, ThreadID: "t1", RunID: "r1"},
		started,
		snap2,
		finished,
	}

	got := Compact(events)
	// Expect: snap2, started (before snap2 is dropped), snap2 retained, finished
	// Compact keeps the latest snapshot and everything after it.
	if len(got) == 0 {
		t.Fatal("Compact returned empty slice")
	}
	// First event must be the latest snapshot.
	if _, ok := got[0].(MessagesSnapshotEvent); !ok {
		t.Fatalf("Compact[0] = %T, want MessagesSnapshotEvent", got[0])
	}
	snap, _ := got[0].(MessagesSnapshotEvent)
	if len(snap.Messages) != 2 {
		t.Fatalf("compact snapshot has %d messages, want 2", len(snap.Messages))
	}
	// Last event must be RunFinished.
	last := got[len(got)-1]
	if _, ok := last.(RunFinishedEvent); !ok {
		t.Fatalf("Compact last = %T, want RunFinishedEvent", last)
	}
}

// TestCompactDoesNotShareBackingArray pins that mutating the returned slice does
// not alias back into the original events slice.
func TestCompactDoesNotShareBackingArray(t *testing.T) {
	snap := MessagesSnapshotEvent{
		BaseEvent: BaseEvent{Type: EventMessagesSnapshot},
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}
	fin := RunFinishedEvent{BaseEvent: BaseEvent{Type: EventRunFinished}}
	events := []Event{snap, fin}

	got := Compact(events)
	if len(got) == 0 {
		t.Fatal("Compact returned empty")
	}
	// Replace the first element in the compacted slice.
	got[0] = RunStartedEvent{BaseEvent: BaseEvent{Type: EventRunStarted}}
	// The original events slice must not be affected.
	if _, ok := events[0].(RunStartedEvent); ok {
		t.Fatal("Compact returned a sub-slice that aliases the original; mutation leaked back")
	}
}

// TestCompactNoSnapshotReturnsAll pins that Compact with no MESSAGES_SNAPSHOT
// returns the full event slice unchanged.
func TestCompactNoSnapshotReturnsAll(t *testing.T) {
	events := []Event{
		RunStartedEvent{BaseEvent: BaseEvent{Type: EventRunStarted}},
		RunFinishedEvent{BaseEvent: BaseEvent{Type: EventRunFinished}},
	}
	got := Compact(events)
	if len(got) != len(events) {
		t.Fatalf("Compact without snapshot: got %d events, want %d", len(got), len(events))
	}
}
