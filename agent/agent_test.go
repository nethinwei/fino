package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/tool"
)

type namedTool struct{ name string }

func (t namedTool) Info() tool.Info { return tool.Info{Name: t.name, Description: t.name} }
func (t namedTool) Run(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, nil
}

func TestNewModeWithToolsAndModelOptions(t *testing.T) {
	mode, err := NewMode("code", "write code", WithTools(namedTool{"read"}), WithModelOptions(model.WithTemperature(0)))
	if err != nil {
		t.Fatalf("NewMode error: %v", err)
	}
	if mode.Name != "code" || len(mode.Tools) != 1 || len(mode.ModelOptions) != 1 {
		t.Fatalf("mode = %#v", mode)
	}
}

func TestNewModeRejectsDuplicateToolNames(t *testing.T) {
	_, err := NewMode("code", "write code", WithTools(namedTool{"read"}, namedTool{"read"}))
	if err == nil {
		t.Fatal("expected duplicate tool error")
	}
}

func TestNewRejectsDuplicateModes(t *testing.T) {
	first, err := NewMode("code", "first")
	if err != nil {
		t.Fatalf("NewMode first error: %v", err)
	}
	second, err := NewMode("code", "second")
	if err != nil {
		t.Fatalf("NewMode second error: %v", err)
	}
	_, err = New("coder", WithMode(first), WithMode(second), WithDefaultMode("code"))
	if err == nil {
		t.Fatal("expected duplicate mode error")
	}
}

func TestNewRejectsMissingDefaultMode(t *testing.T) {
	mode, err := NewMode("code", "write code")
	if err != nil {
		t.Fatalf("NewMode error: %v", err)
	}
	_, err = New("coder", WithMode(mode), WithDefaultMode("plan"))
	if err == nil {
		t.Fatal("expected missing default mode error")
	}
}

func TestNewHandoffToolExposesTargetAgent(t *testing.T) {
	mode, err := NewMode("code", "write code")
	if err != nil {
		t.Fatalf("NewMode error: %v", err)
	}
	target, err := New("target", WithMode(mode), WithDefaultMode("code"))
	if err != nil {
		t.Fatalf("New target error: %v", err)
	}
	h, err := NewHandoffTool(target)
	if err != nil {
		t.Fatalf("NewHandoffTool error: %v", err)
	}
	handoff, ok := h.(HandoffTool)
	if !ok {
		t.Fatalf("handoff tool does not implement HandoffTool")
	}
	if handoff.TargetAgent() != target {
		t.Fatal("wrong target agent")
	}
}
