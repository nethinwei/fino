package runner

import (
	"context"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
)

// Run is one in-progress run session: the active agent and mode, the accumulated
// message history, and the resolved per-run config. Each Run owns its own
// message list; a Runner is immutable configuration and may create many Runs.
//
// Run exposes the ReAct loop's single-step primitives so an outer loop (such as
// x/react, or application code) can drive the turns itself. The core does not
// provide a multi-turn loop; it provides one turn at a time via Step and
// StreamStep, plus the suspend/resume and tool-execution consistency mechanisms
// that cannot be reconstructed by wrapping Tool, Policy, Hook, Mode, or Model.
type Run struct {
	r  *Runner
	st *runState
	// ctx is the base context supplied at construction. lastCtx is the
	// context as updated by lifecycle hooks (BeforeModel/BeforeTool) during the
	// last step; it is carried across steps so hook-injected context propagates
	// exactly as it did inside the old internal loop.
	ctx     context.Context
	lastCtx context.Context
}

// NewRun creates a Run session against the agent. It validates inputs and
// resolves the starting mode but executes nothing; the caller drives turns with
// Step (or StreamStep) and optionally ResumePendingIfEnabled first. The context
// is carried across steps and is the one observed by every model call, tool
// execution, and hook in the run.
func (r *Runner) NewRun(ctx context.Context, a *agent.Agent, input Input, opts ...RunOption) (*Run, error) {
	st, err := r.prepareRun(a, input, opts)
	if err != nil {
		return nil, err
	}
	return &Run{r: r, st: st, ctx: ctx}, nil
}

// Context returns the run's current context: the hook-updated context from the
// last step if any, otherwise the base context supplied at construction. It is
// the context an outer loop should use when reporting a loop-level error
// (e.g. max turns) so the OnError hook observes the same context the steps did.
func (rn *Run) Context() context.Context {
	if rn.lastCtx != nil {
		return rn.lastCtx
	}
	return rn.ctx
}

// History returns the accumulated message history of the run. The slice aliases
// the run's internal storage; callers must not mutate it.
func (rn *Run) History() []message.Message { return rn.st.history }

// Agent returns the currently active agent. Handoffs during a run switch this.
func (rn *Run) Agent() *agent.Agent { return rn.st.agent }

// Mode returns the currently active mode.
func (rn *Run) Mode() agent.Mode { return rn.st.mode }

// RunID returns the run-scoped identifier (runner.WithRunID), empty when unset.
func (rn *Run) RunID() string { return rn.st.cfg.runID }

// StepStatus is the outcome category of a single Step or StreamStep.
type StepStatus uint8

const (
	// StepContinue means the turn ran a tool batch that completed without
	// suspending; the outer loop should call Step again for the next turn.
	StepContinue StepStatus = iota
	// StepCompleted means the model returned a message with no tool calls; the
	// run is done and FinalMessage holds the terminal assistant message.
	StepCompleted
	// StepSuspended means a Policy suspended the turn's tool batch before any
	// tool executed; PendingCalls holds the suspended calls. The outer loop
	// should stop and surface the suspension for approval.
	StepSuspended
	// StepStopped means streaming iteration was stopped by the consumer (yield
	// returned false) before the turn completed. Only StreamStep returns it.
	StepStopped
)

// StepOutcome is the result of one Step or StreamStep.
type StepOutcome struct {
	Status StepStatus
	// FinalMessage is the terminal assistant message when Status is StepCompleted.
	FinalMessage message.Message
	// PendingCalls are the suspended calls when Status is StepSuspended.
	PendingCalls []PendingToolCall
}

// Step runs one ReAct turn: it calls the model, and if the model requested
// tools it authorizes and executes the batch (firing hooks, injecting the
// idempotency context, honoring effect-aware concurrency) and appends the
// results to history. It does not loop and does not count turns; the outer loop
// decides whether to call Step again based on Status.
//
// A StepCompleted outcome ends the run; StepSuspended ends it pending approval;
// StepContinue means call Step again. A non-nil error always means the run
// cannot continue (model failure, tool failure, or context cancellation); the
// OnError hook has already fired for such errors.
func (rn *Run) Step() (StepOutcome, error) {
	ctx := rn.Context()
	if err := ctx.Err(); err != nil {
		rn.r.onError(ctx, err)
		return StepOutcome{}, err
	}
	newCtx, msg, err := rn.r.generate(ctx, rn.st)
	if err != nil {
		return StepOutcome{}, err
	}
	rn.lastCtx = newCtx
	calls := msg.ToolUses()
	if len(calls) == 0 {
		return StepOutcome{Status: StepCompleted, FinalMessage: *msg}, nil
	}
	newCtx, pending, err := rn.r.runToolCalls(newCtx, rn.st, calls)
	if err != nil {
		return StepOutcome{}, err
	}
	rn.lastCtx = newCtx
	if len(pending) > 0 {
		return StepOutcome{Status: StepSuspended, PendingCalls: pending}, nil
	}
	return StepOutcome{Status: StepContinue}, nil
}

// ResumePendingIfEnabled is the single-step form of the resume-from-pending
// seam: when WithResumeFromPendingTools is set and the input history's tail
// carries unresolved tool_uses, it executes that batch (authorizing, firing
// hooks, appending one RoleTool message) before the first model turn. It is a
// no-op when the seam is off or the tail has no pending tool_use. A non-empty
// pending return means the batch suspended and the run should halt for approval
// instead of entering the model-turn loop.
func (rn *Run) ResumePendingIfEnabled() ([]PendingToolCall, error) {
	if !rn.st.cfg.resumePending {
		return nil, nil
	}
	pending := pendingToolUses(rn.st.history)
	if len(pending) == 0 {
		return nil, nil
	}
	ctx := rn.Context()
	if err := ctx.Err(); err != nil {
		rn.r.onError(ctx, err)
		return nil, err
	}
	newCtx, pendingOut, err := rn.r.runToolCalls(ctx, rn.st, pending)
	if err != nil {
		return nil, err
	}
	rn.lastCtx = newCtx
	return pendingOut, nil
}

// Result builds a completed Result from the run state and the terminal assistant
// message. An outer loop calls it when a Step returns StepCompleted.
func (rn *Run) Result(msg message.Message) *Result {
	return rn.st.result(&msg)
}

// SuspendedResult builds a suspended Result from the run state and the batch's
// pending calls. An outer loop calls it when a Step (or ResumePendingIfEnabled)
// returns a suspension. The history tail is the dangling assistant tool_use
// message; no tool_result has been appended.
func (rn *Run) SuspendedResult(pending []PendingToolCall) *Result {
	return rn.st.suspendedResult(pending)
}

// FireOnError fires the OnError hook for a loop-level error that the core's
// single-step primitives did not observe themselves — for example a max-turns
// limit enforced by an outer loop (x/react). Core-internal errors (model
// failure, tool failure, context cancellation) already fire OnError inside the
// step primitives and must not fire it twice. The context should be the run's
// current context (Run.Context).
func (r *Runner) FireOnError(ctx context.Context, err error) {
	r.onError(ctx, err)
}
