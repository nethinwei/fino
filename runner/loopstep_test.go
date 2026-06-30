package runner

import (
	"context"
	"fmt"
	"iter"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/model"
)

// These methods exist only in the test build. The core no longer ships a
// multi-turn loop — x/react.Loop is the reference loop — but the runner
// package's own tests were written against Runner.Run/Stream/ResumeApproved.
// Rather than rewrite every call site, these test-only helpers rebuild the same
// loop from the core's single-step primitives (NewRun + Step / StreamStep +
// NewResumeRun), so the tests keep driving a multi-turn run without the core
// exposing one publicly. They are unavailable to non-test builds and to other
// packages.
//
// The loop here mirrors x/react.Loop.Run/Stream/ResumeApproved. The two cannot
// share code: runner (even its tests) cannot import x/react without an import
// cycle. If the loop's turn/termination semantics change, update both copies.
// The mirror is the cost of keeping the runner package's existing multi-turn
// tests in place; migrating those tests to x/react would drop it.

const testDefaultMaxTurns = 10

// RunMax drives a Run via Step with an explicit turn limit. It mirrors
// x/react.Loop.Run for tests that exercise the core's multi-step behavior.
func (r *Runner) RunMax(maxTurns int, ctx context.Context, a *agent.Agent, input Input, opts ...RunOption) (*Result, error) {
	rn, err := r.NewRun(ctx, a, input, opts...)
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
	for i := 0; i < maxTurns; i++ {
		out, err := rn.Step()
		if err != nil {
			return nil, err
		}
		switch out.Status {
		case StepCompleted:
			return rn.Result(out.FinalMessage), nil
		case StepSuspended:
			return rn.SuspendedResult(out.PendingCalls), nil
		}
	}
	err = fmt.Errorf("%w: %d", ErrMaxTurns, maxTurns)
	r.FireOnError(rn.Context(), err)
	return nil, err
}

// Run is RunMax with the default turn limit, matching the shape the core used
// to provide.
func (r *Runner) Run(ctx context.Context, a *agent.Agent, input Input, opts ...RunOption) (*Result, error) {
	return r.RunMax(testDefaultMaxTurns, ctx, a, input, opts...)
}

// StreamMax drives a Run via StreamStep with an explicit turn limit.
func (r *Runner) StreamMax(maxTurns int, ctx context.Context, a *agent.Agent, input Input, opts ...RunOption) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		rn, err := r.NewRun(ctx, a, input, opts...)
		if err != nil {
			r.FireOnError(ctx, err)
			yield(model.StreamError{Err: err}, err)
			return
		}
		out, err := rn.StreamResumePendingIfEnabled(yield)
		if err != nil {
			return
		}
		if out.Status != StepContinue {
			return
		}
		for i := 0; i < maxTurns; i++ {
			out, err := rn.StreamStep(yield)
			if err != nil {
				return
			}
			if out.Status != StepContinue {
				return
			}
		}
		err = fmt.Errorf("%w: %d", ErrMaxTurns, maxTurns)
		r.FireOnError(rn.Context(), err)
		yield(model.StreamError{Err: err}, err)
	}
}

// Stream is StreamMax with the default turn limit.
func (r *Runner) Stream(ctx context.Context, a *agent.Agent, input Input, opts ...RunOption) iter.Seq2[model.Event, error] {
	return r.StreamMax(testDefaultMaxTurns, ctx, a, input, opts...)
}

// ResumeApproved rebuilds a run from a suspended snapshot via NewResumeRun
// (which executes the approved batch without re-consulting the policy), then
// drives the post-resume turns with Step.
func (r *Runner) ResumeApproved(ctx context.Context, a *agent.Agent, suspended SuspendedRun, approvals []Approval, opts ...RunOption) (*Result, error) {
	rn, err := r.NewResumeRun(ctx, a, suspended, approvals, opts...)
	if err != nil {
		return nil, err
	}
	for i := 0; i < testDefaultMaxTurns; i++ {
		out, err := rn.Step()
		if err != nil {
			return nil, err
		}
		switch out.Status {
		case StepCompleted:
			return rn.Result(out.FinalMessage), nil
		case StepSuspended:
			return rn.SuspendedResult(out.PendingCalls), nil
		}
	}
	err = fmt.Errorf("%w: %d", ErrMaxTurns, testDefaultMaxTurns)
	r.FireOnError(rn.Context(), err)
	return nil, err
}
