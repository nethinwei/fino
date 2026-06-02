package eval_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
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

// denyFixtureLog records a tool call whose policy_decision is a deny, plus the
// tool/model trace a default (AllowAll) replay would take if it ignored policy.
func denyFixtureLog() *replay.Log {
	toolUse := message.Assistant(message.NewToolUse("c1", "noop", json.RawMessage(`{}`)))
	final := message.Assistant(message.NewText("done"))
	rec := replay.ToolRecord{
		Name:   "noop",
		Input:  json.RawMessage(`{}`),
		Result: tool.Result{Content: []message.Block{message.NewText("ok")}},
	}
	return &replay.Log{
		Model: []message.Message{toolUse, final},
		Tools: []replay.ToolRecord{rec},
		Events: []replay.Event{
			{Kind: replay.EventModelResponse, ModelResponse: &replay.ModelResponseEvent{Message: toolUse}},
			{Kind: replay.EventPolicyDecision, PolicyDecision: &replay.PolicyDecisionEvent{
				Request: policy.Request{
					AgentName: "worker", ModeName: "default",
					Tool: tool.Info{Name: "noop"}, Input: json.RawMessage(`{}`),
				},
				Decision: policy.Decision{Kind: policy.DecisionDeny, Reason: "denied by tape"},
			}},
			{Kind: replay.EventToolExecution, ToolExecution: &replay.ToolExecutionEvent{Record: rec}},
			{Kind: replay.EventModelResponse, ModelResponse: &replay.ModelResponseEvent{Message: final}},
		},
	}
}

func buildPolicyAgent(log *replay.Log) func() (*agent.Agent, error) {
	return func() (*agent.Agent, error) {
		mode, err := agent.NewMode("default", "worker", agent.WithTools(replay.ReplayTool("noop", log)))
		if err != nil {
			return nil, err
		}
		return agent.New("worker", agent.WithMode(mode), agent.WithDefaultMode("default"))
	}
}

// TestEvalRunWithOptionsAppliesReplayPolicy proves the runner options passed to
// RunWithOptions actually reach runner.New: the same case completes under the
// default AllowAll policy but fails through the runner deny path once a
// ReplayPolicy replays the recorded deny.
func TestEvalRunWithOptionsAppliesReplayPolicy(t *testing.T) {
	log := denyFixtureLog()
	c := eval.Case{
		Name:  "policy",
		Log:   log,
		Agent: buildPolicyAgent(log),
		Input: runner.Text("go"),
		Assert: func(res *runner.Result) error {
			if res.Text() != "done" {
				return errors.New("final text = " + res.Text() + ", want done")
			}
			return nil
		},
	}

	if err := eval.Run(context.Background(), c); err != nil {
		t.Fatalf("eval.Run with default AllowAll policy: %v", err)
	}

	err := eval.RunWithOptions(context.Background(), c, runner.WithPolicy(&replay.ReplayPolicy{Log: log}))
	if !errors.Is(err, runner.ErrToolDenied) {
		t.Fatalf("RunWithOptions err = %v, want ErrToolDenied", err)
	}
}
