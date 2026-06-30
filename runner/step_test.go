package runner

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/tool"
)

// stepSuspendPolicy suspends every tool call so a Step/StreamStep test can
// observe the StepSuspended outcome without executing the tool.
type stepSuspendPolicy struct{}

func (stepSuspendPolicy) Authorize(ctx context.Context, req policy.Request) (policy.Decision, error) {
	return policy.Decision{Kind: policy.DecisionSuspend, Reason: "need approval"}, nil
}

func TestNewRunStepCompleted(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("hello"))}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rn, err := r.NewRun(context.Background(), testAgent(t), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	out, err := rn.Step()
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if out.Status != StepCompleted {
		t.Fatalf("status = %v, want StepCompleted", out.Status)
	}
	if out.FinalMessage.Text() != "hello" {
		t.Fatalf("text = %q, want hello", out.FinalMessage.Text())
	}
	// History holds [user, assistant]; the core injects the system message at
	// model-call time, not into the run history.
	if len(rn.History()) != 2 {
		t.Fatalf("history len = %d, want 2", len(rn.History()))
	}
}

func TestStepToolBatchThenCompleted(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "echo: " + in.Text, nil
	})
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
		message.Assistant(message.NewText("final")),
	}}
	r, _ := New(m)
	rn, err := r.NewRun(context.Background(), testAgent(t, echo), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	out, err := rn.Step()
	if err != nil {
		t.Fatalf("Step 1: %v", err)
	}
	if out.Status != StepContinue {
		t.Fatalf("status 1 = %v, want StepContinue", out.Status)
	}
	// After the tool batch, history is [user, assistant(tool_use), tool(results)].
	if len(rn.History()) != 3 {
		t.Fatalf("history len after batch = %d, want 3", len(rn.History()))
	}
	out, err = rn.Step()
	if err != nil {
		t.Fatalf("Step 2: %v", err)
	}
	if out.Status != StepCompleted || out.FinalMessage.Text() != "final" {
		t.Fatalf("status 2 = %v text = %q, want StepCompleted/final", out.Status, out.FinalMessage.Text())
	}
}

func TestStepSuspended(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "should not run", nil
	})
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
	}}
	r, _ := New(m, WithPolicy(stepSuspendPolicy{}))
	rn, err := r.NewRun(context.Background(), testAgent(t, echo), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	out, err := rn.Step()
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if out.Status != StepSuspended {
		t.Fatalf("status = %v, want StepSuspended", out.Status)
	}
	if len(out.PendingCalls) != 1 || out.PendingCalls[0].Call.ID != "call_1" {
		t.Fatalf("pending = %+v, want one call_1", out.PendingCalls)
	}
	// No tool_result appended on suspend: history ends with the dangling tool_use.
	if rn.History()[len(rn.History())-1].Role != message.RoleAssistant {
		t.Fatalf("history tail role = %v, want assistant (dangling tool_use)", rn.History()[len(rn.History())-1].Role)
	}
}

func TestStepContextCancelled(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("hello"))}}
	r, _ := New(m)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rn, err := r.NewRun(ctx, testAgent(t), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	_, err = rn.Step()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestStreamStepCompleted(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("hello"))}}
	r, _ := New(m)
	rn, err := r.NewRun(context.Background(), testAgent(t), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	var sawFinal, sawTurn bool
	out, err := rn.StreamStep(func(ev model.Event, _ error) bool {
		switch ev.(type) {
		case model.TurnMessage:
			sawTurn = true
		case model.FinalMessage:
			sawFinal = true
		}
		return true
	})
	if err != nil {
		t.Fatalf("StreamStep: %v", err)
	}
	if out.Status != StepCompleted {
		t.Fatalf("status = %v, want StepCompleted", out.Status)
	}
	if !sawTurn || !sawFinal {
		t.Fatalf("events: sawTurn=%v sawFinal=%v, want both", sawTurn, sawFinal)
	}
}

func TestStreamStepToolBatch(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "echo: " + in.Text, nil
	})
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
	}}
	r, _ := New(m)
	rn, err := r.NewRun(context.Background(), testAgent(t, echo), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	var toolCalls, toolResults int
	out, err := rn.StreamStep(func(ev model.Event, _ error) bool {
		switch ev.(type) {
		case model.ToolCall:
			toolCalls++
		case model.ToolResult:
			toolResults++
		}
		return true
	})
	if err != nil {
		t.Fatalf("StreamStep: %v", err)
	}
	if out.Status != StepContinue {
		t.Fatalf("status = %v, want StepContinue", out.Status)
	}
	if toolCalls != 1 || toolResults != 1 {
		t.Fatalf("events: calls=%d results=%d, want 1/1", toolCalls, toolResults)
	}
}

func TestStreamStepSuspended(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "should not run", nil
	})
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
	}}
	r, _ := New(m, WithPolicy(stepSuspendPolicy{}))
	rn, err := r.NewRun(context.Background(), testAgent(t, echo), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	var sawSuspended bool
	out, err := rn.StreamStep(func(ev model.Event, _ error) bool {
		if _, ok := ev.(model.Suspended); ok {
			sawSuspended = true
		}
		return true
	})
	if err != nil {
		t.Fatalf("StreamStep: %v", err)
	}
	if out.Status != StepSuspended {
		t.Fatalf("status = %v, want StepSuspended", out.Status)
	}
	if !sawSuspended {
		t.Fatalf("did not see model.Suspended event")
	}
	if len(out.PendingCalls) != 1 {
		t.Fatalf("pending = %d, want 1", len(out.PendingCalls))
	}
}

func TestNewResumeRunContinues(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "echo: " + in.Text, nil
	})
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
		message.Assistant(message.NewText("done")),
	}}
	r, _ := New(m, WithPolicy(stepSuspendPolicy{}))
	rn, err := r.NewRun(context.Background(), testAgent(t, echo), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	out, err := rn.Step()
	if err != nil || out.Status != StepSuspended {
		t.Fatalf("Step suspend: err=%v status=%v", err, out.Status)
	}
	suspended := SuspendedRun{
		Messages:      rn.History(),
		LastAgentName: rn.Agent().Name(),
		LastMode:      rn.Mode().Name,
		PendingCalls:  out.PendingCalls,
	}
	// Approve the suspended call; NewResumeRun executes it (appending the
	// tool_result) without re-consulting the policy, then is ready to continue.
	rn2, err := r.NewResumeRun(context.Background(), testAgent(t, echo), suspended,
		[]Approval{{CallID: "call_1", Approved: true}})
	if err != nil {
		t.Fatalf("NewResumeRun: %v", err)
	}
	// History now ends with the resumed tool_result, not the dangling tool_use.
	if rn2.History()[len(rn2.History())-1].Role != message.RoleTool {
		t.Fatalf("resume history tail role = %v, want tool", rn2.History()[len(rn2.History())-1].Role)
	}
	out, err = rn2.Step()
	if err != nil {
		t.Fatalf("Step after resume: %v", err)
	}
	if out.Status != StepCompleted || out.FinalMessage.Text() != "done" {
		t.Fatalf("after resume: status=%v text=%q, want StepCompleted/done", out.Status, out.FinalMessage.Text())
	}
}

func TestNewResumeRunRejectsMismatchedAgent(t *testing.T) {
	r, _ := New(&scriptedModel{})
	suspended := SuspendedRun{
		Messages:      []message.Message{message.UserText("hi"), message.Assistant(message.NewToolUse("c1", "echo", json.RawMessage(`{}`)))},
		LastAgentName: "someone-else",
		LastMode:      "default",
		PendingCalls:  []PendingToolCall{{Call: message.ToolUse{ID: "c1", Name: "echo"}}},
	}
	_, err := r.NewResumeRun(context.Background(), testAgent(t), suspended, []Approval{{CallID: "c1", Approved: true}})
	if !errors.Is(err, ErrResumeAgentMismatch) {
		t.Fatalf("error = %v, want ErrResumeAgentMismatch", err)
	}
}

func TestResumePendingIfEnabled(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo text", func(ctx context.Context, in echoInput) (string, error) {
		return "echo: " + in.Text, nil
	})
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("done"))}}
	r, _ := New(m)
	history := []message.Message{
		message.UserText("hi"),
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"go"}`))),
	}
	rn, err := r.NewRun(context.Background(), testAgent(t, echo), Messages(history), WithResumeFromPendingTools())
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	pending, err := rn.ResumePendingIfEnabled()
	if err != nil {
		t.Fatalf("ResumePendingIfEnabled: %v", err)
	}
	if pending != nil {
		t.Fatalf("pending = %+v, want nil (AllowAll did not suspend)", pending)
	}
	// The pending tail batch ran: history now ends with a tool_result.
	if rn.History()[len(rn.History())-1].Role != message.RoleTool {
		t.Fatalf("history tail role = %v, want tool", rn.History()[len(rn.History())-1].Role)
	}
	out, err := rn.Step()
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if out.Status != StepCompleted || out.FinalMessage.Text() != "done" {
		t.Fatalf("status=%v text=%q, want StepCompleted/done", out.Status, out.FinalMessage.Text())
	}
}

func TestFireOnErrorFiresHook(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("x"))}}
	var got error
	r, _ := New(m, WithHooks(&hooks.Hooks{OnError: func(_ context.Context, err error) {
		got = err
	}}))
	r.FireOnError(context.Background(), ErrMaxTurns)
	if !errors.Is(got, ErrMaxTurns) {
		t.Fatalf("hook got = %v, want ErrMaxTurns", got)
	}
}
