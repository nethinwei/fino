// Package recover provides durable continuation for fino agent runs.
//
// It is a reference composition for the sufficiency thesis in docs/design.md:
// it adds no core capability and lives outside the core packages. A Snapshot
// serializes only the three things invariant I10 (续跑完备) requires — the run
// history and the active mode — and recovery is just re-running the agent over
// that history. No graph state or checkpoint type is introduced.
//
// Recovery contract: a Snapshot must end at a safe boundary (a user or tool
// message, or an assistant text message), not a dangling assistant tool_use.
// See docs/spec/loop-semantics.md §7.1; mid-batch / human-in-the-loop resume is
// out of scope here and would require a minimal core seam.
package recover

import (
	"context"
	"encoding/json"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/runner"
)

// Snapshot is the complete, serializable state needed to resume a run. It holds
// only the run history and the active mode name.
type Snapshot struct {
	History []message.Message `json:"history"`
	Mode    string            `json:"mode"`
}

// Capture builds a Snapshot from a completed run Result.
func Capture(res *runner.Result) Snapshot {
	return Snapshot{
		History: append([]message.Message(nil), res.Messages...),
		Mode:    res.LastMode,
	}
}

// FromHistory builds a Snapshot from an explicit history and mode, for callers
// that persist intermediate state themselves (e.g. after a crash).
func FromHistory(history []message.Message, mode string) Snapshot {
	return Snapshot{
		History: append([]message.Message(nil), history...),
		Mode:    mode,
	}
}

// Marshal serializes the Snapshot to JSON.
func (s Snapshot) Marshal() ([]byte, error) { return json.Marshal(s) }

// Load parses a Snapshot from JSON produced by Marshal.
func Load(data []byte) (Snapshot, error) {
	var s Snapshot
	err := json.Unmarshal(data, &s)
	return s, err
}

// Resume continues a run from the snapshot by re-running the agent over the
// snapshot history. The snapshot's mode is used unless opts contain an explicit
// WithMode that overrides it (opts are appended after the snapshot's mode
// selection, so a later WithMode wins). Resume composes existing primitives only.
func (s Snapshot) Resume(ctx context.Context, r *runner.Runner, a *agent.Agent, opts ...runner.RunOption) (*runner.Result, error) {
	runOpts := append([]runner.RunOption{runner.WithMode(s.Mode)}, opts...)
	return r.Run(ctx, a, runner.Messages(s.History), runOpts...)
}
