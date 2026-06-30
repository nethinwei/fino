// Package eval provides reproducible regression testing for fino agents.
//
// It is a reference composition for the sufficiency thesis in docs/design.md
// and a direct corollary of x/replay: given a recorded Log, a run is fully
// deterministic, so agent behavior becomes a regular regression test. eval adds
// no core capability.
package eval

import (
	"context"
	"fmt"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/x/react"
	"github.com/nethinwei/fino/x/replay"
)

// Case is one reproducible evaluation: a recorded Log, a factory that builds an
// agent whose tools are replay tools backed by the Log, an input, and an
// assertion over the final Result.
//
// The Agent factory must build the agent with replay tools (replay.ReplayTool)
// backed by the same Log. eval does not verify this; an agent wired with real
// tools would execute them and break determinism. This is a user-held contract.
type Case struct {
	Name   string
	Log    *replay.Log
	Agent  func() (*agent.Agent, error)
	Input  runner.Input
	Assert func(*runner.Result) error
}

// Run executes the case deterministically against a ReplayModel and applies the
// assertion. It returns nil on success or a descriptive error. It delegates to
// RunWithOptions with no extra runner options (default AllowAll policy).
func Run(ctx context.Context, c Case) error {
	return RunWithOptions(ctx, c)
}

// RunWithOptions is Run with explicit runner options. Tests that need full tape
// fidelity can pass runner.WithPolicy(&replay.ReplayPolicy{Log: c.Log}) so a
// policy-sensitive run replays its recorded decisions instead of falling back to
// the default AllowAll. The Case shape is unchanged for compatibility.
func RunWithOptions(ctx context.Context, c Case, opts ...runner.Option) error {
	a, err := c.Agent()
	if err != nil {
		return fmt.Errorf("eval %q: build agent: %w", c.Name, err)
	}
	r, err := runner.New(&replay.ReplayModel{Log: c.Log}, opts...)
	if err != nil {
		return fmt.Errorf("eval %q: new runner: %w", c.Name, err)
	}
	l, err := react.New(r)
	if err != nil {
		return fmt.Errorf("eval %q: new loop: %w", c.Name, err)
	}
	res, err := l.Run(ctx, a, c.Input)
	if err != nil {
		return fmt.Errorf("eval %q: run: %w", c.Name, err)
	}
	if err := c.Assert(res); err != nil {
		return fmt.Errorf("eval %q: assertion failed: %w", c.Name, err)
	}
	return nil
}
