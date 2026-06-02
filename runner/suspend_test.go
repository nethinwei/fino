package runner

// Behavior tests for PR2 three-state policy (allow/deny/suspend). They pin the
// design in docs/superpowers/specs/2026-06-02-three-state-policy-design.md:
// suspend halts Run with a Suspended Result carrying only the suspended pending
// calls; no tool in the batch executes; OnError does not fire; deny takes
// precedence over suspend and fails fast; the serial refactor authorizes the
// whole batch before any execution; Stream downgrades suspend to a deny error.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/tool"
)

// kindPolicy returns three-state decisions per tool name and records the order
// in which tools were authorized so tests can assert fail-fast (later calls are
// never authorized).
type kindPolicy struct {
	suspend map[string]string // tool name -> suspend reason
	deny    map[string]bool   // tool name -> deny
	authed  []string
}

func (p *kindPolicy) Authorize(_ context.Context, req policy.Request) (policy.Decision, error) {
	p.authed = append(p.authed, req.Tool.Name)
	if p.deny[req.Tool.Name] {
		return policy.Decision{Kind: policy.DecisionDeny, Reason: "denied"}, nil
	}
	if reason, ok := p.suspend[req.Tool.Name]; ok {
		return policy.Decision{Kind: policy.DecisionSuspend, Reason: reason}, nil
	}
	return policy.Decision{Kind: policy.DecisionAllow}, nil
}

// countingTool is a tool that records how many times it actually executed.
func countingTool(t *testing.T, name string, ran *int32) tool.Tool {
	t.Helper()
	tl, err := tool.NewFunc(name, name, func(_ context.Context, _ echoInput) (string, error) {
		atomic.AddInt32(ran, 1)
		return "ran:" + name, nil
	})
	if err != nil {
		t.Fatalf("NewFunc(%q): %v", name, err)
	}
	return tl
}

// Test 4 & 9: suspend halts Run with a Suspended Result that carries the pending
// call and whose history ends with the dangling assistant tool_use message.
func TestRunSuspendHaltsWithResult(t *testing.T) {
	var ran int32
	fetch := countingTool(t, "fetch", &ran)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "fetch")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "needs approval"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Run(context.Background(), testAgent(t, fetch), Text("hi"))
	if err != nil {
		t.Fatalf("Run error = %v, want nil (suspend is not an error)", err)
	}
	if !res.Suspended {
		t.Fatal("Suspended = false, want true")
	}
	if len(res.PendingCalls) != 1 {
		t.Fatalf("PendingCalls len = %d, want 1", len(res.PendingCalls))
	}
	pc := res.PendingCalls[0]
	if pc.Call.Name != "fetch" || pc.Tool.Name != "fetch" || pc.Reason != "needs approval" {
		t.Fatalf("PendingCall = %+v, want fetch/needs approval", pc)
	}
	if got := atomic.LoadInt32(&ran); got != 0 {
		t.Fatalf("tool ran %d times on suspend, want 0", got)
	}
	last := res.Messages[len(res.Messages)-1]
	if last.Role != message.RoleAssistant || len(last.ToolUses()) != 1 {
		t.Fatalf("history tail = %+v, want assistant with one tool_use", last)
	}
}

// Test 6: suspend does not trigger OnError (it is not a runtime error).
func TestRunSuspendDoesNotTriggerOnError(t *testing.T) {
	fetch, err := tool.NewFunc("fetch", "fetch", func(_ context.Context, _ echoInput) (string, error) {
		return "x", nil
	})
	if err != nil {
		t.Fatalf("NewFunc: %v", err)
	}
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "fetch")),
	}}
	ch := &countingHooks{}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "approve"}}
	r, err := New(m, WithPolicy(pol), WithHooks(ch.hooks()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.Run(context.Background(), testAgent(t, fetch), Text("hi")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := ch.onError.Load(); n != 0 {
		t.Fatalf("OnError fired %d times on suspend, want 0", n)
	}
}

// Test 11: in a mixed Allow+Suspend batch the whole batch suspends; PendingCalls
// contains only the suspended call; neither tool executes; history keeps both
// tool_use blocks with no following tool_result message.
func TestRunMixedAllowSuspendBatch(t *testing.T) {
	var allowRan, suspendRan int32
	allow := countingTool(t, "alpha", &allowRan)
	susp := countingTool(t, "fetch", &suspendRan)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "alpha"), toolUse("call_2", "fetch")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "approve"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Run(context.Background(), testAgent(t, allow, susp), Text("hi"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Suspended {
		t.Fatal("Suspended = false, want true")
	}
	if len(res.PendingCalls) != 1 || res.PendingCalls[0].Call.Name != "fetch" {
		t.Fatalf("PendingCalls = %+v, want only fetch", res.PendingCalls)
	}
	if allowRan != 0 || suspendRan != 0 {
		t.Fatalf("tools ran (allow=%d suspend=%d), want both 0", allowRan, suspendRan)
	}
	for _, msg := range res.Messages {
		if msg.Role == message.RoleTool {
			t.Fatal("found a RoleTool message; no tool_result should be appended on suspend")
		}
	}
	last := res.Messages[len(res.Messages)-1]
	if last.Role != message.RoleAssistant || len(last.ToolUses()) != 2 {
		t.Fatalf("history tail = %+v, want assistant with two tool_uses", last)
	}
}

// Test 8: deny takes precedence over suspend in the same batch.
func TestRunDenyTakesPrecedenceOverSuspend(t *testing.T) {
	fetch, _ := tool.NewFunc("fetch", "fetch", func(_ context.Context, _ echoInput) (string, error) { return "", nil })
	blocked, _ := tool.NewFunc("blocked", "blocked", func(_ context.Context, _ echoInput) (string, error) { return "", nil })
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "fetch"), toolUse("call_2", "blocked")),
	}}
	pol := &kindPolicy{
		suspend: map[string]string{"fetch": "approve"},
		deny:    map[string]bool{"blocked": true},
	}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Run(context.Background(), testAgent(t, fetch, blocked), Text("hi"))
	var tde *ToolDeniedError
	if !errors.As(err, &tde) {
		t.Fatalf("error = %v, want ToolDeniedError", err)
	}
	if res != nil {
		t.Fatalf("result = %+v, want nil on deny", res)
	}
}

// Test 12: deny at index 0 fails fast; the later call is never authorized.
func TestRunDenyIndexZeroFailFast(t *testing.T) {
	blocked, _ := tool.NewFunc("blocked", "blocked", func(_ context.Context, _ echoInput) (string, error) { return "", nil })
	var ran int32
	alpha := countingTool(t, "alpha", &ran)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "blocked"), toolUse("call_2", "alpha")),
	}}
	pol := &kindPolicy{deny: map[string]bool{"blocked": true}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.Run(context.Background(), testAgent(t, blocked, alpha), Text("hi")); !errors.Is(err, ErrToolDenied) {
		t.Fatalf("error = %v, want ErrToolDenied", err)
	}
	for _, name := range pol.authed {
		if name == "alpha" {
			t.Fatal("alpha was authorized; deny at index 0 must fail fast before later calls")
		}
	}
	if ran != 0 {
		t.Fatalf("alpha ran %d times, want 0", ran)
	}
}

// Test 13: the serial refactor authorizes the whole batch before any execution,
// so an Allow at index 0 does not execute when a later call is denied.
func TestRunDenyPreventsEarlierAllowedExecution(t *testing.T) {
	var ran int32
	alpha := countingTool(t, "alpha", &ran)
	blocked, _ := tool.NewFunc("blocked", "blocked", func(_ context.Context, _ echoInput) (string, error) { return "", nil })
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "alpha"), toolUse("call_2", "blocked")),
	}}
	pol := &kindPolicy{deny: map[string]bool{"blocked": true}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.Run(context.Background(), testAgent(t, alpha, blocked), Text("hi")); !errors.Is(err, ErrToolDenied) {
		t.Fatalf("error = %v, want ErrToolDenied", err)
	}
	if ran != 0 {
		t.Fatalf("alpha (index 0, allowed) ran %d times; authorize-all-before-execute must prevent it, want 0", ran)
	}
}

// Test 10: the parallel path also suspends; no tool executes.
func TestRunParallelSuspendHalts(t *testing.T) {
	var aRan, fRan int32
	alpha := countingTool(t, "alpha", &aRan)
	fetch := countingTool(t, "fetch", &fRan)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "alpha"), toolUse("call_2", "fetch")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "approve"}}
	r, err := New(m, WithPolicy(pol), WithMaxConcurrency(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Run(context.Background(), testAgent(t, alpha, fetch), Text("hi"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Suspended || len(res.PendingCalls) != 1 || res.PendingCalls[0].Call.Name != "fetch" {
		t.Fatalf("res = {Suspended:%v Pending:%+v}, want suspended with only fetch", res.Suspended, res.PendingCalls)
	}
	if aRan != 0 || fRan != 0 {
		t.Fatalf("tools ran (alpha=%d fetch=%d), want both 0", aRan, fRan)
	}
}

// Test 15: I3 mutual exclusivity — a non-suspended successful run reports
// Suspended=false with no pending calls.
func TestRunCompletedResultNotSuspended(t *testing.T) {
	m := &scriptedModel{responses: []message.Message{message.Assistant(message.NewText("done"))}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Run(context.Background(), testAgent(t), Text("hi"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Suspended || len(res.PendingCalls) != 0 {
		t.Fatalf("res = {Suspended:%v Pending:%+v}, want completed", res.Suspended, res.PendingCalls)
	}
}

// Test 14: Stream downgrades DecisionSuspend to a deny error (no suspended
// Result, no new event type).
func TestStreamSuspendTreatedAsDeny(t *testing.T) {
	fetch, _ := tool.NewFunc("fetch", "fetch", func(_ context.Context, _ echoInput) (string, error) { return "", nil })
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "fetch")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "approve"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var streamErr error
	for ev, err := range r.Stream(context.Background(), testAgent(t, fetch), Text("hi")) {
		if err != nil {
			streamErr = err
		}
		_ = ev
	}
	var tde *ToolDeniedError
	if !errors.As(streamErr, &tde) {
		t.Fatalf("stream error = %v, want ToolDeniedError (suspend downgraded to deny)", streamErr)
	}
}

// Test 14 (parallel): Stream parallel path also downgrades suspend to deny.
func TestStreamParallelSuspendTreatedAsDeny(t *testing.T) {
	alpha, _ := tool.NewFunc("alpha", "alpha", func(_ context.Context, _ echoInput) (string, error) { return "", nil })
	fetch, _ := tool.NewFunc("fetch", "fetch", func(_ context.Context, _ echoInput) (string, error) { return "", nil })
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(toolUse("call_1", "alpha"), toolUse("call_2", "fetch")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "approve"}}
	r, err := New(m, WithPolicy(pol), WithMaxConcurrency(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var streamErr error
	for ev, err := range r.Stream(context.Background(), testAgent(t, alpha, fetch), Text("hi")) {
		if err != nil {
			streamErr = err
		}
		_ = model.Event(ev)
	}
	var tde *ToolDeniedError
	if !errors.As(streamErr, &tde) {
		t.Fatalf("stream error = %v, want ToolDeniedError", streamErr)
	}
}
