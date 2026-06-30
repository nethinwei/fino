package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
)

// NewResumeRun continues a suspended run after a human has approved or rejected
// its pending tool calls. It is the single-step core primitive for resume: it
// validates that the agent matches the one active at suspend time and that the
// approvals validly cover the pending calls, then executes the batch in call
// order — approved (and previously-allowed) calls run their tools; rejected
// calls produce a model-visible error tool_result — and appends one RoleTool
// message. It does not re-consult the Policy: human approval replaces policy
// authorization for the suspended calls. It does not resume the model-turn
// loop; the returned Run is ready for the caller to drive Step (or StreamStep)
// for the post-resume turns.
//
// The resumed batch's IdempotencyKey matches the original run: the suspended
// RunID always wins over any WithRunID passed here (loop-semantics I13). The
// mode cannot be overridden on resume (the suspended tool_uses were resolved
// against suspended.LastMode).
func (r *Runner) NewResumeRun(ctx context.Context, a *agent.Agent, suspended SuspendedRun, approvals []Approval, opts ...RunOption) (*Run, error) {
	if a == nil {
		return nil, errors.New("agent is required")
	}
	if a.Name() != suspended.LastAgentName {
		return nil, fmt.Errorf("%w: passed %q, want %q", ErrResumeAgentMismatch, a.Name(), suspended.LastAgentName)
	}
	if err := validateSuspendedRun(suspended, approvals); err != nil {
		return nil, err
	}
	mode, ok := a.Mode(suspended.LastMode)
	if !ok {
		return nil, fmt.Errorf("mode %q not found", suspended.LastMode)
	}
	cfg := runConfig{modeName: suspended.LastMode}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	// The resumed batch's IdempotencyKey must match the original run, so the
	// suspended RunID always wins over any WithRunID passed on resume (I13).
	cfg.runID = suspended.RunID
	// Mode cannot be overridden on resume: the suspended batch's tool_uses were
	// resolved against suspended.LastMode, so resuming in another mode would
	// resolve them in the wrong tool set. Other run options are honored.
	if cfg.modeName != suspended.LastMode {
		return nil, fmt.Errorf("%w: cannot override mode on resume (got %q, suspended in %q)",
			ErrInvalidApproval, cfg.modeName, suspended.LastMode)
	}
	st := &runState{
		agent:   a,
		mode:    mode,
		history: append([]message.Message(nil), suspended.Messages...),
		cfg:     cfg,
	}
	rn := &Run{r: r, st: st, ctx: ctx}
	newCtx, err := r.resumeExecuteBatch(ctx, st, suspended.PendingCalls, approvals)
	if err != nil {
		return nil, err
	}
	rn.lastCtx = newCtx
	return rn, nil
}
