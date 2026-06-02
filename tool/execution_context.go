package tool

import "context"

// ExecutionContext is the stable, run-scoped identity the Runner injects into
// the context passed to each tool's Run. Tools and x/ add-ons read it to
// correlate a tool call with its run and to derive an external deduplication or
// audit key. It is not a security boundary: the setter is exported, so any
// package can fabricate one, and tools must not make authorization decisions
// based on its contents.
type ExecutionContext struct {
	// RunID is the caller-supplied run identifier (runner.WithRunID). Empty
	// when the caller did not provide one.
	RunID string
	// ToolCallID is the message.ToolUse.ID of the model's tool_use block, the
	// same value surfaced as Approval.CallID and replay.ToolRecord.CallID. The
	// model provider is expected to supply unique, non-empty IDs per tool_use
	// within a run; the Runner does not validate this.
	ToolCallID string
	// IdempotencyKey is a stable function of (RunID, ToolCallID): RunID+":"+
	// ToolCallID when RunID is non-empty, otherwise empty. Tools read it; they
	// do not construct it.
	IdempotencyKey string
}

// executionContextKey is the unexported context key for ExecutionContext, so no
// other package can collide with it.
type executionContextKey struct{}

// ContextWithExecutionContext returns a copy of ctx carrying ec. The Runner
// calls it before each tool execution; tool authors read with
// ExecutionContextFrom instead.
func ContextWithExecutionContext(ctx context.Context, ec ExecutionContext) context.Context {
	return context.WithValue(ctx, executionContextKey{}, ec)
}

// ExecutionContextFrom returns the ExecutionContext carried by ctx, or
// (ExecutionContext{}, false) when none is present (e.g. replay, unit tests, or
// a tool called outside the Runner).
func ExecutionContextFrom(ctx context.Context) (ExecutionContext, bool) {
	ec, ok := ctx.Value(executionContextKey{}).(ExecutionContext)
	return ec, ok
}
