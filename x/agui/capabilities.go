package agui

import "github.com/nethinwei/fino/message"

// Capabilities describes which AG-UI protocol features the configured Runtime
// can actually execute. A client should gate UI features on these flags rather
// than assuming all capabilities are available.
type Capabilities struct {
	// HasSuspendResume reports whether the Runtime has a SuspendStore, which is
	// required for interrupt/resume flows. False means suspend events will be
	// emitted but resume requests will be rejected with ErrResumeUnavailable.
	HasSuspendResume bool `json:"hasSuspendResume"`

	// Tools is the list of tool names available in the agent's default mode.
	// A client can use this to render an appropriate tool picker UI.
	Tools []string `json:"tools,omitempty"`

	// InputModalities lists the block types the underlying model accepts as
	// input (e.g. "text", "image"), when the model implements model.Capabilities.
	InputModalities []string `json:"inputModalities,omitempty"`

	// OutputModalities lists the block types the model can produce as output.
	OutputModalities []string `json:"outputModalities,omitempty"`
}

// Capabilities returns a snapshot of what this Runtime can execute based on its
// configured agent, runner, and store. It reports only actual capabilities, not
// what the protocol supports in general.
func (rt *Runtime) Capabilities() Capabilities {
	caps := Capabilities{
		HasSuspendResume: rt.store != nil,
	}
	defaultMode := rt.a.DefaultMode()
	if mode, ok := rt.a.Mode(defaultMode); ok {
		for _, t := range mode.Tools {
			caps.Tools = append(caps.Tools, t.Info().Name)
		}
	}
	if info, ok := rt.l.Runner().Capabilities(); ok {
		caps.InputModalities = toStrings(info.InputModalities)
		caps.OutputModalities = toStrings(info.OutputModalities)
	}
	return caps
}

// toStrings converts a slice of message.BlockType to strings, preserving order.
func toStrings(ts []message.BlockType) []string {
	if len(ts) == 0 {
		return nil
	}
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return out
}
