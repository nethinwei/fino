package replay_test

import (
	"context"
	"encoding/json"
	"iter"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/replay"
)

// fakeProvider is a scripted provider used only to produce a recording.
type fakeProvider struct {
	resp []message.Message
	i    int
}

func (p *fakeProvider) Generate(context.Context, []message.Message, []tool.Info, ...model.Option) (*message.Message, error) {
	msg := p.resp[p.i]
	p.i++
	return &msg, nil
}

func (p *fakeProvider) Stream(ctx context.Context, m []message.Message, t []tool.Info, o ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		msg, _ := p.Generate(ctx, m, t, o...)
		yield(model.FinalMessage{Message: *msg}, nil)
	}
}

type searchInput struct {
	Query string `json:"query"`
}

func buildAgent(t *testing.T, search tool.Tool) *agent.Agent {
	t.Helper()
	mode, err := agent.NewMode("default", "search agent", agent.WithTools(search))
	if err != nil {
		t.Fatalf("NewMode: %v", err)
	}
	a, err := agent.New("searcher", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("New agent: %v", err)
	}
	return a
}

func script() []message.Message {
	return []message.Message{
		message.Assistant(message.NewToolUse("c1", "search", json.RawMessage(`{"query":"go"}`))),
		message.Assistant(message.NewText("done")),
	}
}

func TestRecordThenReplayReproducesRun(t *testing.T) {
	var realCalls atomic.Int64
	search, err := tool.NewFunc("search", "search the web", func(_ context.Context, in searchInput) (string, error) {
		realCalls.Add(1)
		return "RESULT:" + in.Query, nil
	})
	if err != nil {
		t.Fatalf("NewFunc: %v", err)
	}

	// Record.
	log := &replay.Log{}
	recModel := replay.RecordingModel{Next: &fakeProvider{resp: script()}, Log: log}
	recAgent := buildAgent(t, replay.RecordingTool(search, log))
	r1, err := runner.New(recModel)
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	rec, err := r1.Run(context.Background(), recAgent, runner.Text("find go"))
	if err != nil {
		t.Fatalf("record Run: %v", err)
	}
	if realCalls.Load() != 1 {
		t.Fatalf("real tool calls during record = %d, want 1", realCalls.Load())
	}

	// Persist and reload to prove the Log is serializable.
	data, err := log.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	log2, err := replay.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Replay: no provider, no real tool.
	repAgent := buildAgent(t, replay.ReplayTool("search", log2))
	r2, err := runner.New(&replay.ReplayModel{Log: log2})
	if err != nil {
		t.Fatalf("runner.New replay: %v", err)
	}
	rep, err := r2.Run(context.Background(), repAgent, runner.Text("find go"))
	if err != nil {
		t.Fatalf("replay Run: %v", err)
	}

	if realCalls.Load() != 1 {
		t.Fatalf("real tool was called during replay; calls = %d", realCalls.Load())
	}
	if !reflect.DeepEqual(rec.Messages, rep.Messages) {
		t.Fatalf("replay did not reproduce run\nrecord: %v\nreplay: %v", rec.Messages, rep.Messages)
	}
	if rep.Text() != "done" {
		t.Fatalf("replay final text = %q, want done", rep.Text())
	}
}
