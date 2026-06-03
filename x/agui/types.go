// Package agui adapts fino runs to the AG-UI protocol.
package agui

// EventType is an AG-UI event discriminator.
type EventType string

const (
	EventRunStarted         EventType = "RUN_STARTED"
	EventRunFinished        EventType = "RUN_FINISHED"
	EventRunError           EventType = "RUN_ERROR"
	EventStepStarted        EventType = "STEP_STARTED"
	EventStepFinished       EventType = "STEP_FINISHED"
	EventTextMessageStart   EventType = "TEXT_MESSAGE_START"
	EventTextMessageContent EventType = "TEXT_MESSAGE_CONTENT"
	EventTextMessageEnd     EventType = "TEXT_MESSAGE_END"
	EventToolCallStart      EventType = "TOOL_CALL_START"
	EventToolCallArgs       EventType = "TOOL_CALL_ARGS"
	EventToolCallEnd        EventType = "TOOL_CALL_END"
	EventToolCallResult     EventType = "TOOL_CALL_RESULT"
	EventMessagesSnapshot   EventType = "MESSAGES_SNAPSHOT"
	EventRaw                EventType = "RAW"
	EventCustom             EventType = "CUSTOM"
)

// Role is an AG-UI message role.
type Role string

const (
	RoleDeveloper Role = "developer"
	RoleSystem    Role = "system"
	RoleAssistant Role = "assistant"
	RoleUser      Role = "user"
	RoleTool      Role = "tool"
	RoleActivity  Role = "activity"
	RoleReasoning Role = "reasoning"
)

// FunctionCall is the function payload in an assistant tool call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCallTypeFunction is the AG-UI tool call type for function calls.
const ToolCallTypeFunction = "function"

// ToolCall is one assistant tool call in an AG-UI message.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// Message is the protocol message shape accepted by RunAgentInput and emitted
// by message snapshot events.
type Message struct {
	ID               string     `json:"id"`
	Role             Role       `json:"role"`
	Content          any        `json:"content,omitempty"`
	Name             string     `json:"name,omitempty"`
	EncryptedContent string     `json:"encryptedContent,omitempty"`
	EncryptedValue   string     `json:"encryptedValue,omitempty"`
	ToolCalls        []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID       string     `json:"toolCallId,omitempty"`
	Error            string     `json:"error,omitempty"`
	ActivityType     string     `json:"activityType,omitempty"`
}

// Tool describes a frontend-visible tool in an AG-UI request.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// Context is one contextual value supplied with an AG-UI run request.
type Context struct {
	Description string `json:"description"`
	Value       string `json:"value"`
}

// RunAgentInput is the top-level AG-UI run request.
type RunAgentInput struct {
	ThreadID       string        `json:"threadId"`
	RunID          string        `json:"runId"`
	ParentRunID    *string       `json:"parentRunId,omitempty"`
	State          any           `json:"state"`
	Messages       []Message     `json:"messages"`
	Tools          []Tool        `json:"tools"`
	Context        []Context     `json:"context"`
	ForwardedProps any           `json:"forwardedProps"`
	Resume         []ResumeEntry `json:"resume,omitempty"`
}

// ResumeStatus is the status of an AG-UI interrupt resolution.
type ResumeStatus string

const (
	ResumeStatusResolved  ResumeStatus = "resolved"
	ResumeStatusCancelled ResumeStatus = "cancelled"
)

// ResumeEntry is one interrupt response in a resumed run request.
type ResumeEntry struct {
	InterruptID string       `json:"interruptId"`
	Status      ResumeStatus `json:"status"`
	Payload     any          `json:"payload,omitempty"`
}

// Interrupt is a pause point requiring user input before a run can continue.
type Interrupt struct {
	ID             string         `json:"id"`
	Reason         string         `json:"reason"`
	Message        string         `json:"message,omitempty"`
	ToolCallID     string         `json:"toolCallId,omitempty"`
	ResponseSchema map[string]any `json:"responseSchema,omitempty"`
	ExpiresAt      string         `json:"expiresAt,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// RunFinishedOutcomeType is the discriminator for a run outcome.
type RunFinishedOutcomeType string

const (
	RunFinishedOutcomeSuccess   RunFinishedOutcomeType = "success"
	RunFinishedOutcomeInterrupt RunFinishedOutcomeType = "interrupt"
)

// RunFinishedOutcome describes whether a run completed or paused.
type RunFinishedOutcome struct {
	Type       RunFinishedOutcomeType `json:"type"`
	Interrupts []Interrupt            `json:"interrupts,omitempty"`
}

// Event is a sealed interface for AG-UI events emitted by this package.
type Event interface{ event() }

// BaseEvent carries fields shared by AG-UI events.
type BaseEvent struct {
	Type      EventType `json:"type"`
	Timestamp *int64    `json:"timestamp,omitempty"`
	RawEvent  any       `json:"rawEvent,omitempty"`
}

// RunStartedEvent signals that a run has started.
type RunStartedEvent struct {
	BaseEvent
	ThreadID    string  `json:"threadId"`
	RunID       string  `json:"runId"`
	ParentRunID *string `json:"parentRunId,omitempty"`
}

// RunFinishedEvent signals that a run has finished.
type RunFinishedEvent struct {
	BaseEvent
	ThreadID string              `json:"threadId"`
	RunID    string              `json:"runId"`
	Result   any                 `json:"result,omitempty"`
	Outcome  *RunFinishedOutcome `json:"outcome,omitempty"`
}

// RunErrorEvent signals that a run has failed.
type RunErrorEvent struct {
	BaseEvent
	Message string  `json:"message"`
	Code    *string `json:"code,omitempty"`
	RunID   string  `json:"runId,omitempty"`
}

// StepStartedEvent signals that an agent step has started.
type StepStartedEvent struct {
	BaseEvent
	StepName string `json:"stepName"`
}

// StepFinishedEvent signals that an agent step has finished.
type StepFinishedEvent struct {
	BaseEvent
	StepName string `json:"stepName"`
}

// TextMessageStartEvent signals the start of an assistant text message.
type TextMessageStartEvent struct {
	BaseEvent
	MessageID string  `json:"messageId"`
	Role      *string `json:"role,omitempty"`
}

// TextMessageContentEvent carries an incremental text message fragment.
type TextMessageContentEvent struct {
	BaseEvent
	MessageID string `json:"messageId"`
	Delta     string `json:"delta"`
}

// TextMessageEndEvent signals the end of an assistant text message.
type TextMessageEndEvent struct {
	BaseEvent
	MessageID string `json:"messageId"`
}

// ToolCallStartEvent signals the start of a tool call.
type ToolCallStartEvent struct {
	BaseEvent
	ToolCallID      string  `json:"toolCallId"`
	ToolCallName    string  `json:"toolCallName"`
	ParentMessageID *string `json:"parentMessageId,omitempty"`
}

// ToolCallArgsEvent carries an incremental tool argument fragment.
type ToolCallArgsEvent struct {
	BaseEvent
	ToolCallID string `json:"toolCallId"`
	Delta      string `json:"delta"`
}

// ToolCallEndEvent signals the end of a tool call.
type ToolCallEndEvent struct {
	BaseEvent
	ToolCallID string `json:"toolCallId"`
}

// ToolCallResultEvent carries a tool call result message.
type ToolCallResultEvent struct {
	BaseEvent
	MessageID  string  `json:"messageId"`
	ToolCallID string  `json:"toolCallId"`
	Content    string  `json:"content"`
	Role       *string `json:"role,omitempty"`
}

// MessagesSnapshotEvent replaces the current protocol message snapshot.
type MessagesSnapshotEvent struct {
	BaseEvent
	Messages []Message `json:"messages"`
}

// RawEvent carries an implementation-specific event.
type RawEvent struct {
	BaseEvent
	Event  any     `json:"event"`
	Source *string `json:"source,omitempty"`
}

// CustomEvent carries an application-defined event.
type CustomEvent struct {
	BaseEvent
	Name  string `json:"name"`
	Value any    `json:"value,omitempty"`
}

func (RunStartedEvent) event()         {}
func (RunFinishedEvent) event()        {}
func (RunErrorEvent) event()           {}
func (StepStartedEvent) event()        {}
func (StepFinishedEvent) event()       {}
func (TextMessageStartEvent) event()   {}
func (TextMessageContentEvent) event() {}
func (TextMessageEndEvent) event()     {}
func (ToolCallStartEvent) event()      {}
func (ToolCallArgsEvent) event()       {}
func (ToolCallEndEvent) event()        {}
func (ToolCallResultEvent) event()     {}
func (MessagesSnapshotEvent) event()   {}
func (RawEvent) event()                {}
func (CustomEvent) event()             {}
