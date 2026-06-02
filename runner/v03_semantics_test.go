package runner

// Behavior tests for the v0.3 core-semantics fixes:
//   - batch-terminal handoff (serial path must match parallel attribution),
//   - parallel fail-fast must not let an induced cancellation shadow the real
//     first error (loop-semantics I6),
//   - Stream emits one TurnMessage per model turn and exactly one terminal
//     FinalMessage, and rejects a provider that violates the event contract.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/tool"
)

type noInput struct{}

// attrRec records, per tool name, the "agent/mode" attribution seen by both the
// BeforeTool hook and the Policy, so a test can assert which mode a tool ran
// under.
type attrRec struct {
	mu     sync.Mutex
	byHook map[string]string
	byPol  map[string]string
}

func newAttrRec() *attrRec {
	return &attrRec{byHook: map[string]string{}, byPol: map[string]string{}}
}

func (a *attrRec) hooks() *hooks.Hooks {
	return &hooks.Hooks{
		BeforeTool: func(ctx context.Context, tc hooks.ToolCall) context.Context {
			a.mu.Lock()
			a.byHook[tc.Tool.Name] = tc.AgentName + "/" + tc.ModeName
			a.mu.Unlock()
			return ctx
		},
	}
}

func (a *attrRec) Authorize(_ context.Context, req policy.Request) (policy.Decision, error) {
	a.mu.Lock()
	a.byPol[req.Tool.Name] = req.AgentName + "/" + req.ModeName
	a.mu.Unlock()
	return policy.Decision{Allow: true}, nil
}

// TestHandoffBatchAttributionEquivalence pins the fix for the serial mid-batch
// handoff bug: in a [handoff, tool] batch, the trailing tool must be attributed
// to the pre-handoff agent/mode, identically under serial and parallel
// execution. Before the fix the serial path switched mode mid-batch, so the
// trailing tool was attributed to the post-handoff mode.
func TestHandoffBatchAttributionEquivalence(t *testing.T) {
	b := buildSimpleAgent(t, "B")
	handoff, err := agentNewHandoff(t, b)
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}
	alpha, err := tool.NewFunc("alpha", "alpha", func(context.Context, noInput) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewFunc: %v", err)
	}
	modeA := mustMode(t, "default", "A", handoff, alpha)
	a := mustAgent(t, "A", modeA)

	turns := []scriptTurn{
		{calls: []scriptCall{{id: "h", name: "handoff_to_B"}, {id: "a", name: "alpha"}}},
		{}, // final text turn (runs under B after the handoff)
	}

	run := func(conc int) (string, string) {
		rec := newAttrRec()
		opts := []Option{WithHooks(rec.hooks()), WithPolicy(rec)}
		if conc > 1 {
			opts = append(opts, WithMaxConcurrency(conc))
		}
		r, err := New(&propModel{turns: turns}, opts...)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		res, err := r.Run(context.Background(), a, Text("hi"))
		if err != nil {
			t.Fatalf("[conc=%d] Run: %v", conc, err)
		}
		// The run must still end on agent B (handoff applied at batch end).
		if res.LastAgent.Name() != "B" {
			t.Fatalf("[conc=%d] last agent = %q, want B", conc, res.LastAgent.Name())
		}
		return rec.byHook["alpha"], rec.byPol["alpha"]
	}

	for _, conc := range []int{1, 4} {
		hookAttr, polAttr := run(conc)
		if hookAttr != "A/default" {
			t.Fatalf("[conc=%d] alpha hook attribution = %q, want A/default", conc, hookAttr)
		}
		if polAttr != "A/default" {
			t.Fatalf("[conc=%d] alpha policy attribution = %q, want A/default", conc, polAttr)
		}
	}
}

// blockingTool blocks until the context is cancelled, then returns ctx.Err().
type blockingTool struct{ name string }

func (b blockingTool) Info() tool.Info {
	return tool.Info{Name: b.name, Description: b.name, InputSchema: emptyObjSchema}
}

func (b blockingTool) Run(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	<-ctx.Done()
	return tool.Result{}, ctx.Err()
}

// failingTool returns err immediately, triggering fail-fast cancellation.
type failingTool struct {
	name string
	err  error
}

func (f failingTool) Info() tool.Info {
	return tool.Info{Name: f.name, Description: f.name, InputSchema: emptyObjSchema}
}

func (f failingTool) Run(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, f.err
}

// slowCtxAwareTool succeeds after a short delay while its context stays live,
// but returns ctx.Err() promptly once cancelled. This lets one batch be run both
// serially (delay elapses, tool succeeds) and in parallel (a failing sibling
// cancels it, so it returns an induced ctx.Err()), which is exactly what I6's
// serial/parallel equivalence needs to be exercised against.
type slowCtxAwareTool struct {
	name  string
	delay time.Duration
}

func (s slowCtxAwareTool) Info() tool.Info {
	return tool.Info{Name: s.name, Description: s.name, InputSchema: emptyObjSchema}
}

func (s slowCtxAwareTool) Run(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	select {
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	case <-time.After(s.delay):
		return tool.Result{}, nil
	}
}

// selfCancelTool returns context.Canceled as its own domain result, without ever
// observing a cancellation. It models a (discouraged) tool that uses
// context.Canceled as a domain error; when it is the first tool to fail, its
// error must not be mislabeled as an induced cancellation.
type selfCancelTool struct{ name string }

func (s selfCancelTool) Info() tool.Info {
	return tool.Info{Name: s.name, Description: s.name, InputSchema: emptyObjSchema}
}

func (s selfCancelTool) Run(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, context.Canceled
}

// cancelThenFailTool waits until its context is cancelled (by a sibling's
// fail-fast) and only then returns err. Because it can only fail *after* a
// sibling has already cancelled the batch, it provides a deterministic ordering:
// it never triggers the batch's fail-fast itself.
type cancelThenFailTool struct {
	name string
	err  error
}

func (c cancelThenFailTool) Info() tool.Info {
	return tool.Info{Name: c.name, Description: c.name, InputSchema: emptyObjSchema}
}

func (c cancelThenFailTool) Run(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	<-ctx.Done()
	return tool.Result{}, c.err
}

// runBatchErr runs a single-turn batch under the given concurrency and returns
// the terminating error (if any).
func runBatchErr(t *testing.T, m model.Model, a *agent.Agent, conc int) error {
	t.Helper()
	var opts []Option
	if conc > 1 {
		opts = append(opts, WithMaxConcurrency(conc))
	}
	r, err := New(m, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runErr := r.Run(context.Background(), a, Text("hi"))
	return runErr
}

// TestParallelInducedCancelDoesNotShadowError pins loop-semantics I6: a failing
// higher-index tool that fail-fast cancels a lower-index ctx-aware sibling must
// surface the genuine error, not the sibling's induced context.Canceled. The
// slow tool is index 0 (so a naive "first error by call order" would return its
// cancellation); boom is index 1. Asserting serial == parallel == boom proves
// the equivalence, not just the parallel behavior in isolation.
func TestParallelInducedCancelDoesNotShadowError(t *testing.T) {
	errBoom := errors.New("boom")
	modeA := mustMode(t, "default", "worker",
		slowCtxAwareTool{name: "slow", delay: 30 * time.Millisecond},
		failingTool{name: "boom", err: errBoom},
	)
	a := mustAgent(t, "worker", modeA)
	turns := []scriptTurn{
		{calls: []scriptCall{{id: "s", name: "slow"}, {id: "b", name: "boom"}}},
	}
	for _, conc := range []int{1, 4} {
		runErr := runBatchErr(t, &propModel{turns: turns}, a, conc)
		if !errors.Is(runErr, errBoom) {
			t.Fatalf("[conc=%d] error = %v, want boom (genuine first error by call order)", conc, runErr)
		}
		if errors.Is(runErr, context.Canceled) {
			t.Fatalf("[conc=%d] induced cancellation shadowed the real error: %v", conc, runErr)
		}
	}
}

// TestParallelGenuineLowIndexCanceledPreserved guards the boundary of the
// induced-cancel rule (loop-semantics §7.1): a low-index tool that returns
// context.Canceled as its own result, *before* any sibling cancels the batch,
// is the genuine fail-fast trigger and must not be mislabeled as induced. The
// high-index tool only fails after being cancelled, so the ordering is
// deterministic and both serial and parallel must return context.Canceled.
func TestParallelGenuineLowIndexCanceledPreserved(t *testing.T) {
	errBoom := errors.New("boom")
	modeA := mustMode(t, "default", "worker",
		selfCancelTool{name: "cancel"},
		cancelThenFailTool{name: "boom", err: errBoom},
	)
	a := mustAgent(t, "worker", modeA)
	turns := []scriptTurn{
		{calls: []scriptCall{{id: "c", name: "cancel"}, {id: "b", name: "boom"}}},
	}
	for _, conc := range []int{1, 4} {
		runErr := runBatchErr(t, &propModel{turns: turns}, a, conc)
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("[conc=%d] error = %v, want context.Canceled (genuine low-index result preserved)", conc, runErr)
		}
		if errors.Is(runErr, errBoom) {
			t.Fatalf("[conc=%d] genuine low-index cancel was mislabeled and shadowed by boom: %v", conc, runErr)
		}
	}
}

// TestParallelExternalCancelOutranks confirms that an external (parent ctx)
// cancellation is still reported as context.Canceled, not masked by the induced
// cancellation handling.
func TestParallelExternalCancelOutranks(t *testing.T) {
	modeA := mustMode(t, "default", "worker", blockingTool{name: "s1"}, blockingTool{name: "s2"})
	a := mustAgent(t, "worker", modeA)
	turns := []scriptTurn{
		{calls: []scriptCall{{id: "1", name: "s1"}, {id: "2", name: "s2"}}},
	}
	r, err := New(&propModel{turns: turns}, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { cancel() }()
	_, runErr := r.Run(ctx, a, Text("hi"))
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", runErr)
	}
}

// TestStreamEmitsTurnMessagePerTurnAndOneFinal pins the event model: each model
// turn surfaces one TurnMessage (including turns that contain tool calls), and
// the Runner emits exactly one terminal FinalMessage equal to the last turn.
func TestStreamEmitsTurnMessagePerTurnAndOneFinal(t *testing.T) {
	echo, err := tool.NewFunc("echo", "Echo", func(_ context.Context, in echoInput) (string, error) {
		return "echo: " + in.Text, nil
	})
	if err != nil {
		t.Fatalf("NewFunc: %v", err)
	}
	m := &streamOnlyModel{turns: [][]model.Event{
		{model.TurnMessage{Message: message.Assistant(message.NewToolUse("c1", "echo", json.RawMessage(`{"text":"go"}`)))}},
		{model.TurnMessage{Message: message.Assistant(message.NewText("done"))}},
	}}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var turnMsgs []message.Message
	var finals []message.Message
	for ev, err := range r.Stream(context.Background(), testAgent(t, echo), Text("hi")) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		switch e := ev.(type) {
		case model.TurnMessage:
			turnMsgs = append(turnMsgs, e.Message)
		case model.FinalMessage:
			finals = append(finals, e.Message)
		}
	}

	if len(turnMsgs) != 2 {
		t.Fatalf("TurnMessage count = %d, want 2", len(turnMsgs))
	}
	if len(turnMsgs[0].ToolUses()) != 1 {
		t.Fatalf("first turn should carry the tool_use; got %+v", turnMsgs[0])
	}
	if len(finals) != 1 {
		t.Fatalf("FinalMessage count = %d, want 1", len(finals))
	}
	if finals[0].Text() != "done" {
		t.Fatalf("final text = %q, want done", finals[0].Text())
	}
	// The terminal FinalMessage mirrors the last turn's TurnMessage.
	if !messagesEqual([]message.Message{turnMsgs[1]}, []message.Message{finals[0]}) {
		t.Fatalf("terminal TurnMessage and FinalMessage differ:\n turn=%v\nfinal=%v", turnMsgs[1], finals[0])
	}
}

// streamContractErr drains a single-turn stream from events and returns the
// terminating error.
func streamContractErr(t *testing.T, events []model.Event) error {
	t.Helper()
	m := &streamOnlyModel{events: events}
	r, err := New(m)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var last error
	for ev, err := range r.Stream(context.Background(), buildSimpleAgent(t, "A"), Text("hi")) {
		_ = ev
		if err != nil {
			last = err
			break
		}
	}
	return last
}

// TestStreamMultipleTurnMessagesIsContractError pins that a model turn may yield
// at most one TurnMessage. A second TurnMessage is a contract violation, not a
// silently-accepted re-emission.
func TestStreamMultipleTurnMessagesIsContractError(t *testing.T) {
	err := streamContractErr(t, []model.Event{
		model.TurnMessage{Message: message.Assistant(message.NewText("one"))},
		model.TurnMessage{Message: message.Assistant(message.NewText("two"))},
	})
	if !errors.Is(err, ErrStreamContract) {
		t.Fatalf("error = %v, want ErrStreamContract for a second TurnMessage", err)
	}
}

// TestStreamEventAfterTurnMessageIsContractError pins that TurnMessage must be
// the final event of a turn: any trailing event violates the contract.
func TestStreamEventAfterTurnMessageIsContractError(t *testing.T) {
	err := streamContractErr(t, []model.Event{
		model.TurnMessage{Message: message.Assistant(message.NewText("done"))},
		model.TextDelta{Text: "trailing"},
	})
	if !errors.Is(err, ErrStreamContract) {
		t.Fatalf("error = %v, want ErrStreamContract for an event after TurnMessage", err)
	}
}
