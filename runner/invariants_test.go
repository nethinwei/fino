package runner

// Property-based verification of the loop invariants in
// docs/spec/loop-semantics.md (I1–I9). Each property runs the real Runner over
// many randomly generated scripts (see model_state_test.go) and asserts the
// invariant directly on the observable output.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
)

const propIterations = 400

var allKinds = []scriptKind{kindSuccess, kindNotFound, kindDenied, kindToolError, kindMaxTurns}

// classify maps a terminal error to a stable token for equivalence checks.
func classify(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrToolNotFound):
		return "notfound"
	case errors.As(err, new(*ToolDeniedError)):
		return "denied"
	case errors.Is(err, ErrMaxTurns):
		return "maxturns"
	default:
		return "toolerror"
	}
}

func expectedToken(kind scriptKind) string {
	switch kind {
	case kindSuccess:
		return "ok"
	case kindNotFound:
		return "notfound"
	case kindDenied:
		return "denied"
	case kindToolError:
		return "toolerror"
	case kindMaxTurns:
		return "maxturns"
	}
	return "?"
}

// runOutcome bundles everything a property needs from a single run.
type runOutcome struct {
	res     *Result
	err     error
	events  []string
	onError int64
}

// runScript executes one script against the real Runner at the given
// concurrency and returns its observable outcome.
func runScript(t *testing.T, s propScript, conc int) runOutcome {
	t.Helper()
	log := &eventLog{}
	a := buildPropAgent(t, log)
	ch := &countingHooks{}
	opts := []Option{WithHooks(ch.hooks())}
	if s.denyTool != "" {
		opts = append(opts, WithPolicy(recPolicy{deny: map[string]bool{s.denyTool: true}, log: log}))
	} else {
		opts = append(opts, WithPolicy(recPolicy{deny: map[string]bool{}, log: log}))
	}
	if conc > 1 {
		opts = append(opts, WithMaxConcurrency(conc))
	}
	r, err := New(&propModel{turns: s.turns}, opts...)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	rn, err := r.NewRun(context.Background(), a, Text("hi"))
	if err != nil {
		return runOutcome{err: err, onError: ch.onError.Load()}
	}
	var res *Result
	var runErr error
	for i := 0; i < s.maxTurns; i++ {
		out, err := rn.Step()
		if err != nil {
			runErr = err
			break
		}
		if out.Status == StepCompleted {
			res = rn.Result(out.FinalMessage)
			break
		}
		if out.Status == StepSuspended {
			res = rn.SuspendedResult(out.PendingCalls)
			break
		}
	}
	if runErr == nil && res == nil {
		runErr = fmt.Errorf("%w: %d", ErrMaxTurns, s.maxTurns)
		r.FireOnError(rn.Context(), runErr)
	}
	return runOutcome{res: res, err: runErr, events: log.snapshot(), onError: ch.onError.Load()}
}

// TestInvariantsRun checks I1, I2, I3, I4, I5, I8 over random scripts at both
// serial and parallel concurrency, plus the success-path oracle match.
func TestInvariantsRun(t *testing.T) {
	rng := rand.New(rand.NewSource(0xF1A0))
	for iter := 0; iter < propIterations; iter++ {
		kind := allKinds[rng.Intn(len(allKinds))]
		s := genScript(rng, kind)
		for _, conc := range []int{1, 4} {
			oc := runScript(t, s, conc)

			// I3: exactly one of (Result, error) is non-nil.
			if (oc.res != nil) == (oc.err != nil) {
				t.Fatalf("[%s conc=%d] I3 violated: res=%v err=%v", tokenName(kind), conc, oc.res != nil, oc.err)
			}

			// Terminal error classification matches the script kind.
			if got := classify(oc.err); got != expectedToken(kind) {
				t.Fatalf("[%s conc=%d] wrong terminal: got %q want %q (err=%v)", tokenName(kind), conc, got, expectedToken(kind), oc.err)
			}

			// I4: OnError fires once on error, zero on success.
			wantOnErr := int64(0)
			if kind != kindSuccess {
				wantOnErr = 1
			}
			if oc.onError != wantOnErr {
				t.Fatalf("[%s conc=%d] I4 violated: OnError=%d want %d", tokenName(kind), conc, oc.onError, wantOnErr)
			}

			// I5: at every prefix of the event log, #auth >= #run, so no tool
			// executes before it is authorized.
			if bad := authBeforeRun(oc.events); bad >= 0 {
				t.Fatalf("[%s conc=%d] I5 violated at index %d: %v", tokenName(kind), conc, bad, oc.events)
			}

			// Denied tool must never execute.
			if kind == kindDenied && containsEvent(oc.events, "run:"+s.denyTool) {
				t.Fatalf("[%s conc=%d] denied tool executed: %v", tokenName(kind), conc, oc.events)
			}

			if kind == kindSuccess {
				// I1 + I2 + structure: history equals the spec-derived oracle.
				want := expectedSuccessHistory(s)
				if !messagesEqual(oc.res.Messages, want) {
					t.Fatalf("[success conc=%d] oracle mismatch\n got: %v\nwant: %v", conc, oc.res.Messages, want)
				}
				// I8: no Runner-injected system message leaks into history.
				if hasSystemRole(oc.res.Messages) {
					t.Fatalf("[success conc=%d] I8 violated: system message in history", conc)
				}
			}
		}
	}
}

// TestInvariantParallelEquivalence checks I6: serial and parallel runs produce
// the same terminal classification, and identical history on success.
func TestInvariantParallelEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(0xBEEF))
	for iter := 0; iter < propIterations; iter++ {
		kind := allKinds[rng.Intn(len(allKinds))]
		s := genScript(rng, kind)
		serial := runScript(t, s, 1)
		parallel := runScript(t, s, 4)

		if classify(serial.err) != classify(parallel.err) {
			t.Fatalf("[%s] I6 terminal mismatch: serial=%q parallel=%q", tokenName(kind), classify(serial.err), classify(parallel.err))
		}
		if kind == kindSuccess && !messagesEqual(serial.res.Messages, parallel.res.Messages) {
			t.Fatalf("[success] I6 history mismatch\nserial:   %v\nparallel: %v", serial.res.Messages, parallel.res.Messages)
		}
	}
}

// TestInvariantHandoffLastWins checks I7: when one batch contains multiple
// handoffs, the last one in call order wins.
func TestInvariantHandoffLastWins(t *testing.T) {
	b := buildSimpleAgent(t, "B")
	c := buildSimpleAgent(t, "C")
	hb, err := agentNewHandoff(t, b)
	if err != nil {
		t.Fatalf("handoff B: %v", err)
	}
	hc, err := agentNewHandoff(t, c)
	if err != nil {
		t.Fatalf("handoff C: %v", err)
	}
	mode := mustMode(t, "default", "router", hb, hc)
	a := mustAgent(t, "A", mode)

	turns := []scriptTurn{
		{calls: []scriptCall{{id: "h1", name: "handoff_to_B"}, {id: "h2", name: "handoff_to_C"}}},
		{}, // final text from C
	}
	r, err := New(&propModel{turns: turns})
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}
	rn, err := r.NewRun(context.Background(), a, Text("go"))
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	res, err := runSteps(rn)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.LastAgent == nil || res.LastAgent.Name() != "C" {
		t.Fatalf("I7 violated: LastAgent=%v want C", res.LastAgent)
	}
	if res.LastMode != "default" {
		t.Fatalf("I7 violated: LastMode=%q want default", res.LastMode)
	}
}

// TestInvariantCtxCancelNoEffects checks I9: a context cancelled before the run
// produces context.Canceled with no model or tool calls.
func TestInvariantCtxCancelNoEffects(t *testing.T) {
	log := &eventLog{}
	a := buildPropAgent(t, log)
	m := &propModel{turns: []scriptTurn{{calls: []scriptCall{{id: "c1", name: "alpha"}}}, {}}}
	ch := &countingHooks{}
	r, err := New(m, WithHooks(ch.hooks()))
	if err != nil {
		t.Fatalf("New runner: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rn, err := r.NewRun(ctx, a, Text("hi"))
	var res *Result
	var runErr error
	if err != nil {
		runErr = err
	} else {
		out, stepErr := rn.Step()
		if stepErr != nil {
			runErr = stepErr
		} else if out.Status == StepCompleted {
			res = rn.Result(out.FinalMessage)
		}
	}
	if res != nil || !errors.Is(runErr, context.Canceled) {
		t.Fatalf("I9: expected context.Canceled, got res=%v err=%v", res, runErr)
	}
	if m.i != 0 {
		t.Fatalf("I9 violated: model called %d times after cancel", m.i)
	}
	if len(log.snapshot()) != 0 {
		t.Fatalf("I9 violated: tool/policy events after cancel: %v", log.snapshot())
	}
	if ch.onError.Load() != 1 {
		t.Fatalf("I4 violated on cancel: OnError=%d want 1", ch.onError.Load())
	}
}

// streamOutcome bundles what a stream property needs.
type streamOutcome struct {
	finals    int
	streamErr int
	toolCalls int
	toolRes   int
	termErr   error
	onError   int64
	// firstResultBeforeCall is true if any ToolResult was seen before its
	// matching ToolCall (would violate event ordering).
	resultBeforeCall bool
}

// streamScript drives the Runner via StreamStep and collects ordered event statistics.
func streamScript(t *testing.T, s propScript, conc int) streamOutcome {
	t.Helper()
	log := &eventLog{}
	a := buildPropAgent(t, log)
	ch := &countingHooks{}
	opts := []Option{WithHooks(ch.hooks())}
	if s.denyTool != "" {
		opts = append(opts, WithPolicy(recPolicy{deny: map[string]bool{s.denyTool: true}, log: log}))
	}
	if conc > 1 {
		opts = append(opts, WithMaxConcurrency(conc))
	}
	r, err := New(&propModel{turns: s.turns}, opts...)
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	out := streamOutcome{}
	openCalls := map[string]bool{}
	processEvent := func(ev model.Event, stepErr error) bool {
		if stepErr != nil {
			out.termErr = stepErr
		}
		switch e := ev.(type) {
		case model.FinalMessage:
			out.finals++
		case model.StreamError:
			out.streamErr++
		case model.ToolCall:
			out.toolCalls++
			openCalls[e.Call.ID] = true
		case model.ToolResult:
			out.toolRes++
			if !openCalls[e.CallID] {
				out.resultBeforeCall = true
			}
		}
		return true
	}

	rn, err := r.NewRun(context.Background(), a, Text("hi"))
	if err != nil {
		r.FireOnError(context.Background(), err)
		processEvent(model.StreamError{Err: err}, err)
		out.onError = ch.onError.Load()
		return out
	}
	hitLimit := true
	for i := 0; i < s.maxTurns; i++ {
		stepOut, stepErr := rn.StreamStep(processEvent)
		if stepErr != nil {
			hitLimit = false
			break
		}
		if stepOut.Status != StepContinue {
			hitLimit = false
			break
		}
	}
	if hitLimit {
		maxErr := fmt.Errorf("%w: %d", ErrMaxTurns, s.maxTurns)
		r.FireOnError(rn.Context(), maxErr)
		processEvent(model.StreamError{Err: maxErr}, maxErr)
	}
	out.onError = ch.onError.Load()
	return out
}

// TestStreamInvariants checks I3, I4, and event ordering over random scripts.
func TestStreamInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(0x57EA))
	for iter := 0; iter < propIterations; iter++ {
		kind := allKinds[rng.Intn(len(allKinds))]
		s := genScript(rng, kind)
		for _, conc := range []int{1, 4} {
			oc := streamScript(t, s, conc)

			if classify(oc.termErr) != expectedToken(kind) {
				t.Fatalf("[stream %s conc=%d] wrong terminal: got %q want %q", tokenName(kind), conc, classify(oc.termErr), expectedToken(kind))
			}

			// I3: success yields exactly one terminal FinalMessage and no
			// StreamError; an error yields no FinalMessage and exactly one
			// StreamError paired with a non-nil iterator error.
			if kind == kindSuccess {
				if oc.finals != 1 || oc.streamErr != 0 {
					t.Fatalf("[stream success conc=%d] I3 violated: finals=%d streamErr=%d", conc, oc.finals, oc.streamErr)
				}
			} else {
				if oc.finals != 0 || oc.streamErr != 1 || oc.termErr == nil {
					t.Fatalf("[stream %s conc=%d] I3 violated: finals=%d streamErr=%d termErr=%v", tokenName(kind), conc, oc.finals, oc.streamErr, oc.termErr)
				}
			}

			// I4: OnError once on error, zero on success.
			wantOnErr := int64(0)
			if kind != kindSuccess {
				wantOnErr = 1
			}
			if oc.onError != wantOnErr {
				t.Fatalf("[stream %s conc=%d] I4 violated: OnError=%d want %d", tokenName(kind), conc, oc.onError, wantOnErr)
			}

			// Event ordering: no ToolResult precedes its ToolCall.
			if oc.resultBeforeCall {
				t.Fatalf("[stream %s conc=%d] ToolResult before ToolCall", tokenName(kind), conc)
			}

			// On success every executed tool yields both a ToolCall and a
			// ToolResult.
			if kind == kindSuccess && oc.toolCalls != oc.toolRes {
				t.Fatalf("[stream success conc=%d] ToolCall=%d != ToolResult=%d", conc, oc.toolCalls, oc.toolRes)
			}
		}
	}
}

// authBeforeRun returns the index where a run event would precede its
// authorization (a prefix with more run than auth events), or -1 if none.
func authBeforeRun(events []string) int {
	auth, run := 0, 0
	for i, e := range events {
		switch {
		case strings.HasPrefix(e, "auth:"):
			auth++
		case strings.HasPrefix(e, "run:"):
			run++
		}
		if run > auth {
			return i
		}
	}
	return -1
}

func containsEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}

func hasSystemRole(msgs []message.Message) bool {
	for _, m := range msgs {
		if m.Role == message.RoleSystem {
			return true
		}
	}
	return false
}

func tokenName(kind scriptKind) string { return expectedToken(kind) }
