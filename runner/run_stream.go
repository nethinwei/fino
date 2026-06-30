package runner

import (
	"github.com/nethinwei/fino/model"
)

// StreamStep runs one ReAct turn like Step but yields semantic events through
// yield as they occur: model-layer deltas and the turn's TurnMessage, then
// ToolCall/ToolResult/Handoff around the batch's tool execution. It is the
// single-step streaming primitive; the outer loop (x/react) calls it repeatedly
// until a terminal Status ends the run.
//
// The streaming tool-batch machinery — authorize-all-before-execute, the
// terminal model.Suspended event, effect-aware parallel dispatch, and the
// stream-contract checks (rejecting provider-yielded FinalMessage/Suspended/
// ToolCall/ToolResult/Handoff) — stays in the core because it is execution-
// consistency machinery, not loop orchestration.
//
// Status meanings: StepCompleted (FinalMessage emitted, run done), StepSuspended
// (model.Suspended emitted, awaiting approval), StepStopped (consumer stopped
// or an error was already emitted as a StreamError), StepContinue (batch ran,
// call StreamStep again). A non-nil error is returned only alongside an emitted
// StreamError; the outer loop should stop in either case.
func (rn *Run) StreamStep(yield func(model.Event, error) bool) (StepOutcome, error) {
	ctx := rn.Context()
	if err := ctx.Err(); err != nil {
		rn.r.emitErr(ctx, yield, err)
		return StepOutcome{Status: StepStopped}, err
	}
	newCtx, msg, ok := rn.r.streamGenerate(ctx, rn.st, yield)
	if !ok {
		// streamGenerate already emitted a StreamError on a model/contract
		// failure, or the consumer stopped; either way this turn is done.
		return StepOutcome{Status: StepStopped}, nil
	}
	rn.lastCtx = newCtx
	calls := msg.ToolUses()
	if len(calls) == 0 {
		if !yield(model.FinalMessage{Message: *msg}, nil) {
			return StepOutcome{Status: StepStopped}, nil
		}
		return StepOutcome{Status: StepCompleted, FinalMessage: *msg}, nil
	}
	newCtx, pending, ok := rn.r.streamToolCalls(newCtx, rn.st, calls, yield)
	if !ok {
		if len(pending) > 0 {
			return StepOutcome{Status: StepSuspended, PendingCalls: pending}, nil
		}
		return StepOutcome{Status: StepStopped}, nil
	}
	rn.lastCtx = newCtx
	return StepOutcome{Status: StepContinue}, nil
}

// StreamResumePendingIfEnabled is the streaming form of ResumePendingIfEnabled:
// when WithResumeFromPendingTools is set and the input history's tail carries
// unresolved tool_uses, it executes that batch while emitting ToolCall/
// ToolResult events (and a terminal model.Suspended if the batch suspends)
// before the first model turn. It is a no-op (returning StepContinue) when the
// seam is off or the tail has no pending tool_use. A StepSuspended outcome
// means the batch suspended; StepStopped means an error was already emitted as
// a StreamError or the consumer stopped; StepContinue means the outer loop
// should proceed to StreamStep.
func (rn *Run) StreamResumePendingIfEnabled(yield func(model.Event, error) bool) (StepOutcome, error) {
	if !rn.st.cfg.resumePending {
		return StepOutcome{Status: StepContinue}, nil
	}
	pending := pendingToolUses(rn.st.history)
	if len(pending) == 0 {
		return StepOutcome{Status: StepContinue}, nil
	}
	ctx := rn.Context()
	if err := ctx.Err(); err != nil {
		rn.r.emitErr(ctx, yield, err)
		return StepOutcome{Status: StepStopped}, err
	}
	newCtx, pendingOut, ok := rn.r.streamToolCalls(ctx, rn.st, pending, yield)
	if !ok {
		if len(pendingOut) > 0 {
			return StepOutcome{Status: StepSuspended, PendingCalls: pendingOut}, nil
		}
		return StepOutcome{Status: StepStopped}, nil
	}
	rn.lastCtx = newCtx
	return StepOutcome{Status: StepContinue}, nil
}
