package runner

// Seam tests for invariant I10 (续跑完备) in docs/spec/loop-semantics.md §7.2.
//
// They pin both sides of the resume-from-pending seam:
//   - SAFE-BOUNDARY recovery (history ends in a user/tool message or an
//     assistant text message) needs no core change: re-running Run over the
//     persisted history continues correctly. This is what x/recover implements.
//   - MID-BATCH / HITL resume (history ends in a dangling assistant tool_use
//     with no results) is NOT auto-resumed by default: [T-MODEL] calls the model
//     first and never executes the pending tool_use. The minimal seam
//     WithResumeFromPendingTools opts into executing the pending batch before the
//     first model turn, without any checkpoint/session/graph concept.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
)

func TestResumeSeamProbe_DanglingToolUseRejectedWithoutSeam(t *testing.T) {
	history := []message.Message{
		message.UserText("hi"),
		message.Assistant(message.NewToolUse("c1", "alpha", json.RawMessage(`{}`))),
	}
	log := &eventLog{}
	a := buildPropAgent(t, log)
	m := &propModel{turns: []scriptTurn{{}}} // would be a final text turn, but never reached

	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}
	// Without the resume seam, a dangling tool_use tail is not a safe boundary
	// (I10): the core rejects it up front rather than forwarding an ill-formed
	// history to the model. The seam (WithResumeFromPendingTools) is the only
	// way to execute the pending batch.
	_, err = r.NewRun(context.Background(), a, Messages(history))
	if !errors.Is(err, ErrPendingToolUseInHistory) {
		t.Fatalf("error = %v, want ErrPendingToolUseInHistory", err)
	}
	if m.i != 0 {
		t.Fatal("model should not be called; the history is rejected before any model turn")
	}
	if containsEvent(log.snapshot(), "run:alpha") {
		t.Fatal("pending tool executed without a resume seam; core behavior changed unexpectedly")
	}
}

// TestResumeFromPendingTools_ExecutesDanglingBatch pins the seam's positive
// behavior: with WithResumeFromPendingTools, a run whose input history ends in a
// dangling assistant tool_use executes that pending tool first, then continues
// into the model turn, producing a well-formed history.
func TestResumeFromPendingTools_ExecutesDanglingBatch(t *testing.T) {
	history := []message.Message{
		message.UserText("hi"),
		message.Assistant(message.NewToolUse("c1", "alpha", json.RawMessage(`{}`))),
	}
	log := &eventLog{}
	a := buildPropAgent(t, log)
	m := &propModel{turns: []scriptTurn{{}}} // final text turn after the resumed tool

	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}
	rn, err := r.NewRun(context.Background(), a, Messages(history), WithResumeFromPendingTools())
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	pending, err := rn.ResumePendingIfEnabled()
	if err != nil {
		t.Fatalf("ResumePendingIfEnabled: %v", err)
	}
	if len(pending) > 0 {
		t.Fatalf("unexpected suspend: %+v", pending)
	}
	out, err := rn.Step()
	if err != nil {
		t.Fatalf("Step error: %v", err)
	}
	res := rn.Result(out.FinalMessage)
	if !containsEvent(log.snapshot(), "run:alpha") {
		t.Fatal("pending tool was not executed under WithResumeFromPendingTools")
	}
	if m.i == 0 {
		t.Fatal("model was not called after resuming the pending batch")
	}
	if res == nil || res.Text() != "final" {
		t.Fatalf("unexpected result: %+v", res)
	}
	// A single tool_result message for the resumed call must now exist, sitting
	// between the dangling assistant message and the final assistant message.
	toolMsgs := 0
	for _, msg := range res.Messages {
		if msg.Role == message.RoleTool {
			toolMsgs++
		}
	}
	if toolMsgs != 1 {
		t.Fatalf("RoleTool message count = %d, want 1", toolMsgs)
	}
}

// TestResumeFromPendingTools_MixedContentTail pins the broadened pending
// detection promised by PR2: the dangling assistant message may interleave
// thinking and text with the tool_use, not only carry a bare tool_use. The
// pending tool must still be detected and executed.
func TestResumeFromPendingTools_MixedContentTail(t *testing.T) {
	history := []message.Message{
		message.UserText("hi"),
		message.Assistant(
			message.NewThinking("let me call alpha"),
			message.NewText("calling alpha now"),
			message.NewToolUse("c1", "alpha", json.RawMessage(`{}`)),
		),
	}
	log := &eventLog{}
	a := buildPropAgent(t, log)
	m := &propModel{turns: []scriptTurn{{}}}

	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}
	rn, err := r.NewRun(context.Background(), a, Messages(history), WithResumeFromPendingTools())
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if _, err := rn.ResumePendingIfEnabled(); err != nil {
		t.Fatalf("ResumePendingIfEnabled: %v", err)
	}
	out, err := rn.Step()
	if err != nil {
		t.Fatalf("Step error: %v", err)
	}
	res := rn.Result(out.FinalMessage)
	if !containsEvent(log.snapshot(), "run:alpha") {
		t.Fatal("pending tool in a thinking+text+tool_use message was not executed")
	}
	if res == nil || res.Text() != "final" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// TestResumeFromPendingTools_StreamExecutesDanglingBatch is the Stream
// counterpart: the resumed pending tool surfaces ToolCall/ToolResult events
// before the terminal FinalMessage.
func TestResumeFromPendingTools_StreamExecutesDanglingBatch(t *testing.T) {
	history := []message.Message{
		message.UserText("hi"),
		message.Assistant(message.NewToolUse("c1", "alpha", json.RawMessage(`{}`))),
	}
	log := &eventLog{}
	a := buildPropAgent(t, log)
	m := &propModel{turns: []scriptTurn{{}}}

	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}
	rn, err := r.NewRun(context.Background(), a, Messages(history), WithResumeFromPendingTools())
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	var sawToolCall, sawToolResult, sawFinal bool
	collectEvents := func(ev model.Event, stepErr error) bool {
		if stepErr != nil {
			t.Fatalf("Stream error: %v", stepErr)
		}
		switch ev.(type) {
		case model.ToolCall:
			sawToolCall = true
		case model.ToolResult:
			sawToolResult = true
		case model.FinalMessage:
			sawFinal = true
		}
		return true
	}
	out, err := rn.StreamResumePendingIfEnabled(collectEvents)
	if err != nil {
		t.Fatalf("StreamResumePendingIfEnabled: %v", err)
	}
	if out.Status == StepContinue {
		if _, err := rn.StreamStep(collectEvents); err != nil {
			t.Fatalf("StreamStep: %v", err)
		}
	}
	if !sawToolCall || !sawToolResult {
		t.Fatalf("pending tool events missing: toolCall=%v toolResult=%v", sawToolCall, sawToolResult)
	}
	if !sawFinal {
		t.Fatal("no terminal FinalMessage after resuming the pending batch")
	}
	if !containsEvent(log.snapshot(), "run:alpha") {
		t.Fatal("pending tool was not executed in the streaming resume")
	}
}

// TestResumeFromPendingTools_NoOpWhenTailNotPending pins that the seam is inert
// when there is nothing pending: a history ending in a user message starts at
// the model exactly as without the seam.
func TestResumeFromPendingTools_NoOpWhenTailNotPending(t *testing.T) {
	log := &eventLog{}
	a := buildPropAgent(t, log)
	m := &propModel{turns: []scriptTurn{{}}}

	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}
	rn, err := r.NewRun(context.Background(), a, Text("hi"), WithResumeFromPendingTools())
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if _, err := rn.ResumePendingIfEnabled(); err != nil {
		t.Fatalf("ResumePendingIfEnabled: %v", err)
	}
	out, err := rn.Step()
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	res := rn.Result(out.FinalMessage)
	if len(log.snapshot()) != 0 {
		t.Fatalf("no tool should run when the tail is not pending; got %v", log.snapshot())
	}
	if m.i == 0 {
		t.Fatal("expected the model to be called")
	}
	if res == nil || res.Text() != "final" {
		t.Fatalf("unexpected result: %+v", res)
	}
}
