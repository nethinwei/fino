package runner

// Seam probe for invariant I10 (续跑完备) in docs/spec/loop-semantics.md §7.1.
//
// This test does not assert a feature; it characterizes whether durable
// recovery can be built entirely outside the core, and records the finding that
// drives the seam decision in the implementation plan.
//
// Finding:
//   - SAFE-BOUNDARY recovery (history ends in a user/tool message or an
//     assistant text message) needs no core change: re-running Run over the
//     persisted history continues correctly. This is what x/recover implements.
//   - MID-BATCH / HITL resume (history ends in a dangling assistant tool_use
//     with no results) is NOT directly resumable: [T-MODEL] always calls the
//     model first and never executes the pending tool_use. Supporting it would
//     require a MINIMAL seam (e.g. a WithResumeFromPendingTools RunOption),
//     which is deferred to explicit sign-off and is intentionally NOT added here.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nethinwei/fino/message"
)

func TestResumeSeamProbe_DanglingToolUseNotAutoExecuted(t *testing.T) {
	history := []message.Message{
		message.UserText("hi"),
		message.Assistant(message.NewToolUse("c1", "alpha", json.RawMessage(`{}`))),
	}
	log := &eventLog{}
	a := buildPropAgent(t, log)
	m := &propModel{turns: []scriptTurn{{}}} // next response is a final text turn

	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}
	res, err := r.Run(context.Background(), a, Messages(history))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// The pending tool_use is NOT auto-executed: the Runner called the model
	// instead. This is the seam gap documented in §7.1.
	if m.i == 0 {
		t.Fatal("expected the model to be called; pending tools are not auto-executed (seam gap absent?)")
	}
	if containsEvent(log.snapshot(), "run:alpha") {
		t.Fatal("pending tool executed without a resume seam; core behavior changed unexpectedly")
	}
	if res == nil {
		t.Fatal("expected a result from re-running over the history")
	}
}
