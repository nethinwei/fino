package runner

import (
	"context"
	"fmt"
	"iter"
	"sync"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/tool"
)

// emitErr reports a terminal error on the event stream: it fires the OnError
// hook and yields a model.StreamError alongside the non-nil iterator error.
func (r *Runner) emitErr(ctx context.Context, yield func(model.Event, error) bool, err error) {
	r.onError(ctx, err)
	yield(model.StreamError{Err: err}, err)
}

// emitSuspended yields the terminal model.Suspended event for a suspended batch
// on the Stream path. It builds the neutral snapshot from the run state and the
// suspended pending calls. Suspension is not an error, so it fires neither
// OnError nor a StreamError; the caller rebuilds a SuspendedRun with
// SuspendedRunFrom and resumes via ResumeApproved.
func (r *Runner) emitSuspended(st *runState, pending []PendingToolCall, yield func(model.Event, error) bool) {
	calls := make([]model.SuspendedCall, len(pending))
	for i, pc := range pending {
		calls[i] = model.SuspendedCall{Tool: pc.Tool, Call: pc.Call, Reason: pc.Reason}
	}
	yield(model.Suspended{
		Messages:      st.history,
		LastAgentName: st.agent.Name(),
		LastMode:      st.mode.Name,
		RunID:         st.cfg.runID,
		PendingCalls:  calls,
	}, nil)
}

// Stream executes the ReAct loop like Run but yields semantic events as they
// occur. Iteration stops after a final message with no tool calls, on the turn
// limit, or on the first error. Terminal errors are yielded as a
// model.StreamError alongside a non-nil iterator error. The arguments mirror Run.
func (r *Runner) Stream(ctx context.Context, a *agent.Agent, input Input, opts ...RunOption) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		r.streamLoop(ctx, yield, a, input, opts)
	}
}

// streamLoop drives the ReAct loop for Stream, emitting events through yield
// until a final message, the turn limit, or an error ends iteration.
func (r *Runner) streamLoop(ctx context.Context, yield func(model.Event, error) bool, a *agent.Agent, input Input, opts []RunOption) {
	st, err := r.prepareRun(a, input, opts)
	if err != nil {
		r.emitErr(ctx, yield, err)
		return
	}
	ctx, ok := r.resumePendingToolsStream(ctx, st, yield)
	if !ok {
		return
	}
	for turn := 0; turn < r.maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			r.emitErr(ctx, yield, err)
			return
		}
		newCtx, msg, ok := r.streamGenerate(ctx, st, yield)
		if !ok {
			return
		}
		ctx = newCtx
		calls := msg.ToolUses()
		if len(calls) == 0 {
			yield(model.FinalMessage{Message: *msg}, nil)
			return
		}
		ctx, ok = r.streamToolCalls(ctx, st, calls, yield)
		if !ok {
			return
		}
	}
	r.emitErr(ctx, yield, fmt.Errorf("%w: %d", ErrMaxTurns, r.maxTurns))
}

// resumePendingToolsStream is the Stream counterpart of resumePendingTools: when
// the resume-from-pending seam is enabled and the input history's tail carries
// pending tool calls, it executes them (emitting ToolCall/ToolResult events)
// before the first model turn. The bool result is false when iteration should
// stop. It is a no-op when the seam is off or there is nothing pending.
func (r *Runner) resumePendingToolsStream(ctx context.Context, st *runState, yield func(model.Event, error) bool) (context.Context, bool) {
	if !st.cfg.resumePending {
		return ctx, true
	}
	pending := pendingToolUses(st.history)
	if len(pending) == 0 {
		return ctx, true
	}
	if err := ctx.Err(); err != nil {
		r.emitErr(ctx, yield, err)
		return ctx, false
	}
	return r.streamToolCalls(ctx, st, pending, yield)
}

// streamGenerate builds the model input, consumes the model's event stream
// (forwarding deltas and the turn's TurnMessage), fires the model hooks, and
// appends the response to history. The bool result is false when iteration
// should stop: a stream error, a stopped consumer, a stream-contract violation
// (provider FinalMessage or a missing TurnMessage).
func (r *Runner) streamGenerate(ctx context.Context, st *runState, yield func(model.Event, error) bool) (context.Context, *message.Message, bool) {
	modelMessages := append([]message.Message{message.SystemText(st.mode.Instructions)}, st.history...)
	modelOpts := append([]model.Option(nil), st.mode.ModelOptions...)
	modelOpts = append(modelOpts, st.cfg.modelOpts...)
	infos, _ := collectTools(st.mode.Tools)

	ctx = r.beforeModel(ctx, st.agent.Name(), st.mode.Name, modelMessages, infos)

	var turnMsg *message.Message
	for event, err := range r.model.Stream(ctx, modelMessages, infos, modelOpts...) {
		if err != nil {
			r.emitErr(ctx, yield, err)
			return ctx, nil, false
		}
		// TurnMessage is the model layer's per-turn terminal event: it must be
		// the last event of the turn. Any event after it (including a second
		// TurnMessage) violates the stream contract.
		if turnMsg != nil {
			r.emitErr(ctx, yield, fmt.Errorf("%w: event after TurnMessage", ErrStreamContract))
			return ctx, nil, false
		}
		switch ev := event.(type) {
		case model.TurnMessage:
			// Capture it and forward it as the turn snapshot; do not relay it raw.
			msg := ev.Message
			turnMsg = &msg
			if !yield(model.TurnMessage{Message: msg}, nil) {
				return ctx, nil, false
			}
		case model.FinalMessage:
			// FinalMessage is the Runner's run-terminal event; a provider that
			// yields it has violated the stream contract. Fail loudly.
			r.emitErr(ctx, yield, fmt.Errorf("%w: provider yielded FinalMessage", ErrStreamContract))
			return ctx, nil, false
		case model.Suspended:
			// Suspended is also a Runner-only terminal event (emitted by the
			// Stream path on Policy suspend); a provider must never yield it,
			// or it would forge a terminal state and break I3.
			r.emitErr(ctx, yield, fmt.Errorf("%w: provider yielded Suspended", ErrStreamContract))
			return ctx, nil, false
		default:
			if !yield(event, nil) {
				return ctx, nil, false
			}
		}
	}
	if turnMsg == nil {
		r.emitErr(ctx, yield, fmt.Errorf("%w: stream ended without a TurnMessage", ErrStreamContract))
		return ctx, nil, false
	}
	finalMsg := turnMsg
	r.afterModel(ctx, st.agent.Name(), st.mode.Name, finalMsg)
	st.history = append(st.history, *finalMsg)
	return ctx, finalMsg, true
}

// streamToolCalls authorizes the whole batch first, then executes it serially or
// in parallel depending on the batch's ParallelSafe declarations. It emits
// ToolCall/ToolResult/Handoff events, then appends a single RoleTool message
// with all results. The bool result is false when iteration should stop.
func (r *Runner) streamToolCalls(ctx context.Context, st *runState, calls []message.ToolUse, yield func(model.Event, error) bool) (context.Context, bool) {
	_, toolsByName := collectTools(st.mode.Tools)
	selected, pending, err := r.authorizeBatch(ctx, st, toolsByName, calls)
	if err != nil {
		r.emitErr(ctx, yield, err)
		return ctx, false
	}
	// A Policy suspend halts the Stream with a terminal model.Suspended event
	// carrying the neutral snapshot (loop-semantics §5). Like the Run path it is
	// not an error: no tool ran, no ToolCall was emitted, and iteration ends
	// without a StreamError. The caller rebuilds a SuspendedRun via
	// SuspendedRunFrom and resumes with ResumeApproved.
	if len(pending) > 0 {
		r.emitSuspended(st, pending, yield)
		return ctx, false
	}
	if r.shouldRunBatchParallel(selected) {
		return r.finishStreamToolCallsParallel(ctx, st, selected, calls, yield)
	}
	return r.streamToolCallsSerialAuthorized(ctx, st, selected, calls, yield)
}

func (r *Runner) streamToolCallsSerialAuthorized(ctx context.Context, st *runState, selected []tool.Tool, calls []message.ToolUse, yield func(model.Event, error) bool) (context.Context, bool) {
	blocks := make([]message.Block, 0, len(calls))
	for i, call := range calls {
		if err := ctx.Err(); err != nil {
			r.emitErr(ctx, yield, err)
			return ctx, false
		}
		if !yield(model.ToolCall{Call: call}, nil) {
			return ctx, false
		}
		newCtx, out, err := r.execute(ctx, st, selected[i], call)
		if err != nil {
			r.emitErr(ctx, yield, err)
			return ctx, false
		}
		ctx = newCtx
		if !yield(model.ToolResult{CallID: call.ID, Name: call.Name, Result: out}, nil) {
			return ctx, false
		}
		blocks = append(blocks, message.NewToolResult(call.ID, call.Name, out.Content, out.IsError))
	}
	if ctx, ok := r.emitHandoffs(ctx, st, selected, yield); !ok {
		return ctx, false
	}
	st.history = append(st.history, message.ToolResults(blocks...))
	return ctx, true
}

// emitHandoffs applies every handoff tool in the batch in call order (last
// wins) and emits a Handoff event for each. It is shared by the serial and
// parallel stream paths so handoffs are always batch-terminal.
func (r *Runner) emitHandoffs(ctx context.Context, st *runState, selected []tool.Tool, yield func(model.Event, error) bool) (context.Context, bool) {
	for _, t := range selected {
		handoff, ok := t.(agent.HandoffTool)
		if !ok {
			continue
		}
		if err := st.switchTo(handoff); err != nil {
			r.emitErr(ctx, yield, err)
			return ctx, false
		}
		if !yield(model.Handoff{Target: st.agent.Name()}, nil) {
			return ctx, false
		}
	}
	return ctx, true
}

// streamResult is one parallel tool execution carrying its call index so the
// consumer can keep results in call order regardless of completion order.
// inducedCancel mirrors toolOutcome: a context.Canceled caused by the batch's
// own fail-fast cancellation rather than a genuine error.
type streamResult struct {
	index         int
	out           tool.Result
	err           error
	inducedCancel bool
}

// finishStreamToolCallsParallel emits every ToolCall event in call order,
// executes an already-authorized, ParallelSafe batch concurrently, and emits
// ToolResult events in completion order. All yields happen on this single
// goroutine, so the iterator stays safe. After execution it reports the first
// error by call order, otherwise applies handoffs and appends the batched
// RoleTool message.
func (r *Runner) finishStreamToolCallsParallel(ctx context.Context, st *runState, selected []tool.Tool, calls []message.ToolUse, yield func(model.Event, error) bool) (context.Context, bool) {
	for _, call := range calls {
		if !yield(model.ToolCall{Call: call}, nil) {
			return ctx, false
		}
	}
	ch, cancel := r.dispatchTools(ctx, st, selected, calls)
	defer cancel(nil)
	outcomes, stopped := r.drainToolResults(ch, cancel, calls, yield)
	if stopped {
		return ctx, false
	}
	if err := r.firstBatchError(ctx, outcomes); err != nil {
		r.emitErr(ctx, yield, err)
		return ctx, false
	}
	return r.finalizeStreamBatch(ctx, st, selected, calls, outcomes, yield)
}

// dispatchTools launches one goroutine per call, bounded by a maxConcurrency
// semaphore, and returns a channel that delivers each result then closes. The
// returned cancel stops ctx-aware siblings; the first tool error also cancels
// with cause errSiblingFailed so induced cancellations can be told apart.
func (r *Runner) dispatchTools(ctx context.Context, st *runState, selected []tool.Tool, calls []message.ToolUse) (<-chan streamResult, context.CancelCauseFunc) {
	parent := ctx
	ctx, cancel := context.WithCancelCause(ctx)
	ch := make(chan streamResult, len(calls))
	sem := make(chan struct{}, r.maxConcurrency)
	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := ctx.Err(); err != nil {
				ch <- streamResult{index: i, err: err, inducedCancel: isInducedCancel(parent, ctx, err)}
				return
			}
			_, out, err := r.execute(ctx, st, selected[i], calls[i])
			induced := isInducedCancel(parent, ctx, err)
			if err != nil {
				cancel(errSiblingFailed)
			}
			ch <- streamResult{index: i, out: out, err: err, inducedCancel: induced}
		}(i)
	}
	go func() { wg.Wait(); close(ch) }()
	return ch, cancel
}

// drainToolResults consumes the result channel to completion, yielding a
// ToolResult event for each successful tool as it arrives, and stores outcomes
// at their call index. If the consumer stops, it cancels and keeps draining so
// no goroutine leaks; the bool reports whether iteration should stop.
func (r *Runner) drainToolResults(ch <-chan streamResult, cancel context.CancelCauseFunc, calls []message.ToolUse, yield func(model.Event, error) bool) ([]toolOutcome, bool) {
	outcomes := make([]toolOutcome, len(calls))
	stopped := false
	for res := range ch {
		outcomes[res.index] = toolOutcome{out: res.out, err: res.err, inducedCancel: res.inducedCancel}
		if stopped || res.err != nil {
			continue
		}
		call := calls[res.index]
		if !yield(model.ToolResult{CallID: call.ID, Name: call.Name, Result: res.out}, nil) {
			stopped = true
			cancel(errSiblingFailed)
		}
	}
	return outcomes, stopped
}

// finalizeStreamBatch applies handoffs in call order, emitting a Handoff event
// for each, then appends the batched RoleTool message in call order.
func (r *Runner) finalizeStreamBatch(ctx context.Context, st *runState, selected []tool.Tool, calls []message.ToolUse, outcomes []toolOutcome, yield func(model.Event, error) bool) (context.Context, bool) {
	if ctx, ok := r.emitHandoffs(ctx, st, selected, yield); !ok {
		return ctx, false
	}
	st.history = append(st.history, message.ToolResults(resultBlocks(calls, outcomes)...))
	return ctx, true
}
