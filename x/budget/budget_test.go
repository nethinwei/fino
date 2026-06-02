package budget_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"sync/atomic"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/budget"
)

// loopingProvider always asks to call the noop tool, forcing more turns until
// some outer condition (the budget) stops the run.
type loopingProvider struct{}

func (loopingProvider) Generate(context.Context, []message.Message, []tool.Info, ...model.Option) (*message.Message, error) {
	msg := message.Assistant(message.NewToolUse("c", "noop", json.RawMessage(`{}`)))
	return &msg, nil
}

func (loopingProvider) Stream(ctx context.Context, m []message.Message, t []tool.Info, o ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		msg, _ := loopingProvider{}.Generate(ctx, m, t, o...)
		yield(model.TurnMessage{Message: *msg}, nil)
	}
}

type emptyInput struct{}

func TestBudgetStopsRunAndFiresOnErrorOnce(t *testing.T) {
	noop, err := tool.NewFunc("noop", "noop", func(context.Context, emptyInput) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewFunc: %v", err)
	}
	mode, err := agent.NewMode("default", "looper", agent.WithTools(noop))
	if err != nil {
		t.Fatalf("NewMode: %v", err)
	}
	a, err := agent.New("looper", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New agent: %v", err)
	}

	// Cost 10 per response, limit 25: turns at used 0,10,20 proceed; at 30 the
	// next call is blocked. So the budget trips on the 4th model call.
	bm := budget.New(loopingProvider{}, 25, func(*message.Message) int { return 10 })

	var onErr atomic.Int64
	h := &hooks.Hooks{OnError: func(context.Context, error) { onErr.Add(1) }}
	r, err := runner.New(bm, runner.WithHooks(h), runner.WithMaxTurns(50))
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}

	res, err := r.Run(context.Background(), a, runner.Text("go"))
	if res != nil || !errors.Is(err, budget.ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got res=%v err=%v", res, err)
	}
	if onErr.Load() != 1 {
		t.Fatalf("OnError fired %d times, want 1", onErr.Load())
	}
	if bm.Used() < 25 {
		t.Fatalf("used = %d, want >= 25 before tripping", bm.Used())
	}
}
