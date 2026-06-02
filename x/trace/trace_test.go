package trace_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/trace"
)

// recorder is an ordered, in-memory Tracer for assertions. Each Begin allocates
// a unique span id so a span ended more than once is detected as a doubleEnd.
type recorder struct {
	mu        sync.Mutex
	events    []string
	open      map[int]bool
	next      int
	doubleEnd bool
}

func (r *recorder) Begin(ctx context.Context, op string) (context.Context, trace.EndFunc) {
	r.mu.Lock()
	if r.open == nil {
		r.open = make(map[int]bool)
	}
	id := r.next
	r.next++
	r.open[id] = true
	r.events = append(r.events, "begin:"+op)
	r.mu.Unlock()
	return ctx, func(error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.open[id] {
			r.doubleEnd = true
			return
		}
		r.open[id] = false
		r.events = append(r.events, "end:"+op)
	}
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type scriptedProvider struct {
	resp []message.Message
	i    int
}

func (p *scriptedProvider) Generate(context.Context, []message.Message, []tool.Info, ...model.Option) (*message.Message, error) {
	msg := p.resp[p.i]
	p.i++
	return &msg, nil
}

func (p *scriptedProvider) Stream(ctx context.Context, m []message.Message, t []tool.Info, o ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		msg, _ := p.Generate(ctx, m, t, o...)
		yield(model.TurnMessage{Message: *msg}, nil)
	}
}

type emptyInput struct{}

var errBoom = errors.New("boom")

func TestTraceSpansAreWellNested(t *testing.T) {
	alpha, err := tool.NewFunc("alpha", "alpha", func(context.Context, emptyInput) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewFunc: %v", err)
	}
	mode, err := agent.NewMode("default", "worker", agent.WithTools(alpha))
	if err != nil {
		t.Fatalf("NewMode: %v", err)
	}
	a, err := agent.New("worker", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New agent: %v", err)
	}

	prov := &scriptedProvider{resp: []message.Message{
		message.Assistant(message.NewToolUse("c1", "alpha", json.RawMessage(`{}`))),
		message.Assistant(message.NewText("done")),
	}}
	rec := &recorder{}
	r, err := runner.New(prov, runner.WithHooks(trace.Hooks(rec)))
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	if _, err := r.Run(context.Background(), a, runner.Text("go")); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := rec.snapshot()
	if !wellNested(events) {
		t.Fatalf("spans not well-nested: %v", events)
	}
	if begins, ends := count(events, "begin:"), count(events, "end:"); begins != ends || begins == 0 {
		t.Fatalf("unbalanced spans: begins=%d ends=%d", begins, ends)
	}
}

// TestTraceNoDoubleEndOnParallelToolError reproduces the path where a model
// call succeeds (AfterModel ends its span) and a subsequent tool fails under
// parallel execution. The core delivers OnError a context that carries modelKey
// but not toolKey, so OnError falls back to modelKey. Without idempotent ends
// that re-ends the already-closed model span; with once() it must be a no-op.
func TestTraceNoDoubleEndOnParallelToolError(t *testing.T) {
	ok, err := tool.NewFunc("alpha", "alpha", func(context.Context, emptyInput) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewFunc alpha: %v", err)
	}
	boom, err := tool.NewFunc("boom", "boom", func(context.Context, emptyInput) (string, error) {
		return "", errBoom
	})
	if err != nil {
		t.Fatalf("NewFunc boom: %v", err)
	}
	mode, err := agent.NewMode("default", "worker", agent.WithTools(ok, boom))
	if err != nil {
		t.Fatalf("NewMode: %v", err)
	}
	a, err := agent.New("worker", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New agent: %v", err)
	}

	prov := &scriptedProvider{resp: []message.Message{
		message.Assistant(
			message.NewToolUse("c1", "alpha", json.RawMessage(`{}`)),
			message.NewToolUse("c2", "boom", json.RawMessage(`{}`)),
		),
	}}
	rec := &recorder{}
	r, err := runner.New(prov, runner.WithMaxConcurrency(2), runner.WithHooks(trace.Hooks(rec)))
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	if _, err := r.Run(context.Background(), a, runner.Text("go")); err == nil {
		t.Fatalf("Run: expected tool error, got nil")
	}

	if rec.doubleEnd {
		t.Fatalf("model span ended more than once: %v", rec.snapshot())
	}
}

// wellNested checks that at no prefix do ends exceed begins.
func wellNested(events []string) bool {
	depth := 0
	for _, e := range events {
		switch {
		case strings.HasPrefix(e, "begin:"):
			depth++
		case strings.HasPrefix(e, "end:"):
			depth--
		}
		if depth < 0 {
			return false
		}
	}
	return depth == 0
}

func count(events []string, prefix string) int {
	n := 0
	for _, e := range events {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}
