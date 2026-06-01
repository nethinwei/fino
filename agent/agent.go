// Package agent defines Agent, Mode, and HandoffTool for the fino Agent SDK.
// An Agent is a model-agnostic behavior configuration; Mode defines a runnable
// persona with instructions, tools, and model options.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/tool"
)

// Mode is a runnable persona: a set of instructions, tools, and optional model
// options. Modes are selected at run time rather than by modifying Agent state.
type Mode struct {
	Name         string
	Instructions string
	Tools        []tool.Tool
	ModelOptions []model.Option
	Metadata     map[string]any
}

// ModeOption configures a Mode.
type ModeOption func(*Mode)

// NewMode creates a Mode with the given name and instructions. It validates
// that the name is non-empty, no tool is nil, and tool names are unique within
// the mode.
func NewMode(name, instructions string, opts ...ModeOption) (Mode, error) {
	if strings.TrimSpace(name) == "" {
		return Mode{}, errors.New("mode name is required")
	}
	m := Mode{Name: name, Instructions: instructions}
	for _, opt := range opts {
		if opt != nil {
			opt(&m)
		}
	}
	seen := map[string]bool{}
	for _, t := range m.Tools {
		if t == nil {
			return Mode{}, errors.New("nil tool in mode")
		}
		n := t.Info().Name
		if seen[n] {
			return Mode{}, fmt.Errorf("duplicate tool name %q in mode %q", n, name)
		}
		seen[n] = true
	}
	return m, nil
}

// WithTools adds tools to the mode.
func WithTools(tools ...tool.Tool) ModeOption {
	return func(m *Mode) { m.Tools = append(m.Tools, tools...) }
}

// WithModelOptions sets default model options for the mode. These are applied
// before any per-run overrides.
func WithModelOptions(opts ...model.Option) ModeOption {
	return func(m *Mode) { m.ModelOptions = append(m.ModelOptions, opts...) }
}

// WithMetadata attaches a key-value pair to the mode's metadata.
func WithMetadata(key string, value any) ModeOption {
	return func(m *Mode) {
		if m.Metadata == nil {
			m.Metadata = map[string]any{}
		}
		m.Metadata[key] = value
	}
}

// Agent is a model-agnostic behavior configuration. It groups one or more
// Modes and delegates execution to the Runner.
type Agent struct {
	name        string
	defaultMode string
	modes       map[string]Mode
	modeList    []Mode
}

// AgentOption configures an Agent.
type AgentOption func(*Agent)

// New creates an Agent with the given name and options. It validates that the
// name is non-empty, at least one mode is provided, mode names are unique, and
// the default mode exists.
func New(name string, opts ...AgentOption) (*Agent, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("agent name is required")
	}
	a := &Agent{name: name, modes: map[string]Mode{}}
	for _, opt := range opts {
		if opt != nil {
			opt(a)
		}
	}
	modes := map[string]Mode{}
	for _, m := range a.modeList {
		if _, exists := modes[m.Name]; exists {
			return nil, fmt.Errorf("duplicate mode %q", m.Name)
		}
		modes[m.Name] = m
	}
	a.modes = modes
	a.modeList = nil
	if len(a.modes) == 0 {
		return nil, errors.New("agent requires at least one mode")
	}
	if a.defaultMode == "" {
		return nil, errors.New("agent default mode is required")
	}
	if _, ok := a.modes[a.defaultMode]; !ok {
		return nil, fmt.Errorf("default mode %q not found", a.defaultMode)
	}
	return a, nil
}

// WithMode adds a Mode to the Agent.
func WithMode(mode Mode) AgentOption {
	return func(a *Agent) { a.modeList = append(a.modeList, mode) }
}

// WithDefaultMode sets the default mode name for the Agent.
func WithDefaultMode(name string) AgentOption {
	return func(a *Agent) { a.defaultMode = name }
}

// Name returns the Agent's name.
func (a *Agent) Name() string { return a.name }

// DefaultMode returns the name of the Agent's default mode.
func (a *Agent) DefaultMode() string { return a.defaultMode }

// Mode returns the Mode with the given name. The second return value reports
// whether the mode exists.
func (a *Agent) Mode(name string) (Mode, bool) {
	m, ok := a.modes[name]
	return m, ok
}

// HandoffTool is a Tool that signals an Agent transfer. Runner detects
// handoffs by type-asserting tools to this interface.
type HandoffTool interface {
	tool.Tool
	// TargetAgent returns the Agent to transfer control to.
	TargetAgent() *Agent
}

type handoffTool struct {
	target *Agent
	info   tool.Info
}

// NewHandoffTool creates a tool that signals a transfer to the target Agent.
// The returned Tool also satisfies the HandoffTool interface, which Runner
// uses to detect and execute the handoff.
func NewHandoffTool(target *Agent) (tool.Tool, error) {
	if target == nil {
		return nil, errors.New("target agent is required")
	}
	name := "handoff_to_" + target.Name()
	return &handoffTool{
		target: target,
		info: tool.Info{
			Name:        name,
			Description: "Transfer control to agent " + target.Name(),
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
	}, nil
}

func (h *handoffTool) Info() tool.Info { return h.info }

func (h *handoffTool) Run(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: []message.Block{message.NewText(h.target.Name())}}, nil
}

func (h *handoffTool) TargetAgent() *Agent { return h.target }
