package runner

// This file is the property-testing harness for the loop semantics defined in
// docs/spec/loop-semantics.md. It provides a programmable model, programmable
// tools, a recording policy and hooks, a random script generator, and a pure
// reference interpreter used as an oracle for success scripts. The property
// assertions live in invariants_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"math/rand"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/tool"
)

// messagesEqual reports whether two message slices are deeply equal, including
// json.RawMessage byte content.
func messagesEqual(a, b []message.Message) bool {
	return reflect.DeepEqual(a, b)
}

// emptyObjSchema is a minimal valid input schema for harness tools.
var emptyObjSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)

// scriptCall is one tool_use the model emits in a turn.
type scriptCall struct {
	id   string
	name string
}

// scriptTurn is one model response. Empty calls means a final text turn.
type scriptTurn struct {
	calls []scriptCall
}

// scriptKind classifies the expected terminal outcome of a script.
type scriptKind int

const (
	kindSuccess   scriptKind = iota // ends with a final text turn, no error
	kindNotFound                    // a turn calls a tool absent from the mode
	kindDenied                      // a turn calls a policy-denied tool
	kindToolError                   // a turn calls a tool configured to fail
	kindMaxTurns                    // more tool turns than maxTurns allows
)

// propScript is a deterministic program for one run.
type propScript struct {
	kind     scriptKind
	turns    []scriptTurn
	failTool string // tool name configured to return an error ("" = none)
	denyTool string // tool name the policy denies ("" = none)
	maxTurns int
}

// propModel returns the scripted turns in order. A fresh propModel is built per
// run so two runs of the same script (e.g. serial vs parallel) are independent.
type propModel struct {
	turns []scriptTurn
	i     int
}

func (m *propModel) Generate(ctx context.Context, _ []message.Message, _ []tool.Info, _ ...model.Option) (*message.Message, error) {
	if m.i >= len(m.turns) {
		msg := message.Assistant(message.NewText("final"))
		return &msg, nil
	}
	turn := m.turns[m.i]
	m.i++
	if len(turn.calls) == 0 {
		msg := message.Assistant(message.NewText("final"))
		return &msg, nil
	}
	blocks := make([]message.Block, len(turn.calls))
	for j, c := range turn.calls {
		blocks[j] = message.NewToolUse(c.id, c.name, json.RawMessage(`{}`))
	}
	msg := message.Assistant(blocks...)
	return &msg, nil
}

func (m *propModel) Stream(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		msg, err := m.Generate(ctx, messages, tools, opts...)
		if err != nil {
			yield(model.StreamError{Err: err}, err)
			return
		}
		yield(model.TurnMessage{Message: *msg}, nil)
	}
}

// eventLog is an ordered, concurrency-safe record of harness events. Tools may
// run in parallel, so writes are mutex-guarded.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(s string) {
	l.mu.Lock()
	l.events = append(l.events, s)
	l.mu.Unlock()
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// propTool is a programmable tool. okText is its result text; if fail is set it
// returns an error. It records "run:<name>" into the shared log.
type propTool struct {
	name string
	fail bool
	log  *eventLog
}

func (t *propTool) Info() tool.Info {
	return tool.Info{Name: t.name, Description: t.name, InputSchema: emptyObjSchema}
}

func (t *propTool) Run(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	t.log.add("run:" + t.name)
	if t.fail {
		return tool.Result{}, fmt.Errorf("tool %q failed", t.name)
	}
	return tool.Result{Content: []message.Block{message.NewText("ok:" + t.name)}}, nil
}

// recPolicy denies tools in deny and records "auth:<name>" into the shared log.
type recPolicy struct {
	deny map[string]bool
	log  *eventLog
}

func (p recPolicy) Authorize(_ context.Context, req policy.Request) (policy.Decision, error) {
	p.log.add("auth:" + req.Tool.Name)
	if p.deny[req.Tool.Name] {
		return policy.Decision{Allow: false, Reason: "denied by harness"}, nil
	}
	return policy.Decision{Allow: true}, nil
}

// countingHooks counts OnError invocations and records tool ordering, so tests
// can assert "OnError exactly once" and "authorize before execute".
type countingHooks struct {
	onError atomic.Int64
}

func (h *countingHooks) hooks() *hooks.Hooks {
	return &hooks.Hooks{
		OnError: func(context.Context, error) { h.onError.Add(1) },
	}
}

// okToolNames is the fixed set of non-failing tools available to the harness
// agent. genScript only references these plus the optional fail tool.
var okToolNames = []string{"alpha", "beta", "gamma"}

const failToolName = "boom"

// buildPropAgent builds a single-mode agent whose mode holds the ok tools plus
// one failing tool, all sharing the given log.
func buildPropAgent(t *testing.T, log *eventLog) *agent.Agent {
	t.Helper()
	tools := make([]tool.Tool, 0, len(okToolNames)+1)
	for _, n := range okToolNames {
		tools = append(tools, &propTool{name: n, log: log})
	}
	tools = append(tools, &propTool{name: failToolName, fail: true, log: log})
	mode, err := agent.NewMode("default", "harness", agent.WithTools(tools...))
	if err != nil {
		t.Fatalf("NewMode error: %v", err)
	}
	a, err := agent.New("harness", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New agent error: %v", err)
	}
	return a
}

// mustMode builds a Mode or fails the test.
func mustMode(t *testing.T, name, instr string, tools ...tool.Tool) agent.Mode {
	t.Helper()
	m, err := agent.NewMode(name, instr, agent.WithTools(tools...))
	if err != nil {
		t.Fatalf("NewMode(%q) error: %v", name, err)
	}
	return m
}

// mustAgent builds an Agent whose default mode is the first given mode.
func mustAgent(t *testing.T, name string, modes ...agent.Mode) *agent.Agent {
	t.Helper()
	opts := make([]agent.AgentOption, 0, len(modes)+1)
	for _, m := range modes {
		opts = append(opts, agent.WithMode(m))
	}
	opts = append(opts, agent.WithDefaultMode(modes[0].Name))
	a, err := agent.New(name, opts...)
	if err != nil {
		t.Fatalf("New agent(%q) error: %v", name, err)
	}
	return a
}

// buildSimpleAgent builds a single-mode agent with no tools.
func buildSimpleAgent(t *testing.T, name string) *agent.Agent {
	t.Helper()
	return mustAgent(t, name, mustMode(t, "default", "simple"))
}

// agentNewHandoff wraps agent.NewHandoffTool for tests.
func agentNewHandoff(t *testing.T, target *agent.Agent) (tool.Tool, error) {
	t.Helper()
	return agent.NewHandoffTool(target)
}

// genScript produces a random but well-formed script of the given kind.
func genScript(rng *rand.Rand, kind scriptKind) propScript {
	s := propScript{kind: kind, maxTurns: 10}
	callID := 0
	nextID := func() string { callID++; return fmt.Sprintf("call_%d", callID) }
	okBatch := func() scriptTurn {
		n := 1 + rng.Intn(3)
		calls := make([]scriptCall, n)
		for i := range calls {
			calls[i] = scriptCall{id: nextID(), name: okToolNames[rng.Intn(len(okToolNames))]}
		}
		return scriptTurn{calls: calls}
	}

	switch kind {
	case kindSuccess:
		toolTurns := rng.Intn(4) // 0..3 tool turns then a final text turn
		for i := 0; i < toolTurns; i++ {
			s.turns = append(s.turns, okBatch())
		}
		s.turns = append(s.turns, scriptTurn{}) // final text
	case kindNotFound:
		lead := rng.Intn(2)
		for i := 0; i < lead; i++ {
			s.turns = append(s.turns, okBatch())
		}
		s.turns = append(s.turns, scriptTurn{calls: []scriptCall{{id: nextID(), name: "ghost"}}})
	case kindDenied:
		victim := okToolNames[rng.Intn(len(okToolNames))]
		s.denyTool = victim
		lead := rng.Intn(2)
		for i := 0; i < lead; i++ {
			// lead batches must avoid the denied tool to actually reach denial later
			calls := []scriptCall{{id: nextID(), name: safeOther(rng, victim)}}
			s.turns = append(s.turns, scriptTurn{calls: calls})
		}
		s.turns = append(s.turns, scriptTurn{calls: []scriptCall{{id: nextID(), name: victim}}})
	case kindToolError:
		s.failTool = failToolName
		s.turns = append(s.turns, scriptTurn{calls: []scriptCall{{id: nextID(), name: failToolName}}})
	case kindMaxTurns:
		s.maxTurns = 3
		for i := 0; i < s.maxTurns+2; i++ {
			s.turns = append(s.turns, okBatch())
		}
	}
	return s
}

// safeOther returns an ok tool name different from avoid.
func safeOther(rng *rand.Rand, avoid string) string {
	for {
		n := okToolNames[rng.Intn(len(okToolNames))]
		if n != avoid {
			return n
		}
	}
}

// expectedSuccessHistory computes the oracle history for a kindSuccess script
// run via runner.Text("hi"). It mirrors [T-MODEL]/[T-TOOLS]/[T-FINAL] from the
// spec: user input, then per turn an assistant message and (for tool turns) one
// batched RoleTool message with results in call order.
func expectedSuccessHistory(s propScript) []message.Message {
	out := []message.Message{message.UserText("hi")}
	for _, turn := range s.turns {
		if len(turn.calls) == 0 {
			out = append(out, message.Assistant(message.NewText("final")))
			break
		}
		blocks := make([]message.Block, len(turn.calls))
		for j, c := range turn.calls {
			blocks[j] = message.NewToolUse(c.id, c.name, json.RawMessage(`{}`))
		}
		out = append(out, message.Assistant(blocks...))
		results := make([]message.Block, len(turn.calls))
		for j, c := range turn.calls {
			results[j] = message.NewToolResult(c.id, c.name, []message.Block{message.NewText("ok:" + c.name)}, false)
		}
		out = append(out, message.ToolResults(results...))
	}
	return out
}

// TestHarnessSmoke verifies the generator and oracle agree with the real Runner
// for a simple success script. It is a guard that the harness itself is sound.
func TestHarnessSmoke(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	s := genScript(rng, kindSuccess)
	log := &eventLog{}
	a := buildPropAgent(t, log)
	r, err := New(&propModel{turns: s.turns}, WithMaxTurns(s.maxTurns))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	res, err := r.Run(context.Background(), a, Text("hi"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	want := expectedSuccessHistory(s)
	if !messagesEqual(res.Messages, want) {
		t.Fatalf("history mismatch\n got: %v\nwant: %v", res.Messages, want)
	}
}
