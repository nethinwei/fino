package agui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/runner"
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
	// Reasoning precedes the assistant's answer. fino only carries reasoning as
	// thinking blocks the provider already surfaced, so an absent block yields no
	// reasoning events and no hidden chain-of-thought is invented.
	events = append(events, m.reasoningEvents(msg)...)
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

// reasoningEvents maps the thinking blocks of one assistant turn into a single
// REASONING sequence: a START/END pair wrapping one MESSAGE_START/CONTENT/END
// trio per thinking block. fino TurnMessages are snapshots, not streamed, so the
// full thinking text arrives in one CONTENT event. Empty thinking blocks are
// skipped; a turn with no thinking text produces no reasoning events at all.
func (m *Mapper) reasoningEvents(msg message.Message) []Event {
	var texts []string
	for _, block := range msg.Content {
		if block.Type == message.TypeThinking && block.Text != "" {
			texts = append(texts, block.Text)
		}
	}
	if len(texts) == 0 {
		return nil
	}
	// Use one shared ID for the outer REASONING_START/END envelope, but give
	// each individual thinking block its own message ID so the client state
	// machine never sees the same ID opened twice without closing first.
	envelopeID := m.nextMessageID()
	events := []Event{ReasoningStartEvent{
		BaseEvent: BaseEvent{Type: EventReasoningStart},
		MessageID: envelopeID,
	}}
	for _, text := range texts {
		msgID := m.nextMessageID()
		events = append(events,
			ReasoningMessageStartEvent{
				BaseEvent: BaseEvent{Type: EventReasoningMessageStart},
				MessageID: msgID,
				Role:      string(RoleReasoning),
			},
			ReasoningMessageContentEvent{
				BaseEvent: BaseEvent{Type: EventReasoningMessageContent},
				MessageID: msgID,
				Delta:     text,
			},
			ReasoningMessageEndEvent{
				BaseEvent: BaseEvent{Type: EventReasoningMessageEnd},
				MessageID: msgID,
			},
		)
	}
	return append(events, ReasoningEndEvent{
		BaseEvent: BaseEvent{Type: EventReasoningEnd},
		MessageID: envelopeID,
	})
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
	if len(blocks) == 1 && blocks[0].Type == message.TypeText {
		// Empty text block is semantically equivalent to no content; normalise
		// to "[]" so clients see a consistent representation in both cases.
		if blocks[0].Text == "" {
			return "[]"
		}
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
		interrupts[i] = buildInterrupt(pending.Call.ID, pending.Reason)
	}
	// The runtime resumes from the persisted snapshot, but a client still needs
	// the full message history to render the interrupt; emit it as a snapshot
	// before the terminal event, per the AG-UI interrupt contract.
	events := m.closeText()
	if len(e.Messages) > 0 {
		events = append(events, MessagesSnapshotEvent{
			BaseEvent: BaseEvent{Type: EventMessagesSnapshot},
			Messages:  toProtocolMessages(e.Messages),
		})
	}
	return append(events, RunFinishedEvent{
		BaseEvent: BaseEvent{Type: EventRunFinished},
		ThreadID:  m.threadID,
		RunID:     m.runID,
		Outcome: &RunFinishedOutcome{
			Type:       RunFinishedOutcomeInterrupt,
			Interrupts: interrupts,
		},
	}), nil
}

// buildInterrupt builds the AG-UI interrupt for one suspended tool call. It is
// shared by the stream suspension path and the resume re-suspension path so both
// report interrupts with identical shape and response schema.
func buildInterrupt(callID, reason string) Interrupt {
	return Interrupt{
		ID:             callID,
		Reason:         "tool_call",
		Message:        reason,
		ToolCallID:     callID,
		ResponseSchema: approvalResponseSchema(),
	}
}

// interruptFinishedEvent builds a terminal RUN_FINISHED carrying an interrupt
// outcome for the given pending calls, used when a resumed run suspends again.
func interruptFinishedEvent(m *Mapper, pending []runner.PendingToolCall) Event {
	interrupts := make([]Interrupt, len(pending))
	for i, pc := range pending {
		interrupts[i] = buildInterrupt(pc.Call.ID, pc.Reason)
	}
	return RunFinishedEvent{
		BaseEvent: BaseEvent{Type: EventRunFinished},
		ThreadID:  m.threadID,
		RunID:     m.runID,
		Outcome: &RunFinishedOutcome{
			Type:       RunFinishedOutcomeInterrupt,
			Interrupts: interrupts,
		},
	}
}

// approvalResponseSchema describes the resume payload an interrupt expects: a
// JSON object with a boolean "approved" field. It tells the client that the
// rejection intent is carried in the payload, not implied by the resume status.
func approvalResponseSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"approved": map[string]any{"type": "boolean"},
		},
		"required": []any{"approved"},
	}
}

// toProtocolMessages converts a fino message history into AG-UI protocol
// messages for a MESSAGES_SNAPSHOT. User text, assistant text and tool calls,
// and tool results are mapped; other roles are skipped.
func toProtocolMessages(msgs []message.Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case message.RoleUser:
			out = append(out, Message{Role: RoleUser, Content: m.Text()})
		case message.RoleAssistant:
			out = append(out, assistantProtocolMessage(m))
		case message.RoleTool:
			out = append(out, toolResultProtocolMessages(m)...)
		}
	}
	return out
}

func assistantProtocolMessage(m message.Message) Message {
	pm := Message{Role: RoleAssistant}
	if text := m.Text(); text != "" {
		pm.Content = text
	}
	for _, tu := range m.ToolUses() {
		args := string(tu.Input)
		if args == "" {
			args = "{}" // match the streaming TOOL_CALL_ARGS default; "" is not valid JSON
		}
		pm.ToolCalls = append(pm.ToolCalls, ToolCall{
			ID:       tu.ID,
			Type:     ToolCallTypeFunction,
			Function: FunctionCall{Name: tu.Name, Arguments: args},
		})
	}
	return pm
}

func toolResultProtocolMessages(m message.Message) []Message {
	var out []Message
	for _, block := range m.Content {
		if block.Type != message.TypeToolResult {
			continue
		}
		out = append(out, Message{
			Role:       RoleTool,
			ToolCallID: block.ToolUseID,
			Name:       block.Name,
			Content:    toolResultContent(block.Content),
		})
	}
	return out
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

// mapResumeResult maps the messages a resumed run appended after the suspend
// snapshot — tool results for the approved (or rejected) calls and any
// post-resume assistant turns — into AG-UI events. ResumeApproved does not
// stream, so these are mapped from the final history rather than live deltas.
func mapResumeResult(m *Mapper, suspended runner.SuspendedRun, result *runner.Result) []Event {
	if result == nil {
		return nil
	}
	// ResumeApproved initializes its history from suspended.Messages and only
	// appends, so result.Messages has suspended.Messages as a prefix; slice it
	// off to get just the post-resume messages. The length guard is defensive.
	appended := result.Messages
	if len(suspended.Messages) <= len(appended) {
		appended = appended[len(suspended.Messages):]
	}
	var events []Event
	for _, msg := range appended {
		switch msg.Role {
		case message.RoleTool:
			events = append(events, m.resumeToolResultEvents(msg)...)
		case message.RoleAssistant:
			events = append(events, m.resumeAssistantEvents(msg)...)
		}
	}
	return events
}

func (m *Mapper) resumeToolResultEvents(msg message.Message) []Event {
	var events []Event
	for _, block := range msg.Content {
		if block.Type != message.TypeToolResult {
			continue
		}
		events = append(events, ToolCallResultEvent{
			BaseEvent:  BaseEvent{Type: EventToolCallResult},
			MessageID:  m.nextMessageID(),
			ToolCallID: block.ToolUseID,
			Content:    toolResultContent(block.Content),
			Role:       stringPtr(string(RoleTool)),
		})
	}
	return events
}

func (m *Mapper) resumeAssistantEvents(msg message.Message) []Event {
	var events []Event
	parentMessageID := ""
	if text := msg.Text(); text != "" {
		parentMessageID = m.nextMessageID()
		events = append(events, textSnapshotEvents(parentMessageID, text)...)
	}
	for _, call := range msg.ToolUses() {
		events = append(events, toolCallEvents(call, parentMessageID)...)
	}
	return events
}
