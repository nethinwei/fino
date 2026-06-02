package runner

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nethinwei/fino/message"
)

// These tests pin the approval-snapshot consistency check (v0.8.0 / P0-1a): the
// PendingToolCall the caller presents for human approval must match the dangling
// tool_use the resumed batch actually executes (same Name, byte-identical
// Input). resumeExecuteBatch reads the executed parameters from
// suspended.Messages, not from PendingCalls, so if the two diverge in a
// serialization round-trip or a UI rebuild, ResumeApproved rejects the snapshot
// rather than approving one payload and executing another. This is a byte-level
// check: PendingCalls is extracted from the same batch at suspend time, so any
// byte difference signals a tampered or wrongly-rebuilt snapshot.

func suspendedFetch(t *testing.T, pendingName, pendingInput string) SuspendedRun {
	t.Helper()
	dangling := message.Assistant(
		message.NewToolUse("call_1", "fetch", json.RawMessage(`{"text":"a"}`)),
	)
	return SuspendedRun{
		Messages:      []message.Message{message.UserText("hi"), dangling},
		LastAgentName: "assistant",
		LastMode:      "default",
		PendingCalls: []PendingToolCall{{
			Call: message.ToolUse{ID: "call_1", Name: pendingName, Input: json.RawMessage(pendingInput)},
		}},
	}
}

func TestResumeApprovedRejectsInputMismatch(t *testing.T) {
	var ran int32
	a := testAgent(t, countingTool(t, "fetch", &ran))
	// PendingCalls shows {"text":"b"} but the dangling batch carries {"text":"a"}.
	sr := suspendedFetch(t, "fetch", `{"text":"b"}`)
	r, err := New(&scriptedModel{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.ResumeApproved(context.Background(), a, sr, []Approval{{CallID: "call_1", Approved: true}}); !errors.Is(err, ErrInvalidApproval) {
		t.Fatalf("err = %v, want ErrInvalidApproval", err)
	}
	if ran != 0 {
		t.Fatalf("tool ran %d times, want 0 (rejected before execution)", ran)
	}
}

func TestResumeApprovedRejectsNameMismatch(t *testing.T) {
	var ran int32
	a := testAgent(t, countingTool(t, "fetch", &ran))
	// PendingCalls names "other" but the dangling batch calls "fetch".
	sr := suspendedFetch(t, "other", `{"text":"a"}`)
	r, err := New(&scriptedModel{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.ResumeApproved(context.Background(), a, sr, []Approval{{CallID: "call_1", Approved: true}}); !errors.Is(err, ErrInvalidApproval) {
		t.Fatalf("err = %v, want ErrInvalidApproval", err)
	}
	if ran != 0 {
		t.Fatalf("tool ran %d times, want 0 (rejected before execution)", ran)
	}
}

// Regression guard: a snapshot whose PendingCalls match the dangling batch still
// resumes and executes the approved call.
func TestResumeApprovedAcceptsMatchingSnapshot(t *testing.T) {
	var ran int32
	a := testAgent(t, countingTool(t, "fetch", &ran))
	sr := suspendedFetch(t, "fetch", `{"text":"a"}`)
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("done"))}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.ResumeApproved(context.Background(), a, sr, []Approval{{CallID: "call_1", Approved: true}})
	if err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	if ran != 1 {
		t.Fatalf("tool ran %d times, want 1", ran)
	}
	if res.Text() != "done" {
		t.Fatalf("final text = %q, want done", res.Text())
	}
}
