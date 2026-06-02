package runner

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nethinwei/fino/message"
)

// These tests pin the tool-use ID invariants (v0.8.0 / P0-2): every tool_use the
// model emits must carry a non-empty ID that is unique within its batch. The
// Runner validates this before authorizing or executing any tool, because
// approval (Approval.CallID), idempotency (IdempotencyKey = RunID:ToolCallID),
// and the replay tape all key on the ID. Run and Stream share authorizeBatch, so
// both paths are covered; ResumeApproved validates the dangling batch in the
// snapshot it is handed.

func TestRunRejectsEmptyToolCallID(t *testing.T) {
	var ran int32
	tl := countingTool(t, "fetch", &ran)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("", "fetch", json.RawMessage(`{"text":"a"}`))),
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.Run(context.Background(), testAgent(t, tl), Text("hi")); !errors.Is(err, ErrInvalidToolCallID) {
		t.Fatalf("err = %v, want ErrInvalidToolCallID", err)
	}
	if ran != 0 {
		t.Fatalf("tool ran %d times, want 0 (rejected before execution)", ran)
	}
}

func TestRunRejectsDuplicateToolCallID(t *testing.T) {
	var ran int32
	tl := countingTool(t, "fetch", &ran)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(
			message.NewToolUse("dup", "fetch", json.RawMessage(`{"text":"a"}`)),
			message.NewToolUse("dup", "fetch", json.RawMessage(`{"text":"b"}`)),
		),
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.Run(context.Background(), testAgent(t, tl), Text("hi")); !errors.Is(err, ErrDuplicateToolCallID) {
		t.Fatalf("err = %v, want ErrDuplicateToolCallID", err)
	}
	if ran != 0 {
		t.Fatalf("tool ran %d times, want 0 (rejected before execution)", ran)
	}
}

func TestStreamRejectsDuplicateToolCallID(t *testing.T) {
	var ran int32
	tl := countingTool(t, "fetch", &ran)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(
			message.NewToolUse("dup", "fetch", json.RawMessage(`{"text":"a"}`)),
			message.NewToolUse("dup", "fetch", json.RawMessage(`{"text":"b"}`)),
		),
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var gotErr error
	for _, err := range r.Stream(context.Background(), testAgent(t, tl), Text("hi")) {
		if err != nil {
			gotErr = err
		}
	}
	if !errors.Is(gotErr, ErrDuplicateToolCallID) {
		t.Fatalf("stream err = %v, want ErrDuplicateToolCallID", gotErr)
	}
	if ran != 0 {
		t.Fatalf("tool ran %d times, want 0", ran)
	}
}

// ResumeApproved must not trust the snapshot's dangling batch blindly: a batch
// with a duplicate (or empty) tool_use ID makes approval matching and the
// idempotency key ambiguous, so it is rejected before any tool runs.
func TestResumeApprovedRejectsDuplicateDanglingToolCallID(t *testing.T) {
	a := testAgent(t, countingTool(t, "fetch", new(int32)))
	dangling := message.Assistant(
		message.NewToolUse("dup", "fetch", json.RawMessage(`{"text":"a"}`)),
		message.NewToolUse("dup", "fetch", json.RawMessage(`{"text":"b"}`)),
	)
	sr := SuspendedRun{
		Messages:      []message.Message{message.UserText("hi"), dangling},
		LastAgentName: "assistant",
		LastMode:      "default",
		PendingCalls: []PendingToolCall{{
			Call: message.ToolUse{ID: "dup", Name: "fetch", Input: json.RawMessage(`{"text":"a"}`)},
		}},
	}
	r, err := New(&scriptedModel{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.ResumeApproved(context.Background(), a, sr, []Approval{{CallID: "dup", Approved: true}}); !errors.Is(err, ErrDuplicateToolCallID) {
		t.Fatalf("err = %v, want ErrDuplicateToolCallID", err)
	}
}
