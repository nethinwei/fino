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
	// iterator must yield a FinalMessage event to signal the end of a turn.
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

// FinalMessage carries the complete assembled message at the end of a model
// turn.
type FinalMessage struct{ Message message.Message }

// StreamError is a terminal error event in the stream. It is always yielded
// alongside a non-nil iterator error to signal iteration stop.
type StreamError struct{ Err error }

func (ContentBlockStart) event() {}
func (ContentBlockDelta) event() {}
func (ContentBlockStop) event()  {}
func (TextDelta) event()         {}
func (ToolCall) event()          {}
func (ToolResult) event()        {}
func (Handoff) event()           {}
func (FinalMessage) event()      {}
func (StreamError) event()       {}

// Option configures a model call.
type Option func(*config)

type config struct {
	temperature *float32
	maxTokens   *int
}

// WithTemperature sets the generation temperature.
func WithTemperature(v float32) Option {
	temp := v
	return func(c *config) { c.temperature = &temp }
}

// WithMaxTokens sets the maximum number of tokens to generate.
func WithMaxTokens(v int) Option {
	n := v
	return func(c *config) { c.maxTokens = &n }
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
