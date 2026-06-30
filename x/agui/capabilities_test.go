package agui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/react"
)

func TestCapabilitiesReflectActualRuntime(t *testing.T) {
	calc, err := tool.NewFunc("calc", "add numbers",
		func(_ context.Context, _ struct{}) (tool.Result, error) {
			return tool.Result{}, nil
		},
	)
	if err != nil {
		t.Fatalf("tool.NewFunc: %v", err)
	}
	mode, err := agent.NewMode("default", "you are helpful", agent.WithTools(calc))
	if err != nil {
		t.Fatalf("NewMode: %v", err)
	}
	a, err := agent.New("myagent", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	m := &streamModel{events: nil} // no events needed for capabilities
	r, err := runner.New(m)
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	l, err := react.New(r)
	if err != nil {
		t.Fatalf("react.New: %v", err)
	}

	// Without a SuspendStore, HasSuspendResume must be false.
	rt, err := NewRuntime(l, a)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	caps := rt.Capabilities()
	if caps.HasSuspendResume {
		t.Fatal("HasSuspendResume should be false without a store")
	}
	if len(caps.Tools) != 1 || caps.Tools[0] != "calc" {
		t.Fatalf("caps.Tools = %v, want [calc]", caps.Tools)
	}

	// With a SuspendStore, HasSuspendResume must be true.
	rt2, err := NewRuntime(l, a, WithSuspendStore(NewInMemorySuspendStore()))
	if err != nil {
		t.Fatalf("NewRuntime with store: %v", err)
	}
	caps2 := rt2.Capabilities()
	if !caps2.HasSuspendResume {
		t.Fatal("HasSuspendResume should be true with a store")
	}
}

func TestCapabilitiesJSONShape(t *testing.T) {
	caps := Capabilities{
		HasSuspendResume: true,
		Tools:            []string{"search", "write_file"},
	}
	data, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"hasSuspendResume":true`,
		`"tools":["search","write_file"]`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}
