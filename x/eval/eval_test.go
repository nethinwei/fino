package eval_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/x/eval"
	"github.com/nethinwei/fino/x/replay"
)

// fixtureLog builds a recorded Log directly (as if produced by x/replay) for a
// simple single-turn run that ends in the given final text.
func fixtureLog(finalText string) *replay.Log {
	return &replay.Log{
		Model: []message.Message{message.Assistant(message.NewText(finalText))},
	}
}

func buildAgent() (*agent.Agent, error) {
	mode, err := agent.NewMode("default", "worker")
	if err != nil {
		return nil, err
	}
	return agent.New("worker", agent.WithMode(mode), agent.WithDefaultMode("default"))
}

func newCase(name, finalText string) eval.Case {
	return eval.Case{
		Name:  name,
		Log:   fixtureLog(finalText),
		Agent: buildAgent,
		Input: runner.Text("go"),
		Assert: func(res *runner.Result) error {
			if res.Text() != "done" {
				return errors.New("final text = " + res.Text() + ", want done")
			}
			return nil
		},
	}
}

func TestEvalPassesOnMatchingFixture(t *testing.T) {
	if err := eval.Run(context.Background(), newCase("ok", "done")); err != nil {
		t.Fatalf("expected pass, got: %v", err)
	}
}

func TestEvalFailsOnMutatedFixture(t *testing.T) {
	// Mutating the recorded final response must make the regression fail.
	if err := eval.Run(context.Background(), newCase("regression", "WRONG")); err == nil {
		t.Fatal("expected eval to fail on mutated fixture, got nil")
	}
}
