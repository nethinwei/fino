package runner

// Behavior tests for PR3 approved resume. They pin the design in
// docs/superpowers/specs/2026-06-02-approved-resume-design.md: Result.SuspendedRun()
// extracts a value snapshot (including LastAgentName) from a suspended Result;
// Runner.ResumeApproved validates the passed agent against LastAgentName, validates
// approvals, executes approved (and previously-allowed) calls, writes rejections
// as model-visible tool_result blocks, and continues the ReAct loop. Validation
// failures return ApprovalError (wrapping ErrInvalidApproval, distinct from
// ErrNotSuspended) without OnError; execution errors fire OnError.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
)

// suspendOnRun runs r and asserts the run suspended, returning the result.
func suspendOnRun(t *testing.T, r *Runner, a *agent.Agent) *Result {
	t.Helper()
	res, err := r.Run(context.Background(), a, Text("hi"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Suspended {
		t.Fatalf("Run did not suspend; res = %+v", res)
	}
	return res
}

// Test 1: Result.SuspendedRun() returns a snapshot carrying the agent/mode
// context (LastAgentName) and the suspended pending calls.
func TestSuspendedRunExtraction(t *testing.T) {
	var ran int32
	fetch := countingTool(t, "fetch", &ran)
	m := &scriptedModel{responses: []message.Message{message.Assistant(toolUse("call_1", "fetch"))}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "approve"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := testAgent(t, fetch)
	res := suspendOnRun(t, r, a)
	sr, err := res.SuspendedRun()
	if err != nil {
		t.Fatalf("SuspendedRun: %v", err)
	}
	if sr.LastAgentName != a.Name() {
		t.Fatalf("LastAgentName = %q, want %q", sr.LastAgentName, a.Name())
	}
	if sr.LastMode != "default" {
		t.Fatalf("LastMode = %q, want default", sr.LastMode)
	}
	if len(sr.PendingCalls) != 1 || sr.PendingCalls[0].Call.ID != "call_1" {
		t.Fatalf("PendingCalls = %+v, want one call_1", sr.PendingCalls)
	}
	if len(sr.Messages) != len(res.Messages) {
		t.Fatalf("Messages len = %d, want %d", len(sr.Messages), len(res.Messages))
	}
}

// Test 2: Result.SuspendedRun() on a completed (non-suspended) result returns
// ErrNotSuspended, which must be distinct from ErrInvalidApproval.
func TestSuspendedRunOnCompletedResultErrors(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("done"))}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Run(context.Background(), testAgent(t), Text("hi"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, err = res.SuspendedRun()
	if !errors.Is(err, ErrNotSuspended) {
		t.Fatalf("err = %v, want ErrNotSuspended", err)
	}
	if errors.Is(err, ErrInvalidApproval) {
		t.Fatal("ErrNotSuspended must not satisfy errors.Is(ErrInvalidApproval)")
	}
}

// Test 3: approving a suspended call executes the tool, appends its result, and
// continues the loop to the next model turn.
func TestResumeApprovedExecutesTool(t *testing.T) {
	var ran int32
	fetch := countingTool(t, "fetch", &ran)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "fetch")),
		message.Assistant(message.NewText("final")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "approve"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := testAgent(t, fetch)
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	res2, err := r.ResumeApproved(context.Background(), a, sr, []Approval{{CallID: "call_1", Approved: true}})
	if err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	if res2.Suspended {
		t.Fatal("res2 suspended, want completed")
	}
	if res2.Text() != "final" {
		t.Fatalf("text = %q, want final", res2.Text())
	}
	if ran != 1 {
		t.Fatalf("fetch ran %d, want 1", ran)
	}
}

// Test 4: rejecting a suspended call writes a model-visible tool_result with
// IsError and the rejection reason, and does not execute the tool.
func TestResumeApprovedRejectWritesError(t *testing.T) {
	var ran int32
	del := countingTool(t, "delete_file", &ran)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "delete_file")),
		message.Assistant(message.NewText("ok, skipped")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"delete_file": "confirm"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := testAgent(t, del)
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	res2, err := r.ResumeApproved(context.Background(), a, sr,
		[]Approval{{CallID: "call_1", Approved: false, Reason: "user denied"}})
	if err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	if ran != 0 {
		t.Fatalf("delete_file ran %d, want 0 (rejected)", ran)
	}
	var found bool
	for _, msg := range res2.Messages {
		if msg.Role != message.RoleTool {
			continue
		}
		b := msg.Content[0]
		if b.ToolUseID == "call_1" && b.IsError && strings.Contains(b.Content[0].Text, "rejected: user denied") {
			found = true
		}
	}
	if !found {
		t.Fatal("did not find rejected tool_result with IsError and reason")
	}
}

// Test 5: a mixed batch (call_1 Allow, call_2 Suspend) resumes by executing the
// allowed call without an approval and the suspended call with its approval.
func TestResumeApprovedMixedAllowSuspend(t *testing.T) {
	var aRan, fRan int32
	alpha := countingTool(t, "alpha", &aRan)
	fetch := countingTool(t, "fetch", &fRan)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "alpha"), toolUse("call_2", "fetch")),
		message.Assistant(message.NewText("done")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "approve"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := testAgent(t, alpha, fetch)
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	if len(sr.PendingCalls) != 1 || sr.PendingCalls[0].Call.ID != "call_2" {
		t.Fatalf("PendingCalls = %+v, want only call_2", sr.PendingCalls)
	}
	if _, err := r.ResumeApproved(context.Background(), a, sr,
		[]Approval{{CallID: "call_2", Approved: true}}); err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	if aRan != 1 || fRan != 1 {
		t.Fatalf("ran (alpha=%d fetch=%d), want both 1", aRan, fRan)
	}
}

// Test 6: a rejection is visible to the model on the next turn.
func TestResumeApprovedRejectionModelVisible(t *testing.T) {
	del := countingTool(t, "delete_file", new(int32))
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "delete_file")),
		message.Assistant(message.NewText("acknowledged")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"delete_file": "confirm"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := testAgent(t, del)
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	if _, err := r.ResumeApproved(context.Background(), a, sr,
		[]Approval{{CallID: "call_1", Approved: false, Reason: "denied"}}); err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	if len(m.calls) < 2 {
		t.Fatalf("model called %d times, want >= 2", len(m.calls))
	}
	var sawRejection bool
	for _, msg := range m.calls[len(m.calls)-1] {
		if msg.Role != message.RoleTool {
			continue
		}
		for _, b := range msg.Content {
			if b.IsError && strings.Contains(b.Content[0].Text, "rejected") {
				sawRejection = true
			}
		}
	}
	if !sawRejection {
		t.Fatal("model's next turn did not observe the rejection tool_result")
	}
}

// Test 7: ResumeApproved does not re-consult the Policy for the suspended calls.
func TestResumeApprovedDoesNotReauthorize(t *testing.T) {
	var ran int32
	fetch := countingTool(t, "fetch", &ran)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "fetch")),
		message.Assistant(message.NewText("final")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "approve"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := testAgent(t, fetch)
	res := suspendOnRun(t, r, a)
	authedAfterRun := len(pol.authed)
	sr, _ := res.SuspendedRun()
	if _, err := r.ResumeApproved(context.Background(), a, sr,
		[]Approval{{CallID: "call_1", Approved: true}}); err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	if len(pol.authed) != authedAfterRun {
		t.Fatalf("policy authorized %d extra times during resume, want 0", len(pol.authed)-authedAfterRun)
	}
	if ran != 1 {
		t.Fatalf("fetch ran %d, want 1", ran)
	}
}

// twoPendingSetup builds a runner whose single batch suspends two tools.
func twoPendingSetup(t *testing.T) (*Runner, *agent.Agent, *int32, *int32) {
	t.Helper()
	r1 := new(int32)
	r2 := new(int32)
	f1 := countingTool(t, "fetch1", r1)
	f2 := countingTool(t, "fetch2", r2)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "fetch1"), toolUse("call_2", "fetch2")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"fetch1": "a", "fetch2": "b"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, testAgent(t, f1, f2), r1, r2
}

// Test 8: a missing approval for a pending call returns ApprovalError (wrapping
// ErrInvalidApproval, distinct from ErrNotSuspended) and executes nothing.
func TestResumeApprovedMissingApproval(t *testing.T) {
	r, a, r1, r2 := twoPendingSetup(t)
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	_, err := r.ResumeApproved(context.Background(), a, sr,
		[]Approval{{CallID: "call_1", Approved: true}})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *ApprovalError", err)
	}
	if len(ae.Missing) != 1 || ae.Missing[0] != "call_2" {
		t.Fatalf("Missing = %v, want [call_2]", ae.Missing)
	}
	if !errors.Is(err, ErrInvalidApproval) {
		t.Fatal("errors.Is(err, ErrInvalidApproval) = false, want true")
	}
	if errors.Is(err, ErrNotSuspended) {
		t.Fatal("errors.Is(err, ErrNotSuspended) = true, want false (distinct sentinels)")
	}
	if *r1 != 0 || *r2 != 0 {
		t.Fatalf("tools ran on validation failure (%d,%d), want 0", *r1, *r2)
	}
}

// Test 9: an approval referencing a CallID not in the pending set returns
// ApprovalError.Unknown.
func TestResumeApprovedUnknownCallID(t *testing.T) {
	r, a, _, _ := twoPendingSetup(t)
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	_, err := r.ResumeApproved(context.Background(), a, sr, []Approval{
		{CallID: "call_1", Approved: true},
		{CallID: "call_2", Approved: true},
		{CallID: "call_99", Approved: true},
	})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *ApprovalError", err)
	}
	if len(ae.Unknown) != 1 || ae.Unknown[0] != "call_99" {
		t.Fatalf("Unknown = %v, want [call_99]", ae.Unknown)
	}
}

// Test 10: two approvals for the same CallID return ApprovalError.Duplicate.
func TestResumeApprovedDuplicateApproval(t *testing.T) {
	r, a, _, _ := twoPendingSetup(t)
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	_, err := r.ResumeApproved(context.Background(), a, sr, []Approval{
		{CallID: "call_1", Approved: true},
		{CallID: "call_2", Approved: true},
		{CallID: "call_1", Approved: true},
	})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *ApprovalError", err)
	}
	if len(ae.Duplicate) != 1 || ae.Duplicate[0] != "call_1" {
		t.Fatalf("Duplicate = %v, want [call_1]", ae.Duplicate)
	}
}

// handoffThenSuspend builds A --handoff--> B where B's fetch is suspended, plus
// an optional final response. It runs to the suspended state and returns the
// runner, root agent A, target agent B, the suspended result, and fetch counter.
func handoffThenSuspend(t *testing.T, finalText string) (*Runner, *agent.Agent, *agent.Agent, *Result, *int32) {
	t.Helper()
	ran := new(int32)
	fetch := countingTool(t, "fetch", ran)
	bMode, err := agent.NewMode("default", "B", agent.WithTools(fetch))
	if err != nil {
		t.Fatalf("NewMode B: %v", err)
	}
	b, err := agent.New("B", agent.WithMode(bMode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New B: %v", err)
	}
	handoff, err := agent.NewHandoffTool(b)
	if err != nil {
		t.Fatalf("NewHandoffTool: %v", err)
	}
	aMode, err := agent.NewMode("default", "A", agent.WithTools(handoff))
	if err != nil {
		t.Fatalf("NewMode A: %v", err)
	}
	a, err := agent.New("A", agent.WithMode(aMode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New A: %v", err)
	}
	responses := []message.Message{
		message.Assistant(toolUse("h1", "handoff_to_B")),
		message.Assistant(toolUse("call_1", "fetch")),
	}
	if finalText != "" {
		responses = append(responses, message.Assistant(message.NewText(finalText)))
	}
	m := &scriptedModel{responses: responses}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "approve"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res := suspendOnRun(t, r, a)
	if res.LastAgent != b {
		t.Fatalf("LastAgent = %v, want B (handoff target)", res.LastAgent)
	}
	return r, a, b, res, ran
}

// Test 11: passing the wrong agent (the root, not the handoff target active at
// suspend) is rejected before any tool runs.
func TestResumeApprovedAgentMismatch(t *testing.T) {
	r, a, _, res, ran := handoffThenSuspend(t, "")
	sr, _ := res.SuspendedRun()
	_, err := r.ResumeApproved(context.Background(), a, sr,
		[]Approval{{CallID: "call_1", Approved: true}})
	if err == nil {
		t.Fatal("ResumeApproved with wrong agent returned nil, want error")
	}
	if *ran != 0 {
		t.Fatalf("fetch ran %d on agent mismatch, want 0", *ran)
	}
}

// Test 12: resuming with the correct handoff-target agent executes the suspended
// call under that agent and continues the loop.
func TestResumeApprovedHandoffThenSuspendThenResume(t *testing.T) {
	r, _, b, res, ran := handoffThenSuspend(t, "final")
	sr, _ := res.SuspendedRun()
	res2, err := r.ResumeApproved(context.Background(), b, sr,
		[]Approval{{CallID: "call_1", Approved: true}})
	if err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	if *ran != 1 {
		t.Fatalf("fetch ran %d, want 1", *ran)
	}
	if res2.Text() != "final" {
		t.Fatalf("text = %q, want final", res2.Text())
	}
	if res2.LastAgent != b {
		t.Fatalf("LastAgent = %v, want B", res2.LastAgent)
	}
}

// Test 13: an approved handoff tool inside a resumed batch applies batch-terminal,
// switching the active agent for the continuation.
func TestResumeApprovedHandoffInBatch(t *testing.T) {
	bMode, err := agent.NewMode("default", "B")
	if err != nil {
		t.Fatalf("NewMode B: %v", err)
	}
	b, err := agent.New("B", agent.WithMode(bMode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New B: %v", err)
	}
	handoff, err := agent.NewHandoffTool(b)
	if err != nil {
		t.Fatalf("NewHandoffTool: %v", err)
	}
	aMode, err := agent.NewMode("default", "A", agent.WithTools(handoff))
	if err != nil {
		t.Fatalf("NewMode A: %v", err)
	}
	a, err := agent.New("A", agent.WithMode(aMode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New A: %v", err)
	}
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("h1", "handoff_to_B")),
		message.Assistant(message.NewText("final")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"handoff_to_B": "approve"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	res2, err := r.ResumeApproved(context.Background(), a, sr,
		[]Approval{{CallID: "h1", Approved: true}})
	if err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	if res2.LastAgent != b {
		t.Fatalf("LastAgent = %v, want B after approved handoff", res2.LastAgent)
	}
}

// Test 14: pre-loop validation failures do not fire OnError.
func TestResumeApprovedValidationDoesNotFireOnError(t *testing.T) {
	r, a, _, _ := twoPendingSetup(t)
	ch := &countingHooks{}
	r.hooks = ch.hooks()
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	_, err := r.ResumeApproved(context.Background(), a, sr,
		[]Approval{{CallID: "call_1", Approved: true}})
	if err == nil {
		t.Fatal("want ApprovalError, got nil")
	}
	if n := ch.onError.Load(); n != 0 {
		t.Fatalf("OnError fired %d times on validation failure, want 0", n)
	}
}

// Test 15: a tool execution error during resume fires OnError exactly once and
// returns the error.
func TestResumeApprovedExecutionErrorFiresOnError(t *testing.T) {
	boom := failingTool{name: "boom", err: errors.New("boom")}
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "boom")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"boom": "approve"}}
	ch := &countingHooks{}
	r, err := New(m, WithPolicy(pol), WithHooks(ch.hooks()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := testAgent(t, boom)
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	_, err = r.ResumeApproved(context.Background(), a, sr,
		[]Approval{{CallID: "call_1", Approved: true}})
	if err == nil {
		t.Fatal("want execution error, got nil")
	}
	if n := ch.onError.Load(); n != 1 {
		t.Fatalf("OnError fired %d times, want exactly 1", n)
	}
}

// Test 16: maxConcurrency does not change ResumeApproved; the resumed batch runs
// serially and the result is identical.
func TestResumeApprovedParallelNotAffected(t *testing.T) {
	var aRan, fRan int32
	alpha := countingTool(t, "alpha", &aRan)
	fetch := countingTool(t, "fetch", &fRan)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "alpha"), toolUse("call_2", "fetch")),
		message.Assistant(message.NewText("done")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "approve"}}
	r, err := New(m, WithPolicy(pol), WithMaxConcurrency(8))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := testAgent(t, alpha, fetch)
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	res2, err := r.ResumeApproved(context.Background(), a, sr,
		[]Approval{{CallID: "call_2", Approved: true}})
	if err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	if aRan != 1 || fRan != 1 {
		t.Fatalf("ran (alpha=%d fetch=%d), want both 1", aRan, fRan)
	}
	if res2.Text() != "done" {
		t.Fatalf("text = %q, want done", res2.Text())
	}
}

// Test 18: a snapshot with dangling tool_uses but no pending calls is rejected
// (it would otherwise degrade into a blind, approval-free resume).
func TestResumeApprovedRejectsEmptyPending(t *testing.T) {
	var ran int32
	fetch := countingTool(t, "fetch", &ran)
	a := testAgent(t, fetch)
	sr := SuspendedRun{
		Messages: []message.Message{
			message.UserText("hi"),
			message.Assistant(toolUse("call_1", "fetch")),
		},
		LastAgentName: a.Name(),
		LastMode:      "default",
		PendingCalls:  nil,
	}
	r, err := New(&scriptedModel{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = r.ResumeApproved(context.Background(), a, sr, nil)
	if !errors.Is(err, ErrInvalidApproval) {
		t.Fatalf("err = %v, want ErrInvalidApproval", err)
	}
	if ran != 0 {
		t.Fatalf("fetch ran %d on rejected resume, want 0", ran)
	}
}

// Test 19: a snapshot whose history contains a system message is rejected
// (preserving I8 no-system-leak, like Run).
func TestResumeApprovedRejectsSystemMessage(t *testing.T) {
	var ran int32
	fetch := countingTool(t, "fetch", &ran)
	a := testAgent(t, fetch)
	sr := SuspendedRun{
		Messages: []message.Message{
			message.SystemText("leaked"),
			message.UserText("hi"),
			message.Assistant(toolUse("call_1", "fetch")),
		},
		LastAgentName: a.Name(),
		LastMode:      "default",
		PendingCalls:  []PendingToolCall{{Call: message.ToolUse{ID: "call_1", Name: "fetch"}}},
	}
	r, err := New(&scriptedModel{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = r.ResumeApproved(context.Background(), a, sr,
		[]Approval{{CallID: "call_1", Approved: true}})
	if !errors.Is(err, ErrSystemMessageInHistory) {
		t.Fatalf("err = %v, want ErrSystemMessageInHistory", err)
	}
	if ran != 0 {
		t.Fatalf("fetch ran %d on rejected resume, want 0", ran)
	}
}

// Test 20: overriding the mode on resume is rejected; the batch tool_uses were
// resolved against the suspended mode.
func TestResumeApprovedRejectsModeOverride(t *testing.T) {
	var ran int32
	fetch := countingTool(t, "fetch", &ran)
	m := &scriptedModel{responses: []message.Message{message.Assistant(toolUse("call_1", "fetch"))}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "approve"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := testAgent(t, fetch)
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	_, err = r.ResumeApproved(context.Background(), a, sr,
		[]Approval{{CallID: "call_1", Approved: true}}, WithMode("other"))
	if !errors.Is(err, ErrInvalidApproval) {
		t.Fatalf("err = %v, want ErrInvalidApproval on mode override", err)
	}
	if ran != 0 {
		t.Fatalf("fetch ran %d on rejected resume, want 0", ran)
	}
}

// Test 17 (I12): after ResumeApproved, every tool_use in the suspended batch has
// a corresponding tool_result in the single appended RoleTool message.
func TestResumeApprovedI12EveryToolUseHasResult(t *testing.T) {
	var r1, r2 int32
	keep := countingTool(t, "keep", &r1)
	drop := countingTool(t, "drop", &r2)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "keep"), toolUse("call_2", "drop")),
		message.Assistant(message.NewText("done")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"keep": "a", "drop": "b"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := testAgent(t, keep, drop)
	res := suspendOnRun(t, r, a)
	sr, _ := res.SuspendedRun()
	res2, err := r.ResumeApproved(context.Background(), a, sr, []Approval{
		{CallID: "call_1", Approved: true},
		{CallID: "call_2", Approved: false, Reason: "no"},
	})
	if err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	resultIDs := map[string]bool{}
	for _, msg := range res2.Messages {
		if msg.Role != message.RoleTool {
			continue
		}
		for _, b := range msg.Content {
			resultIDs[b.ToolUseID] = true
		}
	}
	for _, id := range []string{"call_1", "call_2"} {
		if !resultIDs[id] {
			t.Fatalf("tool_use %q has no tool_result after resume (I12)", id)
		}
	}
}
