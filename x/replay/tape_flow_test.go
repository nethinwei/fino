package replay_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/react"
	"github.com/nethinwei/fino/x/replay"
)

// countingPolicy forwards to next while counting Authorize calls, so a test can
// prove a real policy is not consulted during replay.
type countingPolicy struct {
	next  policy.Policy
	calls *atomic.Int64
}

func (p countingPolicy) Authorize(ctx context.Context, req policy.Request) (policy.Decision, error) {
	p.calls.Add(1)
	return p.next.Authorize(ctx, req)
}

// fixedPolicy returns the same decision for every call.
type fixedPolicy struct {
	decision policy.Decision
	calls    *atomic.Int64
}

func (p fixedPolicy) Authorize(context.Context, policy.Request) (policy.Decision, error) {
	if p.calls != nil {
		p.calls.Add(1)
	}
	return p.decision, nil
}

func assertKinds(t *testing.T, events []replay.Event, want []replay.EventKind) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("event count = %d, want %d\nevents: %+v", len(events), len(want), events)
	}
	for i, k := range want {
		if events[i].Kind != k {
			t.Fatalf("event[%d].Kind = %q, want %q\nevents: %+v", i, events[i].Kind, k, events)
		}
	}
}

func searchTool(t *testing.T, calls *atomic.Int64) tool.Tool {
	t.Helper()
	s, err := tool.NewFunc("search", "search the web", func(_ context.Context, in searchInput) (string, error) {
		if calls != nil {
			calls.Add(1)
		}
		return "RESULT:" + in.Query, nil
	})
	if err != nil {
		t.Fatalf("NewFunc: %v", err)
	}
	return s
}

// TestRecordBoundaryEventsCopySlices proves boundary recorders snapshot
// caller-owned slices: mutating the caller's SuspendedRun.Messages,
// PendingCalls, or the approvals slice after recording does not rewrite the tape.
func TestRecordBoundaryEventsCopySlices(t *testing.T) {
	suspended := runner.SuspendedRun{
		LastAgentName: "searcher",
		LastMode:      "default",
		Messages:      []message.Message{message.Assistant(message.NewText("a"))},
		PendingCalls: []runner.PendingToolCall{{
			Call:   message.ToolUse{ID: "c1", Name: "search", Input: json.RawMessage(`{}`)},
			Reason: "needs approval",
		}},
	}
	approvals := []runner.Approval{{CallID: "c1", Approved: true}}

	log := &replay.Log{}
	replay.RecordSuspend(log, suspended)
	replay.RecordApproval(log, approvals)
	replay.RecordResume(log, suspended, approvals, &runner.Result{Message: message.Assistant(message.NewText("done"))}, nil)

	suspended.Messages[0] = message.Assistant(message.NewText("MUTATED"))
	suspended.PendingCalls[0].Reason = "MUTATED"
	approvals[0] = runner.Approval{CallID: "MUTATED", Approved: false}

	gotSuspend := log.Events[0].Suspend.Suspended
	if gotSuspend.Messages[0].Text() != "a" {
		t.Errorf("suspend messages mutated: %q", gotSuspend.Messages[0].Text())
	}
	if gotSuspend.PendingCalls[0].Reason != "needs approval" {
		t.Errorf("suspend pending calls mutated: %q", gotSuspend.PendingCalls[0].Reason)
	}
	if log.Events[1].Approval.Approvals[0].CallID != "c1" {
		t.Errorf("approval mutated: %q", log.Events[1].Approval.Approvals[0].CallID)
	}
	if log.Events[2].Resume.Approvals[0].CallID != "c1" {
		t.Errorf("resume approvals mutated: %q", log.Events[2].Resume.Approvals[0].CallID)
	}
}

// TestRecordingToolCapturesCallID proves RecordingTool populates ToolRecord.CallID
// from the run-scoped tool.ExecutionContext the Runner injects, and that the
// CallID survives a JSON round-trip.
func TestRecordingToolCapturesCallID(t *testing.T) {
	log := &replay.Log{}
	recModel := replay.RecordingModel{Next: &fakeProvider{resp: script()}, Log: log}
	recAgent := buildAgent(t, replay.RecordingTool(searchTool(t, nil), log))

	r, err := runner.New(recModel)
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	l, err := react.New(r)
	if err != nil {
		t.Fatalf("react.New: %v", err)
	}
	if _, err := l.Run(context.Background(), recAgent, runner.Text("find go"), runner.WithRunID("run_x")); err != nil {
		t.Fatalf("record Run: %v", err)
	}
	if len(log.Tools) != 1 {
		t.Fatalf("recorded tools = %d, want 1", len(log.Tools))
	}
	if log.Tools[0].CallID != "c1" {
		t.Fatalf("ToolRecord.CallID = %q, want c1", log.Tools[0].CallID)
	}

	data, err := log.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := replay.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Tools[0].CallID != "c1" {
		t.Fatalf("round-trip CallID = %q, want c1", got.Tools[0].CallID)
	}
}

// TestRecordingToolLegacyFixtureNoCallID proves a fixture written before the
// CallID field loads with CallID empty.
func TestRecordingToolLegacyFixtureNoCallID(t *testing.T) {
	legacy := []byte(`{"model":[],"tools":[{"name":"search","input":{"query":"go"},"result":{"Content":null,"IsError":false}}]}`)
	got, err := replay.Unmarshal(legacy)
	if err != nil {
		t.Fatalf("Unmarshal legacy: %v", err)
	}
	if len(got.Tools) != 1 || got.Tools[0].CallID != "" {
		t.Fatalf("legacy CallID = %q, want empty", got.Tools[0].CallID)
	}
}

// TestRecordCompletedRunEventOrder pins spec test #1: a completed run records
// events in order model_response, policy_decision, tool_execution,
// model_response, termination.
func TestRecordCompletedRunEventOrder(t *testing.T) {
	log := &replay.Log{}
	recModel := replay.RecordingModel{Next: &fakeProvider{resp: script()}, Log: log}
	recPolicy := replay.RecordingPolicy{Next: policy.AllowAll{}, Log: log}
	recAgent := buildAgent(t, replay.RecordingTool(searchTool(t, nil), log))

	r, err := runner.New(recModel, runner.WithPolicy(recPolicy))
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	l, err := react.New(r)
	if err != nil {
		t.Fatalf("react.New: %v", err)
	}
	res, err := l.Run(context.Background(), recAgent, runner.Text("find go"))
	if err != nil {
		t.Fatalf("record Run: %v", err)
	}
	replay.RecordTermination(log, res, err)

	assertKinds(t, log.Events, []replay.EventKind{
		replay.EventModelResponse,
		replay.EventPolicyDecision,
		replay.EventToolExecution,
		replay.EventModelResponse,
		replay.EventTermination,
	})
	term := log.Events[len(log.Events)-1].Termination
	if term.Status != replay.StatusCompleted || term.FinalText != "done" {
		t.Fatalf("termination = %+v, want completed/done", term)
	}
}

// TestReplayAvoidsRealModelToolPolicy pins spec test #2: replaying a recorded
// run with ReplayModel, ReplayTool, and ReplayPolicy calls no real component.
func TestReplayAvoidsRealModelToolPolicy(t *testing.T) {
	var toolCalls, policyCalls atomic.Int64
	fp := &fakeProvider{resp: script()}

	log := &replay.Log{}
	recModel := replay.RecordingModel{Next: fp, Log: log}
	recPolicy := replay.RecordingPolicy{Next: countingPolicy{next: policy.AllowAll{}, calls: &policyCalls}, Log: log}
	recAgent := buildAgent(t, replay.RecordingTool(searchTool(t, &toolCalls), log))

	r1, err := runner.New(recModel, runner.WithPolicy(recPolicy))
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	l1, err := react.New(r1)
	if err != nil {
		t.Fatalf("react.New: %v", err)
	}
	if _, err := l1.Run(context.Background(), recAgent, runner.Text("find go")); err != nil {
		t.Fatalf("record Run: %v", err)
	}
	recordedModelCalls, recordedToolCalls, recordedPolicyCalls := fp.i, toolCalls.Load(), policyCalls.Load()
	if recordedToolCalls != 1 || recordedPolicyCalls != 1 {
		t.Fatalf("record calls: tool=%d policy=%d, want 1/1", recordedToolCalls, recordedPolicyCalls)
	}

	repAgent := buildAgent(t, replay.ReplayTool("search", log))
	r2, err := runner.New(&replay.ReplayModel{Log: log}, runner.WithPolicy(&replay.ReplayPolicy{Log: log}))
	if err != nil {
		t.Fatalf("runner.New replay: %v", err)
	}
	l2, err := react.New(r2)
	if err != nil {
		t.Fatalf("react.New replay: %v", err)
	}
	rep, err := l2.Run(context.Background(), repAgent, runner.Text("find go"))
	if err != nil {
		t.Fatalf("replay Run: %v", err)
	}
	if rep.Text() != "done" {
		t.Fatalf("replay final text = %q, want done", rep.Text())
	}
	if fp.i != recordedModelCalls {
		t.Errorf("real model called during replay: %d -> %d", recordedModelCalls, fp.i)
	}
	if toolCalls.Load() != recordedToolCalls {
		t.Errorf("real tool called during replay: %d -> %d", recordedToolCalls, toolCalls.Load())
	}
	if policyCalls.Load() != recordedPolicyCalls {
		t.Errorf("real policy called during replay: %d -> %d", recordedPolicyCalls, policyCalls.Load())
	}
}

// TestReplayPolicyDeny pins spec test #3: a recorded deny replays through the
// runner as the same deny path without consulting the real policy.
func TestReplayPolicyDeny(t *testing.T) {
	var policyCalls atomic.Int64
	toolUse := []message.Message{message.Assistant(message.NewToolUse("c1", "search", json.RawMessage(`{"query":"go"}`)))}

	log := &replay.Log{}
	recModel := replay.RecordingModel{Next: &fakeProvider{resp: toolUse}, Log: log}
	deny := fixedPolicy{decision: policy.Decision{Kind: policy.DecisionDeny, Reason: "nope"}, calls: &policyCalls}
	recPolicy := replay.RecordingPolicy{Next: deny, Log: log}
	recAgent := buildAgent(t, replay.RecordingTool(searchTool(t, nil), log))

	r1, err := runner.New(recModel, runner.WithPolicy(recPolicy))
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	l1, err := react.New(r1)
	if err != nil {
		t.Fatalf("react.New: %v", err)
	}
	res, err := l1.Run(context.Background(), recAgent, runner.Text("find go"))
	if !errors.Is(err, runner.ErrToolDenied) {
		t.Fatalf("record run err = %v, want ErrToolDenied", err)
	}
	replay.RecordTermination(log, res, err)
	recordedPolicyCalls := policyCalls.Load()

	repAgent := buildAgent(t, replay.ReplayTool("search", log))
	r2, err := runner.New(&replay.ReplayModel{Log: log}, runner.WithPolicy(&replay.ReplayPolicy{Log: log}))
	if err != nil {
		t.Fatalf("runner.New replay: %v", err)
	}
	l2, err := react.New(r2)
	if err != nil {
		t.Fatalf("react.New replay: %v", err)
	}
	_, err = l2.Run(context.Background(), repAgent, runner.Text("find go"))
	if !errors.Is(err, runner.ErrToolDenied) {
		t.Fatalf("replay run err = %v, want ErrToolDenied", err)
	}
	if policyCalls.Load() != recordedPolicyCalls {
		t.Errorf("real policy consulted during replay: %d -> %d", recordedPolicyCalls, policyCalls.Load())
	}
}

// TestRecordSuspendTape pins spec test #4: a suspended run's tape includes
// policy_decision, suspend, and termination with status suspended.
func TestRecordSuspendTape(t *testing.T) {
	log := &replay.Log{}
	recModel := replay.RecordingModel{Next: &fakeProvider{resp: script()}, Log: log}
	suspend := fixedPolicy{decision: policy.Decision{Kind: policy.DecisionSuspend, Reason: "needs approval"}}
	recPolicy := replay.RecordingPolicy{Next: suspend, Log: log}
	recAgent := buildAgent(t, replay.RecordingTool(searchTool(t, nil), log))

	r, err := runner.New(recModel, runner.WithPolicy(recPolicy))
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	l, err := react.New(r)
	if err != nil {
		t.Fatalf("react.New: %v", err)
	}
	res, err := l.Run(context.Background(), recAgent, runner.Text("find go"))
	if err != nil {
		t.Fatalf("record Run: %v", err)
	}
	if !res.Suspended {
		t.Fatalf("run did not suspend")
	}
	suspended, err := res.SuspendedRun()
	if err != nil {
		t.Fatalf("SuspendedRun: %v", err)
	}
	replay.RecordSuspend(log, suspended)
	replay.RecordTermination(log, res, err)

	assertKinds(t, log.Events, []replay.EventKind{
		replay.EventModelResponse,
		replay.EventPolicyDecision,
		replay.EventSuspend,
		replay.EventTermination,
	})
	if got := log.Events[len(log.Events)-1].Termination.Status; got != replay.StatusSuspended {
		t.Fatalf("termination status = %q, want suspended", got)
	}
}

// TestReplayPolicyExhausted covers the failure path where the tape has no
// recorded policy decision: Authorize returns a replay: fixture-mismatch error,
// not a policy deny.
func TestReplayPolicyExhausted(t *testing.T) {
	p := &replay.ReplayPolicy{Log: &replay.Log{}}
	_, err := p.Authorize(context.Background(), policy.Request{
		AgentName: "searcher", ModeName: "default",
		Tool: tool.Info{Name: "search"}, Input: json.RawMessage(`{}`),
	})
	if err == nil || !strings.HasPrefix(err.Error(), "replay:") {
		t.Fatalf("Authorize err = %v, want replay: prefix", err)
	}
}

// TestReplayPolicyMalformedEvent covers a corrupt fixture: a policy_decision
// event with a nil payload errors rather than being skipped, so a later valid
// decision is not consumed in its place.
func TestReplayPolicyMalformedEvent(t *testing.T) {
	req := policy.Request{
		AgentName: "searcher", ModeName: "default",
		Tool: tool.Info{Name: "search"}, Input: json.RawMessage(`{}`),
	}
	log := &replay.Log{Events: []replay.Event{
		{Kind: replay.EventPolicyDecision, PolicyDecision: nil},
		{Kind: replay.EventPolicyDecision, PolicyDecision: &replay.PolicyDecisionEvent{
			Request: req, Decision: policy.Decision{Kind: policy.DecisionAllow},
		}},
	}}
	p := &replay.ReplayPolicy{Log: log}
	_, err := p.Authorize(context.Background(), req)
	if err == nil || !strings.HasPrefix(err.Error(), "replay:") {
		t.Fatalf("Authorize err = %v, want replay: malformed error", err)
	}
}

// TestReplayPolicyRequestMismatch covers the failure path where the next
// policy_decision does not match the current request's stable identity.
func TestReplayPolicyRequestMismatch(t *testing.T) {
	log := &replay.Log{Events: []replay.Event{
		{Kind: replay.EventPolicyDecision, PolicyDecision: &replay.PolicyDecisionEvent{
			Request: policy.Request{
				AgentName: "searcher", ModeName: "default",
				Tool: tool.Info{Name: "search"}, Input: json.RawMessage(`{"query":"go"}`),
			},
			Decision: policy.Decision{Kind: policy.DecisionAllow},
		}},
	}}
	p := &replay.ReplayPolicy{Log: log}
	_, err := p.Authorize(context.Background(), policy.Request{
		AgentName: "searcher", ModeName: "default",
		Tool: tool.Info{Name: "search"}, Input: json.RawMessage(`{"query":"rust"}`),
	})
	if err == nil || !strings.HasPrefix(err.Error(), "replay:") {
		t.Fatalf("Authorize err = %v, want replay: mismatch error", err)
	}
}

// TestReplayPolicyRecordedErr covers a recorded policy-system error: Authorize
// returns the recorded decision plus an error carrying the recorded message,
// distinct from a replay: fixture mismatch.
func TestReplayPolicyRecordedErr(t *testing.T) {
	req := policy.Request{
		AgentName: "searcher", ModeName: "default",
		Tool: tool.Info{Name: "search"}, Input: json.RawMessage(`{}`),
	}
	log := &replay.Log{Events: []replay.Event{
		{Kind: replay.EventPolicyDecision, PolicyDecision: &replay.PolicyDecisionEvent{
			Request:  req,
			Decision: policy.Decision{Kind: policy.DecisionAllow, Reason: "graceful"},
			Err:      "rbac timeout",
		}},
	}}
	p := &replay.ReplayPolicy{Log: log}
	decision, err := p.Authorize(context.Background(), req)
	if err == nil || err.Error() != "rbac timeout" {
		t.Fatalf("Authorize err = %v, want \"rbac timeout\"", err)
	}
	if decision.Kind != policy.DecisionAllow || decision.Reason != "graceful" {
		t.Fatalf("decision = %+v, want recorded allow/graceful", decision)
	}
}

// TestReplayPolicySkipsInterleavedEvents covers the normal-tape case: Authorize
// skips a leading model_response and returns the first policy_decision in order.
func TestReplayPolicySkipsInterleavedEvents(t *testing.T) {
	req := policy.Request{
		AgentName: "searcher", ModeName: "default",
		Tool: tool.Info{Name: "search"}, Input: json.RawMessage(`{"query":"go"}`),
	}
	log := &replay.Log{Events: []replay.Event{
		{Kind: replay.EventModelResponse, ModelResponse: &replay.ModelResponseEvent{
			Message: message.Assistant(message.NewText("thinking")),
		}},
		{Kind: replay.EventPolicyDecision, PolicyDecision: &replay.PolicyDecisionEvent{
			Request: req, Decision: policy.Decision{Kind: policy.DecisionAllow},
		}},
	}}
	p := &replay.ReplayPolicy{Log: log}
	decision, err := p.Authorize(context.Background(), req)
	if err != nil {
		t.Fatalf("Authorize err = %v, want nil", err)
	}
	if decision.Kind != policy.DecisionAllow {
		t.Fatalf("decision = %+v, want allow", decision)
	}
}

// TestRecordApprovalResumeTape pins spec test #5: an approval-then-resume flow
// records approval, resume, the approved tool execution, and final termination.
func TestRecordApprovalResumeTape(t *testing.T) {
	log := &replay.Log{}
	recModel := replay.RecordingModel{Next: &fakeProvider{resp: script()}, Log: log}
	suspend := fixedPolicy{decision: policy.Decision{Kind: policy.DecisionSuspend, Reason: "needs approval"}}
	recPolicy := replay.RecordingPolicy{Next: suspend, Log: log}
	var toolCalls atomic.Int64
	recAgent := buildAgent(t, replay.RecordingTool(searchTool(t, &toolCalls), log))

	r, err := runner.New(recModel, runner.WithPolicy(recPolicy))
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	l, err := react.New(r)
	if err != nil {
		t.Fatalf("react.New: %v", err)
	}
	res, err := l.Run(context.Background(), recAgent, runner.Text("find go"))
	if err != nil || !res.Suspended {
		t.Fatalf("record Run: err=%v suspended=%v", err, res != nil && res.Suspended)
	}
	suspended, err := res.SuspendedRun()
	if err != nil {
		t.Fatalf("SuspendedRun: %v", err)
	}
	replay.RecordSuspend(log, suspended)

	approvals := []runner.Approval{{CallID: "c1", Approved: true}}
	replay.RecordApproval(log, approvals)
	res2, err := l.ResumeApproved(context.Background(), recAgent, suspended, approvals)
	if err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	replay.RecordResume(log, suspended, approvals, res2, err)
	replay.RecordTermination(log, res2, err)

	if toolCalls.Load() != 1 {
		t.Fatalf("approved tool ran %d times, want 1", toolCalls.Load())
	}
	assertKinds(t, log.Events, []replay.EventKind{
		replay.EventModelResponse,
		replay.EventPolicyDecision,
		replay.EventSuspend,
		replay.EventApproval,
		replay.EventToolExecution,
		replay.EventModelResponse,
		replay.EventResume,
		replay.EventTermination,
	})
	if got := log.Events[6].Resume.Status; got != replay.StatusCompleted {
		t.Fatalf("resume status = %q, want completed", got)
	}
	if got := log.Events[7].Termination; got.Status != replay.StatusCompleted || got.FinalText != "done" {
		t.Fatalf("termination = %+v, want completed/done", got)
	}
}
