// Package trace adapts fino lifecycle hooks to a minimal tracer.
//
// It is a reference composition for the sufficiency thesis in docs/design.md:
// observability is a cross-cutting concern carried by the deterministic firing
// of hooks.Hooks, not by a core capability. The Tracer interface is a tiny,
// dependency-free seam; an OpenTelemetry adapter belongs in examples, not here.
package trace

import (
	"context"
	"sync"

	"github.com/nethinwei/fino/hooks"
)

// EndFunc closes a span. err is nil on success or the terminating error.
type EndFunc func(err error)

// once wraps an EndFunc so the underlying span is ended at most once, no matter
// how many hooks resolve to it. This makes the trace adapter robust against the
// core's hook contract on error paths:
//
//   - A model call that fails never invokes AfterModel, so OnError must end the
//     model span via modelKey. A successful model call ends it in AfterModel,
//     leaving modelKey resolvable for a *later* tool error in the same turn.
//   - In parallel tool execution the per-tool context (with toolKey) is scoped
//     to that tool's Run and AfterTool and is not propagated to OnError (see the
//     hooks ctx-scope note in docs/design.md). OnError then falls back to
//     modelKey, which would otherwise re-end an already-closed model span.
//
// Idempotent ends turn that second call into a no-op rather than a double-end,
// which on a real OTel tracer can warn or panic. The residual edge is benign: a
// tool that fails under parallel execution has its span opened (BeforeTool) but
// not explicitly ended, because the core delivers neither AfterTool nor a
// toolKey-bearing context to OnError for it. Such an unended span is dropped or
// marked incomplete by typical exporters; it is never double-ended.
func once(end EndFunc) EndFunc {
	var o sync.Once
	return func(err error) { o.Do(func() { end(err) }) }
}

// Tracer starts spans. Begin returns a child context and a function to end the
// span. Implementations must be safe for the single-goroutine hook call order.
type Tracer interface {
	Begin(ctx context.Context, op string) (context.Context, EndFunc)
}

type modelKey struct{}
type toolKey struct{}

// Hooks returns lifecycle hooks that open a span before each model call and
// tool call and close it afterward, recording the terminating error on OnError.
func Hooks(tr Tracer) *hooks.Hooks {
	return &hooks.Hooks{
		BeforeModel: func(ctx context.Context, mc hooks.ModelCall) context.Context {
			ctx, end := tr.Begin(ctx, "model:"+mc.AgentName+"/"+mc.ModeName)
			return context.WithValue(ctx, modelKey{}, once(end))
		},
		AfterModel: func(ctx context.Context, _ hooks.ModelResult) {
			if end, ok := ctx.Value(modelKey{}).(EndFunc); ok {
				end(nil)
			}
		},
		BeforeTool: func(ctx context.Context, tc hooks.ToolCall) context.Context {
			ctx, end := tr.Begin(ctx, "tool:"+tc.Tool.Name)
			return context.WithValue(ctx, toolKey{}, once(end))
		},
		AfterTool: func(ctx context.Context, _ hooks.ToolResult) {
			if end, ok := ctx.Value(toolKey{}).(EndFunc); ok {
				end(nil)
			}
		},
		OnError: func(ctx context.Context, err error) {
			// Prefer the innermost open span. A tool error in serial mode
			// carries toolKey; a model error carries only modelKey. The
			// modelKey fallback is required for model failures (AfterModel
			// never ran) and is safe for tool failures because once() makes
			// re-ending an already-closed model span a no-op.
			if end, ok := ctx.Value(toolKey{}).(EndFunc); ok {
				end(err)
				return
			}
			if end, ok := ctx.Value(modelKey{}).(EndFunc); ok {
				end(err)
			}
		},
	}
}
