package agui

import (
	"encoding/json"
	"fmt"
)

// Lineage carries the adapter-owned identifiers that correlate one run to its
// thread and optional parent. These IDs are set by the adapter, not the core,
// and they survive serialization without modification.
type Lineage struct {
	ThreadID    string  `json:"threadId"`
	RunID       string  `json:"runId"`
	ParentRunID *string `json:"parentRunId,omitempty"`
}

// MarshalEventLog encodes a slice of AG-UI events as a JSON array. Each event
// is encoded with its discriminating "type" field so UnmarshalEventLog can
// restore the concrete Go type.
func MarshalEventLog(events []Event) ([]byte, error) {
	return json.Marshal(events)
}

// rawEvent is the minimal representation used during unmarshal to read the
// discriminating type field before decoding the full event.
type rawEvent struct {
	Type EventType       `json:"type"`
	Raw  json.RawMessage // full JSON object
}

func (r *rawEvent) UnmarshalJSON(data []byte) error {
	r.Raw = data
	var probe struct {
		Type EventType `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	r.Type = probe.Type
	return nil
}

// UnmarshalEventLog decodes a JSON array produced by MarshalEventLog back into
// typed Event values. Unknown event types are wrapped in RawEvent so the caller
// can handle forward-compatibility gracefully without an error.
func UnmarshalEventLog(data []byte) ([]Event, error) {
	var raws []rawEvent
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, fmt.Errorf("unmarshal event log: %w", err)
	}
	events := make([]Event, len(raws))
	for i, r := range raws {
		ev, err := unmarshalEvent(r.Type, r.Raw)
		if err != nil {
			return nil, fmt.Errorf("event[%d] (%s): %w", i, r.Type, err)
		}
		events[i] = ev
	}
	return events, nil
}

func unmarshalEvent(t EventType, raw json.RawMessage) (Event, error) {
	switch t {
	case EventRunStarted:
		return decode[RunStartedEvent](raw)
	case EventRunFinished:
		return decode[RunFinishedEvent](raw)
	case EventRunError:
		return decode[RunErrorEvent](raw)
	case EventStepStarted:
		return decode[StepStartedEvent](raw)
	case EventStepFinished:
		return decode[StepFinishedEvent](raw)
	case EventTextMessageStart:
		return decode[TextMessageStartEvent](raw)
	case EventTextMessageContent:
		return decode[TextMessageContentEvent](raw)
	case EventTextMessageEnd:
		return decode[TextMessageEndEvent](raw)
	case EventToolCallStart:
		return decode[ToolCallStartEvent](raw)
	case EventToolCallArgs:
		return decode[ToolCallArgsEvent](raw)
	case EventToolCallEnd:
		return decode[ToolCallEndEvent](raw)
	case EventToolCallResult:
		return decode[ToolCallResultEvent](raw)
	case EventMessagesSnapshot:
		return decode[MessagesSnapshotEvent](raw)
	case EventStateSnapshot:
		return decode[StateSnapshotEvent](raw)
	case EventStateDelta:
		return decode[StateDeltaEvent](raw)
	case EventActivitySnapshot:
		return decode[ActivitySnapshotEvent](raw)
	case EventActivityDelta:
		return decode[ActivityDeltaEvent](raw)
	case EventReasoningStart:
		return decode[ReasoningStartEvent](raw)
	case EventReasoningMessageStart:
		return decode[ReasoningMessageStartEvent](raw)
	case EventReasoningMessageContent:
		return decode[ReasoningMessageContentEvent](raw)
	case EventReasoningMessageEnd:
		return decode[ReasoningMessageEndEvent](raw)
	case EventReasoningEnd:
		return decode[ReasoningEndEvent](raw)
	case EventCustom:
		return decode[CustomEvent](raw)
	case EventRaw:
		return decode[RawEvent](raw)
	default:
		// Unknown type: wrap in RawEvent for forward-compatibility.
		return RawEvent{
			BaseEvent: BaseEvent{Type: t},
			Event:     json.RawMessage(raw),
		}, nil
	}
}

func decode[T Event](raw json.RawMessage) (Event, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// Compact reduces an event log to the latest MESSAGES_SNAPSHOT and all events
// that follow it. If no snapshot is present the full slice is returned. This
// lets clients discard replayed history while preserving the current message
// state and subsequent events. Compact does not introduce a session store; the
// caller owns the resulting slice. The returned slice is always a fresh copy so
// mutations to its elements do not alias back into the original events slice.
func Compact(events []Event) []Event {
	lastSnap := -1
	for i, e := range events {
		if _, ok := e.(MessagesSnapshotEvent); ok {
			lastSnap = i
		}
	}
	src := events
	if lastSnap >= 0 {
		src = events[lastSnap:]
	}
	out := make([]Event, len(src))
	copy(out, src)
	return out
}
