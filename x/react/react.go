// Package react provides a reference multi-turn ReAct loop over fino's core
// single-step primitives.
//
// The fino core (package runner) intentionally does not provide a multi-turn
// loop: it exposes one turn at a time via Run.Step / Run.StreamStep plus the
// suspend/resume and tool-execution consistency mechanisms. This package wires
// those primitives into the turn-counted while-loop many agents want, mirroring
// the shape the core used to provide (Run / Stream / ResumeApproved) so callers
// that want a ready-made loop can use it, and callers that want to write their
// own can drive Run.Step directly.
package react

import (
	"context"
	"errors"
	"fmt"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/runner"
)

// Loop is a multi-turn ReAct loop over a core *runner.Runner. It holds only the
// loop-level configuration (the turn limit); all execution consistency —
// policy authorization, hooks, effect-aware concurrency, idempotency, suspend/
// resume — stays in the core and is invoked one turn at a time.
type Loop struct {
	r        *runner.Runner
	maxTurns int
}

// Option configures a Loop.
type Option func(*Loop)

// New creates a Loop over the given runner. The default turn limit is 10. It
// returns an error if the runner is nil or the configured maximum turns is not
// positive.
func New(r *runner.Runner, opts ...Option) (*Loop, error) {
	if r == nil {
		return nil, errors.New("runner is required")
	}
	l := &Loop{r: r, maxTurns: 10}
	for _, opt := range opts {
		if opt != nil {
			opt(l)
		}
	}
	if l.maxTurns <= 0 {
		return nil, errors.New("max turns must be positive")
	}
	return l, nil
}

// WithMaxTurns sets the maximum number of model turns per run.
func WithMaxTurns(n int) Option { return func(l *Loop) { l.maxTurns = n } }

// Runner returns the underlying core runner. It lets adapters (such as x/agui)
// reuse the runner's data types and single-step primitives without re-wrapping.
func (l *Loop) Runner() *runner.Runner { return l.r }

// Run executes the ReAct loop until the model returns a message with no tool
// calls, a tool batch suspends, the turn limit is reached, or an error occurs.
// It is the multi-turn composition of runner.Run.Step.
func (l *Loop) Run(ctx context.Context, a *agent.Agent, input runner.Input, opts ...runner.RunOption) (*runner.Result, error) {
	rn, err := l.r.NewRun(ctx, a, input, opts...)
	if err != nil {
		return nil, err
	}
	pending, err := rn.ResumePendingIfEnabled()
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		return rn.SuspendedResult(pending), nil
	}
	return l.driveSteps(rn)
}

// ResumeApproved continues a suspended run after a human has approved or rejected
// its pending tool calls. It rebuilds the run from the suspended snapshot via
// runner.Runner.NewResumeRun (which executes the approved batch without
// re-consulting the policy), then drives the post-resume turns with Step.
func (l *Loop) ResumeApproved(ctx context.Context, a *agent.Agent, suspended runner.SuspendedRun, approvals []runner.Approval, opts ...runner.RunOption) (*runner.Result, error) {
	rn, err := l.r.NewResumeRun(ctx, a, suspended, approvals, opts...)
	if err != nil {
		return nil, err
	}
	return l.driveSteps(rn)
}

// driveSteps runs Step until the run completes, suspends, or hits the turn
// limit. It is the shared post-setup loop for Run and ResumeApproved.
func (l *Loop) driveSteps(rn *runner.Run) (*runner.Result, error) {
	for turn := 0; turn < l.maxTurns; turn++ {
		out, err := rn.Step()
		if err != nil {
			return nil, err
		}
		switch out.Status {
		case runner.StepCompleted:
			return rn.Result(out.FinalMessage), nil
		case runner.StepSuspended:
			return rn.SuspendedResult(out.PendingCalls), nil
		}
	}
	err := fmt.Errorf("%w: %d", runner.ErrMaxTurns, l.maxTurns)
	l.r.FireOnError(rn.Context(), err)
	return nil, err
}
