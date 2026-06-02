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
// assertion. It returns nil on success or a descriptive error.
func Run(ctx context.Context, c Case) error {
	a, err := c.Agent()
	if err != nil {
		return fmt.Errorf("eval %q: build agent: %w", c.Name, err)
	}
	r, err := runner.New(&replay.ReplayModel{Log: c.Log})
	if err != nil {
		return fmt.Errorf("eval %q: new runner: %w", c.Name, err)
	}
	res, err := r.Run(ctx, a, c.Input)
	if err != nil {
		return fmt.Errorf("eval %q: run: %w", c.Name, err)
	}
	if err := c.Assert(res); err != nil {
		return fmt.Errorf("eval %q: assertion failed: %w", c.Name, err)
	}
	return nil
}
