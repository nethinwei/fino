package recover_test

import (
	"context"
	"encoding/json"
	"iter"
	"reflect"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/recover"
)

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
		yield(model.FinalMessage{Message: *msg}, nil)
	}
}

type emptyInput struct{}

func buildAgent(t *testing.T) *agent.Agent {
	t.Helper()
	alpha, err := tool.NewFunc("alpha", "alpha tool", func(context.Context, emptyInput) (string, error) {
		return "ok:alpha", nil
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
	return a
}

// TestCrashRecoveryAtSafeBoundary proves I10: a run interrupted after a tool
// result (a safe boundary) resumes from the persisted history to the same final
// state as an uninterrupted run, with no checkpoint type and no core change.
func TestCrashRecoveryAtSafeBoundary(t *testing.T) {
	// Uninterrupted reference run.
	full := &scriptedProvider{resp: []message.Message{
		message.Assistant(message.NewToolUse("c1", "alpha", json.RawMessage(`{}`))),
		message.Assistant(message.NewText("done")),
	}}
	rFull, err := runner.New(full)
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	want, err := rFull.Run(context.Background(), buildAgent(t), runner.Text("go"))
	if err != nil {
		t.Fatalf("full Run: %v", err)
	}

	// Persisted state captured at a safe boundary: after the tool result, before
	// the next model call (a realistic crash point).
	persisted := []message.Message{
		message.UserText("go"),
		message.Assistant(message.NewToolUse("c1", "alpha", json.RawMessage(`{}`))),
		message.ToolResults(message.NewToolResult("c1", "alpha", []message.Block{message.NewText("ok:alpha")}, false)),
	}
	snap := recover.FromHistory(persisted, "default")

	// Round-trip the snapshot through JSON to prove it is serializable.
	data, err := snap.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	snap2, err := recover.Load(data)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Resume: only the remaining model turn is left.
	resume := &scriptedProvider{resp: []message.Message{
		message.Assistant(message.NewText("done")),
	}}
	rResume, err := runner.New(resume)
	if err != nil {
		t.Fatalf("runner.New resume: %v", err)
	}
	got, err := snap2.Resume(context.Background(), rResume, buildAgent(t))
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	if !reflect.DeepEqual(got.Messages, want.Messages) {
		t.Fatalf("resume did not match uninterrupted run\n got: %v\nwant: %v", got.Messages, want.Messages)
	}
}

// TestSnapshotHoldsOnlyHistoryAndMode guards the boundary: Snapshot must not
// grow into a checkpoint/graph type. It has exactly two fields.
func TestSnapshotHoldsOnlyHistoryAndMode(t *testing.T) {
	v := reflect.TypeOf(recover.Snapshot{})
	if v.NumField() != 2 {
		t.Fatalf("Snapshot has %d fields; recovery must hold only history and mode", v.NumField())
	}
}
