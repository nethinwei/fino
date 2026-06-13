// Package model defines the Model interface and stream event types for the
// fino Agent SDK. Provider adapters implement this interface to integrate
// specific LLM APIs.
package model

import (
	"context"
	"iter"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/tool"
)

// Model is the provider abstraction. Implementations must support both
// synchronous generation and streaming.
type Model interface {
	// Generate produces a single model response synchronously.
	Generate(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...Option) (*message.Message, error)
	// Stream produces a sequence of semantic events from the model. The
	// iterator must yield exactly one TurnMessage as the final event to signal
	// the end of the turn. A provider yields only model-layer events; it must
	// not yield the Runner-only events FinalMessage, Suspended, ToolCall,
	// ToolResult, or Handoff (all are emitted by Runner.Stream, never by a
	// provider). No further events may follow the TurnMessage. The Runner treats
	// a missing TurnMessage, a second TurnMessage, a post-TurnMessage event, or
	// any FinalMessage/Suspended/ToolCall/ToolResult/Handoff as a contract
	// violation (runner.ErrStreamContract).
	Stream(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...Option) iter.Seq2[Event, error]
}

// Event is a sealed interface for stream events. Only the concrete types
// defined in this package satisfy Event; external types must not implement it.
type Event interface{ event() }

// ContentBlockStart signals the beginning of a content block in the stream.
type ContentBlockStart struct {
	Index int
	Block message.Block
}

// ContentBlockDelta carries an incremental update within a content block.
type ContentBlockDelta struct {
	Index int
	Block message.Block
}

// ContentBlockStop signals the end of a content block in the stream.
type ContentBlockStop struct {
	Index int
	Block message.Block
}

// TextDelta carries an incremental text fragment.
type TextDelta struct{ Text string }

// ToolCall is a Runner-generated event emitted before a tool is executed.
type ToolCall struct{ Call message.ToolUse }

// ToolResult is a Runner-generated event emitted after a tool execution
// completes.
type ToolResult struct {
	CallID string
	Name   string
	Result tool.Result
}

// Handoff is a Runner-generated event emitted when the current agent switches
// to a target agent.
type Handoff struct{ Target string }

// TurnMessage carries the complete assembled assistant message produced by one
// model turn. It is the model layer's terminal event for a single Stream call:
// a model.Model implementation MUST yield exactly one TurnMessage at the end of
// its stream and MUST NOT yield the Runner-only terminal events FinalMessage or
// Suspended. The Runner forwards each turn's
// TurnMessage to consumers so the full per-turn assistant snapshot (including
// turns that contain tool calls) is observable and replayable.
type TurnMessage struct{ Message message.Message }

// FinalMessage marks the terminal result of an entire Runner.Stream run. It is
// emitted only by the Runner, exactly once, after the final model turn produced
// a message with no tool calls. Provider model.Model implementations must not
// yield it (they yield TurnMessage instead); the Runner treats a provider-sent
// FinalMessage — or Suspended, which is likewise Runner-only — as a contract
// violation.
type FinalMessage struct{ Message message.Message }

// StreamError is a terminal error event in the stream. It is always yielded
// alongside a non-nil iterator error to signal iteration stop.
type StreamError struct{ Err error }

// SuspendedCall is the neutral form of a Policy-suspended tool call. It mirrors
// runner.PendingToolCall without importing runner (model must not depend on
// runner), so the Stream path can surface suspended calls as an event.
type SuspendedCall struct {
	Tool   tool.Info
	Call   message.ToolUse
	Reason string
}

// Suspended is a Runner-generated terminal event on the Stream path: a Policy
// suspended the batch, so no tool ran and no ToolCall was emitted. It carries
// the neutral snapshot a caller needs to resume — rebuild a runner.SuspendedRun
// with runner.SuspendedRunFrom, then call Runner.ResumeApproved. Like the Run
// path's suspended Result it is not an error: iteration ends without a
// StreamError. A provider must not yield it (Runner-only); the Runner treats a
// provider-sent Suspended as ErrStreamContract. It is the Stream counterpart of
// [T-SUSPEND] (loop-semantics §3/§5).
type Suspended struct {
	Messages      []message.Message
	LastAgentName string
	LastMode      string
	RunID         string
	PendingCalls  []SuspendedCall
}

func (ContentBlockStart) event() {}
func (ContentBlockDelta) event() {}
func (ContentBlockStop) event()  {}
func (TextDelta) event()         {}
func (ToolCall) event()          {}
func (ToolResult) event()        {}
func (Handoff) event()           {}
func (TurnMessage) event()       {}
func (FinalMessage) event()      {}
func (StreamError) event()       {}
func (Suspended) event()         {}

// Option configures a model call.
type Option func(*config)

type config struct {
	temperature *float32
	topP        *float32
	maxTokens   *int
	extra       map[any]any
}

// WithTemperature sets the generation temperature.
func WithTemperature(v float32) Option {
	temp := v
	return func(c *config) { c.temperature = &temp }
}

// WithTopP sets the nucleus sampling probability mass. It is universal across
// providers, so it lives in the core option set.
func WithTopP(v float32) Option {
	p := v
	return func(c *config) { c.topP = &p }
}

// WithMaxTokens sets the maximum number of tokens to generate.
func WithMaxTokens(v int) Option {
	n := v
	return func(c *config) { c.maxTokens = &n }
}

// WithExtra stores a provider-specific value keyed by an opaque key. It is the
// single core primitive for provider extensions: provider packages wrap it in
// typed options (e.g. openai.WithReasoningEffort) and read it back via
// ExtraValue. Use an unexported key type per provider to avoid collisions, as
// the context package does. The core never interprets these values.
func WithExtra(key, value any) Option {
	return func(c *config) {
		if c.extra == nil {
			c.extra = map[any]any{}
		}
		c.extra[key] = value
	}
}

func newConfig(opts []Option) config {
	cfg := config{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// Config is the resolved set of model options. Provider adapters obtain it via
// ApplyOptions to read user-supplied generation settings. A nil field means
// the option was not set and the provider should omit it from the request.
type Config struct {
	Temperature *float32
	TopP        *float32
	MaxTokens   *int
	// Extra holds provider-specific values set via WithExtra, keyed by the
	// provider's opaque key. Read them with ExtraValue.
	Extra map[any]any
}

// ApplyOptions resolves the given options into a Config for provider adapters.
func ApplyOptions(opts ...Option) Config {
	c := newConfig(opts)
	return Config{Temperature: c.temperature, TopP: c.topP, MaxTokens: c.maxTokens, Extra: c.extra}
}

// ExtraValue returns the provider extra stored under key, typed as T. The
// second result is false when the key is absent or holds a different type.
func ExtraValue[T any](c Config, key any) (T, bool) {
	v, ok := c.Extra[key].(T)
	return v, ok
}
