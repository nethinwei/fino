package runner

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/tool"
)

// T2: New rejects a nil model.
func TestNewRejectsNilModel(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) error = nil, want non-nil")
	}
}

// T4: WithMode selects a non-default mode; the run uses that mode's
// instructions as the system message.
func TestRunWithModeSelectsMode(t *testing.T) {
	def, err := agent.NewMode("code", "code mode")
	if err != nil {
		t.Fatalf("NewMode code: %v", err)
	}
	plan, err := agent.NewMode("plan", "plan mode")
	if err != nil {
		t.Fatalf("NewMode plan: %v", err)
	}
	a, err := agent.New("assistant",
		agent.WithMode(def), agent.WithMode(plan), agent.WithDefaultMode("code"))
	if err != nil {
		t.Fatalf("New agent: %v", err)
	}
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("ok"))}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}
	rn, err := r.NewRun(context.Background(), a, Text("hi"), WithMode("plan"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	res, err := runSteps(rn)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LastMode != "plan" {
		t.Fatalf("LastMode = %q, want plan", res.LastMode)
	}
	if got := m.calls[0][0].Text(); got != "plan mode" {
		t.Fatalf("system instructions = %q, want %q", got, "plan mode")
	}
}

// T5: after a handoff, a tool that existed only in the previous mode is
// resolved against the new mode and reported as not found.
func TestRunHandoffToolNotInNewMode(t *testing.T) {
	targetMode, err := agent.NewMode("default", "target")
	if err != nil {
		t.Fatalf("NewMode target: %v", err)
	}
	target, err := agent.New("target", agent.WithMode(targetMode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New target: %v", err)
	}
	handoff, err := agent.NewHandoffTool(target)
	if err != nil {
		t.Fatalf("NewHandoffTool: %v", err)
	}
	echo, err := tool.NewFunc("echo", "Echo", func(ctx context.Context, in echoInput) (string, error) {
		return in.Text, nil
	})
	if err != nil {
		t.Fatalf("NewFunc: %v", err)
	}
	sourceMode, err := agent.NewMode("default", "source", agent.WithTools(handoff, echo))
	if err != nil {
		t.Fatalf("NewMode source: %v", err)
	}
	source, err := agent.New("source", agent.WithMode(sourceMode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New source: %v", err)
	}
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "handoff_to_target", json.RawMessage(`{}`))),
		// echo exists in the source mode but not the target mode.
		message.Assistant(message.NewToolUse("call_2", "echo", json.RawMessage(`{"text":"x"}`))),
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}
	rn, err := r.NewRun(context.Background(), source, Text("hi"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	_, runErr := runSteps(rn)
	if !errors.Is(runErr, ErrToolNotFound) {
		t.Fatalf("Run error = %v, want ErrToolNotFound", runErr)
	}
}

// recordPolicy captures the last Request it authorized.
type recordPolicy struct {
	req policy.Request
}

func (p *recordPolicy) Authorize(ctx context.Context, req policy.Request) (policy.Decision, error) {
	p.req = req
	return policy.Decision{Allow: true}, nil
}

// T8: the Runner populates every policy.Request field from the run state and
// tool call.
func TestPolicyReceivesFullRequest(t *testing.T) {
	echo, err := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return in.Text, nil
	})
	if err != nil {
		t.Fatalf("NewFunc: %v", err)
	}
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
		message.Assistant(message.NewText("done")),
	}}
	pol := &recordPolicy{}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}
	rn, err := r.NewRun(context.Background(), testAgent(t, echo), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if _, err := runSteps(rn); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if pol.req.AgentName != "assistant" {
		t.Fatalf("AgentName = %q, want assistant", pol.req.AgentName)
	}
	if pol.req.ModeName != "default" {
		t.Fatalf("ModeName = %q, want default", pol.req.ModeName)
	}
	if pol.req.Tool.Name != "echo" {
		t.Fatalf("Tool.Name = %q, want echo", pol.req.Tool.Name)
	}
	if string(pol.req.Input) != `{"text":"go"}` {
		t.Fatalf("Input = %s, want %s", pol.req.Input, `{"text":"go"}`)
	}
}

// T8b: a tool's declared Effects are visible to the Policy via
// policy.Request.Tool.Effects without any change to the policy.Policy interface.
func TestPolicySeesToolEffects(t *testing.T) {
	want := tool.Effects{ReadOnly: true, Idempotent: true, ParallelSafe: true}
	echo, err := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return in.Text, nil
	}, tool.WithEffects(want))
	if err != nil {
		t.Fatalf("NewFunc: %v", err)
	}
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
		message.Assistant(message.NewText("done")),
	}}
	pol := &recordPolicy{}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}
	rn, err := r.NewRun(context.Background(), testAgent(t, echo), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if _, err := runSteps(rn); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := pol.req.Tool.Effects; got != want {
		t.Fatalf("Tool.Effects = %+v, want %+v", got, want)
	}
}
