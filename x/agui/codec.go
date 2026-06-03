package agui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
)

// ErrInvalidRunIdentity indicates that a mapper was created without the
// protocol identifiers required to correlate its events.
var ErrInvalidRunIdentity = errors.New("invalid AG-UI run identity")

// ErrInvalidStreamError indicates that a StreamError event did not carry the
// error required to build an AG-UI RUN_ERROR event.
var ErrInvalidStreamError = errors.New("invalid stream error event")

// ErrInvalidTextStream indicates that streamed text cannot be reconciled with
// the complete TurnMessage snapshot.
var ErrInvalidTextStream = errors.New("invalid text stream")

// ErrInvalidSuspension indicates that a Suspended event did not carry any
// pending calls.
var ErrInvalidSuspension = errors.New("invalid suspension event")

// ErrInvalidToolCall indicates that a tool call cannot be represented as a
// valid AG-UI tool event lifecycle.
var ErrInvalidToolCall = errors.New("invalid tool call")

// ErrInvalidTurnMessage indicates that a TurnMessage cannot be represented as
// an AG-UI assistant message lifecycle.
var ErrInvalidTurnMessage = errors.New("invalid turn message")

// Mapper converts fino stream events into AG-UI events for one run.
type Mapper struct {
	threadID      string
	runID         string
	messageSeq    int
	textMessageID string
	text          strings.Builder
}

// NewMapper creates an event mapper for one AG-UI thread and run.
func NewMapper(threadID, runID string) (*Mapper, error) {
	if threadID == "" {
		return nil, fmt.Errorf("%w: thread ID is empty", ErrInvalidRunIdentity)
	}
	if runID == "" {
		return nil, fmt.Errorf("%w: run ID is empty", ErrInvalidRunIdentity)
	}
	return &Mapper{threadID: threadID, runID: runID}, nil
}

// RunStarted returns the lifecycle event that begins this mapper's run.
func (m *Mapper) RunStarted(parentRunID *string) Event {
	return RunStartedEvent{
		BaseEvent:   BaseEvent{Type: EventRunStarted},
		ThreadID:    m.threadID,
		RunID:       m.runID,
		ParentRunID: parentRunID,
	}
}

// Map converts one fino stream event into zero or more AG-UI events.
func (m *Mapper) Map(ev model.Event) ([]Event, error) {
	switch e := ev.(type) {
	case model.TextDelta:
		return m.mapTextDelta(e), nil
	case model.TurnMessage:
		return m.mapTurnMessage(e.Message)
	case model.ToolResult:
		return m.mapToolResult(e)
	case model.FinalMessage:
		return append(m.closeText(), RunFinishedEvent{
			BaseEvent: BaseEvent{Type: EventRunFinished},
			ThreadID:  m.threadID,
			RunID:     m.runID,
		}), nil
	case model.Suspended:
		return m.mapSuspended(e)
	case model.StreamError:
		if e.Err == nil || e.Err.Error() == "" {
			return nil, ErrInvalidStreamError
		}
		return append(m.closeText(), RunErrorEvent{
			BaseEvent: BaseEvent{Type: EventRunError},
			Message:   e.Err.Error(),
			RunID:     m.runID,
		}), nil
	default:
		return nil, nil
	}
}

func (m *Mapper) mapTextDelta(e model.TextDelta) []Event {
	if e.Text == "" {
		return nil
	}
	m.text.WriteString(e.Text)
	if m.textMessageID != "" {
		return []Event{TextMessageContentEvent{
			BaseEvent: BaseEvent{Type: EventTextMessageContent},
			MessageID: m.textMessageID,
			Delta:     e.Text,
		}}
	}
	m.textMessageID = m.nextMessageID()
	return []Event{
		TextMessageStartEvent{
			BaseEvent: BaseEvent{Type: EventTextMessageStart},
			MessageID: m.textMessageID,
			Role:      stringPtr(string(RoleAssistant)),
		},
		TextMessageContentEvent{
			BaseEvent: BaseEvent{Type: EventTextMessageContent},
			MessageID: m.textMessageID,
			Delta:     e.Text,
		},
	}
}

func (m *Mapper) mapTurnMessage(msg message.Message) ([]Event, error) {
	if msg.Role != message.RoleAssistant {
		return nil, fmt.Errorf("%w: role is %q", ErrInvalidTurnMessage, msg.Role)
	}
	if err := validateAssistantBlocks(msg.Content); err != nil {
		return nil, err
	}
	calls := msg.ToolUses()
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if err := validateToolCall(call); err != nil {
			return nil, err
		}
		if _, ok := seen[call.ID]; ok {
			return nil, fmt.Errorf("%w: duplicate ID %q", ErrInvalidToolCall, call.ID)
		}
		seen[call.ID] = struct{}{}
	}
	events := make([]Event, 0, 1+len(calls)*3)
	parentMessageID := m.textMessageID
	if m.textMessageID != "" {
		fullText := msg.Text()
		streamedText := m.text.String()
		if !strings.HasPrefix(fullText, streamedText) {
			return nil, fmt.Errorf("%w: streamed text is not a prefix of TurnMessage text", ErrInvalidTextStream)
		}
		if suffix := strings.TrimPrefix(fullText, streamedText); suffix != "" {
			events = append(events, TextMessageContentEvent{
				BaseEvent: BaseEvent{Type: EventTextMessageContent},
				MessageID: m.textMessageID,
				Delta:     suffix,
			})
		}
		events = append(events, TextMessageEndEvent{
			BaseEvent: BaseEvent{Type: EventTextMessageEnd},
			MessageID: m.textMessageID,
		})
		m.textMessageID = ""
		m.text.Reset()
	}
	if parentMessageID == "" && msg.Text() != "" {
		parentMessageID = m.nextMessageID()
		events = append(events, textSnapshotEvents(parentMessageID, msg.Text())...)
	}
	for _, call := range calls {
		events = append(events, toolCallEvents(call, parentMessageID)...)
	}
	return events, nil
}

func validateAssistantBlocks(blocks []message.Block) error {
	for _, block := range blocks {
		switch block.Type {
		case message.TypeText, message.TypeToolUse, message.TypeThinking:
		default:
			return fmt.Errorf("%w: unsupported assistant block type %q", ErrInvalidTurnMessage, block.Type)
		}
	}
	return nil
}

func textSnapshotEvents(messageID, text string) []Event {
	return []Event{
		TextMessageStartEvent{
			BaseEvent: BaseEvent{Type: EventTextMessageStart},
			MessageID: messageID,
			Role:      stringPtr(string(RoleAssistant)),
		},
		TextMessageContentEvent{
			BaseEvent: BaseEvent{Type: EventTextMessageContent},
			MessageID: messageID,
			Delta:     text,
		},
		TextMessageEndEvent{
			BaseEvent: BaseEvent{Type: EventTextMessageEnd},
			MessageID: messageID,
		},
	}
}

func validateToolCall(call message.ToolUse) error {
	if call.ID == "" {
		return fmt.Errorf("%w: ID is empty", ErrInvalidToolCall)
	}
	if call.Name == "" {
		return fmt.Errorf("%w: name is empty", ErrInvalidToolCall)
	}
	if len(call.Input) > 0 && !json.Valid(call.Input) {
		return fmt.Errorf("%w: input is not valid JSON", ErrInvalidToolCall)
	}
	return nil
}

func toolCallEvents(call message.ToolUse, parentMessageID string) []Event {
	input := string(call.Input)
	if input == "" {
		input = "{}"
	}
	return []Event{
		ToolCallStartEvent{
			BaseEvent:       BaseEvent{Type: EventToolCallStart},
			ToolCallID:      call.ID,
			ToolCallName:    call.Name,
			ParentMessageID: stringPtr(parentMessageID),
		},
		ToolCallArgsEvent{
			BaseEvent:  BaseEvent{Type: EventToolCallArgs},
			ToolCallID: call.ID,
			Delta:      input,
		},
		ToolCallEndEvent{
			BaseEvent:  BaseEvent{Type: EventToolCallEnd},
			ToolCallID: call.ID,
		},
	}
}

func (m *Mapper) mapToolResult(e model.ToolResult) ([]Event, error) {
	if e.CallID == "" {
		return nil, fmt.Errorf("%w: result call ID is empty", ErrInvalidToolCall)
	}
	return []Event{ToolCallResultEvent{
		BaseEvent:  BaseEvent{Type: EventToolCallResult},
		MessageID:  m.nextMessageID(),
		ToolCallID: e.CallID,
		Content:    toolResultContent(e.Result.Content),
		Role:       stringPtr(string(RoleTool)),
	}}, nil
}

func toolResultContent(blocks []message.Block) string {
	if len(blocks) == 1 && blocks[0].Type == message.TypeText && blocks[0].Text != "" {
		return blocks[0].Text
	}
	if len(blocks) == 0 {
		return "[]"
	}
	data, err := json.Marshal(blocks)
	if err != nil {
		return fmt.Sprintf("unserializable tool result: %v", err)
	}
	return string(data)
}

func (m *Mapper) mapSuspended(e model.Suspended) ([]Event, error) {
	if len(e.PendingCalls) == 0 {
		return nil, ErrInvalidSuspension
	}
	interrupts := make([]Interrupt, len(e.PendingCalls))
	seen := make(map[string]struct{}, len(e.PendingCalls))
	for i, pending := range e.PendingCalls {
		if pending.Call.ID == "" {
			return nil, fmt.Errorf("%w: pending call ID is empty", ErrInvalidSuspension)
		}
		if _, ok := seen[pending.Call.ID]; ok {
			return nil, fmt.Errorf("%w: duplicate pending call ID %q", ErrInvalidSuspension, pending.Call.ID)
		}
		seen[pending.Call.ID] = struct{}{}
		interrupts[i] = Interrupt{
			ID:         pending.Call.ID,
			Reason:     "tool_call",
			Message:    pending.Reason,
			ToolCallID: pending.Call.ID,
		}
	}
	return append(m.closeText(), RunFinishedEvent{
		BaseEvent: BaseEvent{Type: EventRunFinished},
		ThreadID:  m.threadID,
		RunID:     m.runID,
		Outcome: &RunFinishedOutcome{
			Type:       RunFinishedOutcomeInterrupt,
			Interrupts: interrupts,
		},
	}), nil
}

func (m *Mapper) closeText() []Event {
	if m.textMessageID == "" {
		return nil
	}
	event := TextMessageEndEvent{
		BaseEvent: BaseEvent{Type: EventTextMessageEnd},
		MessageID: m.textMessageID,
	}
	m.textMessageID = ""
	m.text.Reset()
	return []Event{event}
}

func (m *Mapper) nextMessageID() string {
	m.messageSeq++
	return fmt.Sprintf("%s-message-%d", m.runID, m.messageSeq)
}

func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
