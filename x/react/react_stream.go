package react

import (
	"context"
	"fmt"
	"iter"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/runner"
)

// Stream executes the ReAct loop like Run but yields semantic events as they
// occur. Iteration stops after a final message with no tool calls, a tool batch
// suspends, the turn limit is reached, or an error occurs. Terminal errors are
// yielded as a model.StreamError alongside a non-nil iterator error; a
// suspension is yielded as a terminal model.Suspended. The arguments mirror Run.
func (l *Loop) Stream(ctx context.Context, a *agent.Agent, input runner.Input, opts ...runner.RunOption) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		l.streamLoop(ctx, yield, a, input, opts)
	}
}

func (l *Loop) streamLoop(ctx context.Context, yield func(model.Event, error) bool, a *agent.Agent, input runner.Input, opts []runner.RunOption) {
	rn, err := l.r.NewRun(ctx, a, input, opts...)
	if err != nil {
		l.r.FireOnError(ctx, err)
		yield(model.StreamError{Err: err}, err)
		return
	}
	// Execute the resume-from-pending tail batch (if the seam is enabled) before
	// the first model turn, emitting its ToolCall/ToolResult events. A non-
	// StepContinue outcome (suspend, error, or consumer-stop) ends iteration.
	out, err := rn.StreamResumePendingIfEnabled(yield)
	if err != nil {
		return
	}
	if out.Status != runner.StepContinue {
		return
	}
	for turn := 0; turn < l.maxTurns; turn++ {
		out, err := rn.StreamStep(yield)
		if err != nil {
			return
		}
		if out.Status != runner.StepContinue {
			return
		}
	}
	err = fmt.Errorf("%w: %d", runner.ErrMaxTurns, l.maxTurns)
	l.r.FireOnError(rn.Context(), err)
	yield(model.StreamError{Err: err}, err)
}
