package runner

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/tool"
)

// TestStreamSerialBatchAuthorizedBeforeExecute pins that the serial stream path
// authorizes the whole batch before running any tool: when a later call in the
// batch is denied, no earlier tool executes and no ToolCall event is emitted.
func TestStreamSerialBatchAuthorizedBeforeExecute(t *testing.T) {
	var t0ran int32
	t0 := countingTool(t, "t0", &t0ran)
	t1 := echoTool(t, "t1")
	m := &streamOnlyModel{turns: [][]model.Event{
		{model.TurnMessage{Message: message.Assistant(toolUse("c0", "t0"), toolUse("c1", "t1"))}},
	}}
	// Default maxConcurrency keeps the batch serial; deny only the second call.
	r, err := New(m, WithPolicy(&kindPolicy{deny: map[string]bool{"t1": true}}))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var gotErr error
	var sawToolCall bool
	for ev, err := range r.Stream(context.Background(), testAgent(t, t0, t1), Text("hi")) {
		if err != nil {
			gotErr = err
			break
		}
		if _, ok := ev.(model.ToolCall); ok {
			sawToolCall = true
		}
	}
	var tde *ToolDeniedError
	if !errors.As(gotErr, &tde) {
		t.Fatalf("error = %v, want ToolDeniedError", gotErr)
	}
	if sawToolCall {
		t.Error("ToolCall emitted before a batch-level deny; serial Stream must authorize the whole batch first")
	}
	if n := atomic.LoadInt32(&t0ran); n != 0 {
		t.Errorf("t0 executed %d times; a denied batch must execute no tool", n)
	}
}

func echoTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, err := tool.NewFunc(name, "echo", func(ctx context.Context, in echoInput) (string, error) {
		return name, nil
	}, tool.WithEffects(parallelSafeEffects))
	if err != nil {
		t.Fatalf("NewFunc error: %v", err)
	}
	return tl
}

// TestStreamParallelPolicyDenial covers the authorizeBatch error path before
// Stream selects either the serial or parallel execution branch.
func TestStreamParallelPolicyDenial(t *testing.T) {
	m := &streamOnlyModel{turns: [][]model.Event{
		{model.TurnMessage{Message: message.Assistant(toolUse("c0", "t0"), toolUse("c1", "t1"))}},
	}}
	r, err := New(m, WithMaxConcurrency(2), WithPolicy(denyPolicy{}))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var gotErr error
	for _, err := range r.Stream(context.Background(), testAgent(t, echoTool(t, "t0"), echoTool(t, "t1")), Text("hi")) {
		if err != nil {
			gotErr = err
			break
		}
	}
	var tde *ToolDeniedError
	if !errors.As(gotErr, &tde) {
		t.Fatalf("error = %v, want ToolDeniedError", gotErr)
	}
}

// TestStreamParallelToolError covers the per-result error branch in
// drainToolResults and the error reporting in finishStreamToolCallsParallel.
func TestStreamParallelToolError(t *testing.T) {
	boom := errors.New("boom")
	failing, _ := tool.NewFunc("t0", "fail", func(ctx context.Context, in echoInput) (string, error) {
		return "", boom
	}, tool.WithEffects(parallelSafeEffects))
	m := &streamOnlyModel{turns: [][]model.Event{
		{model.TurnMessage{Message: message.Assistant(toolUse("c0", "t0"), toolUse("c1", "t1"))}},
	}}
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var gotErr error
	for _, err := range r.Stream(context.Background(), testAgent(t, failing, echoTool(t, "t1")), Text("hi")) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if !errors.Is(gotErr, boom) {
		t.Fatalf("error = %v, want boom", gotErr)
	}
}

// TestStreamParallelHandoff covers the handoff branch in finalizeStreamBatch
// (handoff requested within a parallel tool batch while streaming).
func TestStreamParallelHandoff(t *testing.T) {
	targetMode, _ := agent.NewMode("default", "target")
	target, _ := agent.New("target", agent.WithMode(targetMode), agent.WithDefaultMode("default"))
	handoff, _ := agent.NewHandoffTool(target)
	handoff = parallelSafeHandoff(t, handoff)
	srcMode, _ := agent.NewMode("default", "source", agent.WithTools(echoTool(t, "echo"), handoff))
	source, _ := agent.New("source", agent.WithMode(srcMode), agent.WithDefaultMode("default"))

	m := &streamOnlyModel{turns: [][]model.Event{
		{model.TurnMessage{Message: message.Assistant(
			message.NewToolUse("c0", "echo", json.RawMessage(`{"text":"x"}`)),
			message.NewToolUse("c1", "handoff_to_target", json.RawMessage(`{}`)),
		)}},
		{model.TurnMessage{Message: message.Assistant(message.NewText("from target"))}},
	}}
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var handoffs int
	for ev, err := range r.Stream(context.Background(), source, Text("hi")) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		if _, ok := ev.(model.Handoff); ok {
			handoffs++
		}
	}
	if handoffs != 1 {
		t.Fatalf("handoffs = %d, want 1", handoffs)
	}
}

// TestStreamSerialToolError covers the execute-error branch in the serial stream
// execution path (single tool call uses the serial path).
func TestStreamSerialToolError(t *testing.T) {
	boom := errors.New("boom")
	failing, _ := tool.NewFunc("echo", "fail", func(ctx context.Context, in echoInput) (string, error) {
		return "", boom
	})
	m := &streamOnlyModel{turns: [][]model.Event{
		{model.TurnMessage{Message: message.Assistant(message.NewToolUse("c0", "echo", json.RawMessage(`{"text":"x"}`)))}},
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var gotErr error
	for _, err := range r.Stream(context.Background(), testAgent(t, failing), Text("hi")) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if !errors.Is(gotErr, boom) {
		t.Fatalf("error = %v, want boom", gotErr)
	}
}

// TestStreamParallelConsumerStops covers the consumer-stop branch in the
// parallel streaming path: breaking mid-stream must end iteration cleanly.
func TestStreamParallelConsumerStops(t *testing.T) {
	m := &streamOnlyModel{turns: [][]model.Event{
		{model.TurnMessage{Message: message.Assistant(toolUse("c0", "t0"), toolUse("c1", "t1"))}},
		{model.TurnMessage{Message: message.Assistant(message.NewText("done"))}},
	}}
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	count := 0
	for _, err := range r.Stream(context.Background(), testAgent(t, echoTool(t, "t0"), echoTool(t, "t1")), Text("hi")) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		count++
		break // stop after the first event
	}
	if count != 1 {
		t.Fatalf("consumed %d events, want 1", count)
	}
}
