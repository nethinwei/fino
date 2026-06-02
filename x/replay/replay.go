// Package replay provides record-and-replay for fino agent runs.
//
// It is a reference composition for the sufficiency thesis in docs/design.md:
// it adds no core capability and lives outside the core packages. Because the
// only sources of non-determinism in a run flow through model.Model and
// tool.Tool, recording those two seams is enough to reproduce a run exactly.
//
// Record a run by wrapping its model with RecordingModel and its tools with
// RecordingTool sharing one *Log. Persist the Log as JSON. Replay by driving an
// identical agent with ReplayModel and ReplayTool built from the Log; no
// provider or real tool is ever called.
package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sync"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/tool"
)

// emptyObjSchema is the input schema reported by replay tools.
var emptyObjSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)

// ToolRecord is one recorded tool execution. A run replays a tool by matching
// on Name and Input.
type ToolRecord struct {
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input,omitempty"`
	Result tool.Result     `json:"result"`
	Err    string          `json:"err,omitempty"`
}

// Log is the ordered record of a run: model responses in call order and tool
// executions. It is JSON-serializable. Recording is concurrency-safe so tools
// running in parallel can record without races.
type Log struct {
	mu    sync.Mutex
	Model []message.Message `json:"model"`
	Tools []ToolRecord      `json:"tools"`
}

func (l *Log) recordModel(m message.Message) {
	l.mu.Lock()
	l.Model = append(l.Model, m)
	l.mu.Unlock()
}

func (l *Log) recordTool(r ToolRecord) {
	l.mu.Lock()
	l.Tools = append(l.Tools, r)
	l.mu.Unlock()
}

// lookup returns the first recorded tool result matching name and input.
// Replay presumes tools are deterministic: identical inputs produce identical
// results, so "first match" is sufficient. A non-deterministic tool that
// recorded different results for the same input would always replay the first.
func (l *Log) lookup(name string, input json.RawMessage) (ToolRecord, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, rec := range l.Tools {
		if rec.Name == name && string(rec.Input) == string(input) {
			return rec, true
		}
	}
	return ToolRecord{}, false
}

// RecordingModel wraps a Model, recording every response into Log while
// forwarding calls to Next unchanged.
type RecordingModel struct {
	Next model.Model
	Log  *Log
}

// Generate forwards to Next and records the response.
func (m RecordingModel) Generate(ctx context.Context, msgs []message.Message, tools []tool.Info, opts ...model.Option) (*message.Message, error) {
	out, err := m.Next.Generate(ctx, msgs, tools, opts...)
	if err != nil {
		return nil, err
	}
	m.Log.recordModel(*out)
	return out, nil
}

// Stream forwards events from Next and records the turn's TurnMessage.
func (m RecordingModel) Stream(ctx context.Context, msgs []message.Message, tools []tool.Info, opts ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		for ev, err := range m.Next.Stream(ctx, msgs, tools, opts...) {
			if err != nil {
				yield(ev, err)
				return
			}
			if tm, ok := ev.(model.TurnMessage); ok {
				m.Log.recordModel(tm.Message)
			}
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// ReplayModel serves recorded model responses in order and calls no provider.
// Use a pointer; it advances an internal cursor across turns.
type ReplayModel struct {
	Log *Log
	mu  sync.Mutex
	i   int
}

func (m *ReplayModel) next() (message.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.i >= len(m.Log.Model) {
		return message.Message{}, errors.New("replay: no more recorded model responses")
	}
	msg := m.Log.Model[m.i]
	m.i++
	return msg, nil
}

// Generate returns the next recorded response.
func (m *ReplayModel) Generate(context.Context, []message.Message, []tool.Info, ...model.Option) (*message.Message, error) {
	msg, err := m.next()
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// Stream yields the next recorded response as a single TurnMessage, honoring the
// model.Model stream contract.
func (m *ReplayModel) Stream(context.Context, []message.Message, []tool.Info, ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		msg, err := m.next()
		if err != nil {
			yield(model.StreamError{Err: err}, err)
			return
		}
		yield(model.TurnMessage{Message: msg}, nil)
	}
}

type recordingTool struct {
	next tool.Tool
	log  *Log
}

// RecordingTool wraps a tool, recording its result while forwarding execution.
func RecordingTool(t tool.Tool, log *Log) tool.Tool {
	return recordingTool{next: t, log: log}
}

func (t recordingTool) Info() tool.Info { return t.next.Info() }

func (t recordingTool) Run(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	out, err := t.next.Run(ctx, input)
	rec := ToolRecord{Name: t.next.Info().Name, Input: input, Result: out}
	if err != nil {
		rec.Err = err.Error()
	}
	t.log.recordTool(rec)
	return out, err
}

type replayTool struct {
	name string
	log  *Log
}

// ReplayTool returns a tool that serves the recorded result for the named tool,
// matched by input. It never performs real work.
func ReplayTool(name string, log *Log) tool.Tool {
	return replayTool{name: name, log: log}
}

func (t replayTool) Info() tool.Info {
	return tool.Info{Name: t.name, Description: "replay of " + t.name, InputSchema: emptyObjSchema}
}

func (t replayTool) Run(_ context.Context, input json.RawMessage) (tool.Result, error) {
	rec, ok := t.log.lookup(t.name, input)
	if !ok {
		return tool.Result{}, fmt.Errorf("replay: no recorded result for tool %q with input %s", t.name, string(input))
	}
	if rec.Err != "" {
		return rec.Result, errors.New(rec.Err)
	}
	return rec.Result, nil
}

// Marshal serializes the Log to JSON.
func (l *Log) Marshal() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return json.Marshal(struct {
		Model []message.Message `json:"model"`
		Tools []ToolRecord      `json:"tools"`
	}{Model: l.Model, Tools: l.Tools})
}

// Unmarshal parses a Log from JSON produced by Marshal.
func Unmarshal(data []byte) (*Log, error) {
	var raw struct {
		Model []message.Message `json:"model"`
		Tools []ToolRecord      `json:"tools"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return &Log{Model: raw.Model, Tools: raw.Tools}, nil
}
