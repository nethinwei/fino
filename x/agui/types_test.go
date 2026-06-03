package agui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunAgentInputJSONShape(t *testing.T) {
	parent := "run_parent"
	input := RunAgentInput{
		ThreadID:    "thread_1",
		RunID:       "run_1",
		ParentRunID: &parent,
		State:       map[string]any{"count": float64(1)},
		Messages: []Message{{
			ID:      "msg_1",
			Role:    RoleUser,
			Content: "hello",
		}},
		Tools: []Tool{{
			Name:        "search",
			Description: "Search the web",
			Parameters:  map[string]any{"type": "object"},
		}},
		Context:        []Context{{Description: "locale", Value: "en-US"}},
		ForwardedProps: map[string]any{"tenant": "acme"},
	}

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"threadId":"thread_1"`,
		`"runId":"run_1"`,
		`"parentRunId":"run_parent"`,
		`"forwardedProps":{"tenant":"acme"}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}

func TestTextMessageContentEventJSONShape(t *testing.T) {
	ev := TextMessageContentEvent{
		BaseEvent: BaseEvent{Type: EventTextMessageContent, Timestamp: int64Ptr(42)},
		MessageID: "msg_1",
		Delta:     "hello",
	}

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"type":"TEXT_MESSAGE_CONTENT"`,
		`"timestamp":42`,
		`"messageId":"msg_1"`,
		`"delta":"hello"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}

func TestBaseEventPreservesZeroTimestamp(t *testing.T) {
	data, err := json.Marshal(BaseEvent{Type: EventRaw, Timestamp: int64Ptr(0)})
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if got := string(data); !strings.Contains(got, `"timestamp":0`) {
		t.Fatalf("json = %s, want timestamp 0", got)
	}
}

func TestToolCallEventsUseProtocolFieldNames(t *testing.T) {
	events := []Event{
		ToolCallStartEvent{
			BaseEvent:       BaseEvent{Type: EventToolCallStart},
			ToolCallID:      "call_1",
			ToolCallName:    "search",
			ParentMessageID: stringPtr("msg_1"),
		},
		ToolCallArgsEvent{
			BaseEvent:  BaseEvent{Type: EventToolCallArgs},
			ToolCallID: "call_1",
			Delta:      `{"query":"go"}`,
		},
		ToolCallEndEvent{
			BaseEvent:  BaseEvent{Type: EventToolCallEnd},
			ToolCallID: "call_1",
		},
		ToolCallResultEvent{
			BaseEvent:  BaseEvent{Type: EventToolCallResult},
			MessageID:  "msg_result",
			ToolCallID: "call_1",
			Content:    "done",
			Role:       stringPtr(string(RoleTool)),
		},
	}

	data, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"toolCallId":"call_1"`,
		`"toolCallName":"search"`,
		`"parentMessageId":"msg_1"`,
		`"messageId":"msg_result"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}

func TestRunStartedEventJSONShape(t *testing.T) {
	parent := "run-parent"
	ev := RunStartedEvent{
		BaseEvent:   BaseEvent{Type: EventRunStarted},
		ThreadID:    "thread-1",
		RunID:       "run-1",
		ParentRunID: &parent,
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"type":"RUN_STARTED"`,
		`"threadId":"thread-1"`,
		`"runId":"run-1"`,
		`"parentRunId":"run-parent"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}

func TestRunFinishedEventJSONShape(t *testing.T) {
	ev := RunFinishedEvent{
		BaseEvent: BaseEvent{Type: EventRunFinished},
		ThreadID:  "thread-1",
		RunID:     "run-1",
		Outcome: &RunFinishedOutcome{
			Type: RunFinishedOutcomeInterrupt,
			Interrupts: []Interrupt{{
				ID:         "call_1",
				ToolCallID: "call_1",
				Reason:     "tool_call",
			}},
		},
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"type":"RUN_FINISHED"`,
		`"threadId":"thread-1"`,
		`"runId":"run-1"`,
		`"outcome":`,
		`"type":"interrupt"`,
		`"toolCallId":"call_1"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}

func TestRunErrorEventJSONShape(t *testing.T) {
	ev := RunErrorEvent{
		BaseEvent: BaseEvent{Type: EventRunError},
		Message:   "something went wrong",
		RunID:     "run-1",
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"type":"RUN_ERROR"`,
		`"message":"something went wrong"`,
		`"runId":"run-1"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}

func int64Ptr(v int64) *int64 { return &v }
