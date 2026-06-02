package runner

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/tool"
)

// ecCapture records the ExecutionContext each tool invocation observed, keyed by
// the tool's input text so tests can assert per-call identity. It is safe for
// the parallel execution path.
type ecCapture struct {
	mu   sync.Mutex
	seen map[string]tool.ExecutionContext
	ok   map[string]bool
}

func newECCapture() *ecCapture {
	return &ecCapture{seen: map[string]tool.ExecutionContext{}, ok: map[string]bool{}}
}

func (c *ecCapture) record(key string, ec tool.ExecutionContext, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen[key] = ec
	c.ok[key] = ok
}

// captureTool returns a tool that records the ExecutionContext it sees under the
// input's "text" value. When parallelSafe is true it declares ParallelSafe.
func captureTool(t *testing.T, name string, cap *ecCapture, parallelSafe bool) tool.Tool {
	t.Helper()
	opts := []tool.Option{}
	if parallelSafe {
		opts = append(opts, tool.WithEffects(tool.Effects{ParallelSafe: true}))
	}
	tl, err := tool.NewFunc(name, "capture ec", func(ctx context.Context, in echoInput) (string, error) {
		ec, ok := tool.ExecutionContextFrom(ctx)
		cap.record(in.Text, ec, ok)
		return "ok", nil
	}, opts...)
	if err != nil {
		t.Fatalf("NewFunc error: %v", err)
	}
	return tl
}

func TestRunInjectsExecutionContext(t *testing.T) {
	cap := newECCapture()
	tl := captureTool(t, "cap", cap, false)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "cap", json.RawMessage(`{"text":"a"}`))),
		message.Assistant(message.NewText("done")),
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	if _, err := r.Run(context.Background(), testAgent(t, tl), Text("hi"), WithRunID("run_123")); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	ec := cap.seen["a"]
	if !cap.ok["a"] {
		t.Fatalf("tool did not observe an ExecutionContext")
	}
	want := tool.ExecutionContext{RunID: "run_123", ToolCallID: "call_1", IdempotencyKey: "run_123:call_1"}
	if ec != want {
		t.Fatalf("ExecutionContext = %+v, want %+v", ec, want)
	}
}

func TestRunWithoutRunIDEmptyKey(t *testing.T) {
	cap := newECCapture()
	tl := captureTool(t, "cap", cap, false)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "cap", json.RawMessage(`{"text":"a"}`))),
		message.Assistant(message.NewText("done")),
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	if _, err := r.Run(context.Background(), testAgent(t, tl), Text("hi")); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	ec := cap.seen["a"]
	if !cap.ok["a"] {
		t.Fatalf("tool did not observe an ExecutionContext")
	}
	// The Runner always injects, so ToolCallID is set even without a RunID, but
	// the IdempotencyKey stays empty (backward compatible).
	want := tool.ExecutionContext{RunID: "", ToolCallID: "call_1", IdempotencyKey: ""}
	if ec != want {
		t.Fatalf("ExecutionContext = %+v, want %+v", ec, want)
	}
}

func TestBeforeToolHookSeesExecutionContext(t *testing.T) {
	echo, err := tool.NewFunc("echo", "echo", func(ctx context.Context, in echoInput) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewFunc error: %v", err)
	}
	var hookKey string
	var hookOK bool
	h := &hooks.Hooks{
		BeforeTool: func(ctx context.Context, c hooks.ToolCall) context.Context {
			ec, ok := tool.ExecutionContextFrom(ctx)
			hookKey = ec.IdempotencyKey
			hookOK = ok
			return ctx
		},
	}
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "echo", json.RawMessage(`{"text":"a"}`))),
		message.Assistant(message.NewText("done")),
	}}
	r, err := New(m, WithHooks(h))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	if _, err := r.Run(context.Background(), testAgent(t, echo), Text("hi"), WithRunID("run_9")); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !hookOK {
		t.Fatalf("BeforeTool hook did not observe an ExecutionContext")
	}
	if hookKey != "run_9:call_1" {
		t.Fatalf("BeforeTool IdempotencyKey = %q, want run_9:call_1", hookKey)
	}
}

func TestSuspendedRunCarriesRunID(t *testing.T) {
	cap := newECCapture()
	tl := captureTool(t, "cap", cap, false)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "cap", json.RawMessage(`{"text":"a"}`))),
	}}
	pol := &kindPolicy{suspend: map[string]string{"cap": "approve"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Run(context.Background(), testAgent(t, tl), Text("hi"), WithRunID("run_r"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Suspended {
		t.Fatalf("run did not suspend")
	}
	sr, err := res.SuspendedRun()
	if err != nil {
		t.Fatalf("SuspendedRun: %v", err)
	}
	if sr.RunID != "run_r" {
		t.Fatalf("SuspendedRun.RunID = %q, want run_r", sr.RunID)
	}
}

func TestResumeApprovedReusesSuspendedRunID(t *testing.T) {
	cap := newECCapture()
	tl := captureTool(t, "cap", cap, false)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "cap", json.RawMessage(`{"text":"a"}`))),
		message.Assistant(message.NewText("final")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"cap": "approve"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := testAgent(t, tl)
	res, err := r.Run(context.Background(), a, Text("hi"), WithRunID("run_r"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	sr, err := res.SuspendedRun()
	if err != nil {
		t.Fatalf("SuspendedRun: %v", err)
	}
	if _, err := r.ResumeApproved(context.Background(), a, sr, []Approval{{CallID: "call_1", Approved: true}}); err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	// The approved call runs on resume and must see the same IdempotencyKey it
	// would have in the original run: a deterministic function of the suspended
	// RunID and the tool_use ID (loop-semantics I13).
	want := tool.ExecutionContext{RunID: "run_r", ToolCallID: "call_1", IdempotencyKey: "run_r:call_1"}
	if cap.seen["a"] != want {
		t.Fatalf("resumed ExecutionContext = %+v, want %+v", cap.seen["a"], want)
	}
}

func TestParallelInjectsPerCallExecutionContext(t *testing.T) {
	cap := newECCapture()
	tl := captureTool(t, "cap", cap, true)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(
			message.NewToolUse("call_a", "cap", json.RawMessage(`{"text":"a"}`)),
			message.NewToolUse("call_b", "cap", json.RawMessage(`{"text":"b"}`)),
		),
		message.Assistant(message.NewText("done")),
	}}
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	if _, err := r.Run(context.Background(), testAgent(t, tl), Text("hi"), WithRunID("run_p")); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if cap.seen["a"].ToolCallID != "call_a" || cap.seen["a"].IdempotencyKey != "run_p:call_a" {
		t.Fatalf("call a ec = %+v", cap.seen["a"])
	}
	if cap.seen["b"].ToolCallID != "call_b" || cap.seen["b"].IdempotencyKey != "run_p:call_b" {
		t.Fatalf("call b ec = %+v", cap.seen["b"])
	}
}
