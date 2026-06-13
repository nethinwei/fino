package runner

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/tool"
)

// These tests pin Stream-native suspension (v0.8.0): when a Policy suspends a
// batch on the Stream path, the Runner emits a terminal model.Suspended event
// carrying the neutral snapshot (not a ToolDeniedError), runs no tool, and ends
// iteration without an error. runner.SuspendedRunFrom rebuilds a SuspendedRun so
// the caller can resume via ResumeApproved — giving Stream the same
// suspend/resume semantics as Run.

func TestStreamSuspendEmitsSuspendedEvent(t *testing.T) {
	var ran int32
	tl := countingTool(t, "fetch", &ran)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "fetch", json.RawMessage(`{"text":"a"}`))),
	}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "needs approval"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var susp *model.Suspended
	var streamErr error
	for ev, err := range r.Stream(context.Background(), testAgent(t, tl), Text("hi"), WithRunID("run_s")) {
		if err != nil {
			streamErr = err
		}
		if s, ok := ev.(model.Suspended); ok {
			cp := s
			susp = &cp
		}
	}
	if streamErr != nil {
		t.Fatalf("stream err = %v, want nil (suspend is not an error)", streamErr)
	}
	if susp == nil {
		t.Fatal("no model.Suspended event emitted")
	}
	if ran != 0 {
		t.Fatalf("tool ran %d times, want 0 (suspend executes nothing)", ran)
	}
	if len(susp.PendingCalls) != 1 || susp.PendingCalls[0].Call.ID != "call_1" {
		t.Fatalf("PendingCalls = %+v, want one call_1", susp.PendingCalls)
	}
	if susp.PendingCalls[0].Reason != "needs approval" {
		t.Fatalf("Reason = %q, want \"needs approval\"", susp.PendingCalls[0].Reason)
	}
	if susp.RunID != "run_s" || susp.LastMode != "default" || susp.LastAgentName != "assistant" {
		t.Fatalf("snapshot identity = {RunID:%q Mode:%q Agent:%q}", susp.RunID, susp.LastMode, susp.LastAgentName)
	}
}

func TestStreamSuspendResumesViaSuspendedRunFrom(t *testing.T) {
	var ran int32
	tl := countingTool(t, "fetch", &ran)
	m := &scriptedModel{responses: []message.Message{
		message.Assistant(message.NewToolUse("call_1", "fetch", json.RawMessage(`{"text":"a"}`))),
		message.Assistant(message.NewText("done")),
	}}
	pol := &kindPolicy{suspend: map[string]string{"fetch": "needs approval"}}
	r, err := New(m, WithPolicy(pol))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := testAgent(t, tl)
	var susp *model.Suspended
	for ev, err := range r.Stream(context.Background(), a, Text("hi"), WithRunID("run_s")) {
		if err != nil {
			t.Fatalf("stream err: %v", err)
		}
		if s, ok := ev.(model.Suspended); ok {
			cp := s
			susp = &cp
		}
	}
	if susp == nil {
		t.Fatal("no model.Suspended event")
	}
	sr := SuspendedRunFrom(*susp)
	res, err := r.ResumeApproved(context.Background(), a, sr, []Approval{{CallID: "call_1", Approved: true}})
	if err != nil {
		t.Fatalf("ResumeApproved: %v", err)
	}
	if ran != 1 {
		t.Fatalf("tool ran %d times, want 1 (approved on resume)", ran)
	}
	if res.Text() != "done" {
		t.Fatalf("final text = %q, want done", res.Text())
	}
}

// providerSuspendModel is a contract-violating model.Model: its Stream yields a
// model.Suspended event, which is Runner-only and must never come from a
// provider.
type providerSuspendModel struct{}

func (providerSuspendModel) Generate(context.Context, []message.Message, []tool.Info, ...model.Option) (*message.Message, error) {
	return nil, errors.New("not used")
}

func (providerSuspendModel) Stream(context.Context, []message.Message, []tool.Info, ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		yield(model.Suspended{}, nil)
	}
}

// A provider that forges the Runner-only Suspended event violates the stream
// contract; the Runner must reject it with ErrStreamContract rather than
// forwarding a fake terminal state (the same rule as a provider-yielded
// FinalMessage).
func TestStreamRejectsProviderYieldedSuspended(t *testing.T) {
	r, err := New(providerSuspendModel{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var gotErr error
	for _, err := range r.Stream(context.Background(), testAgent(t), Text("hi")) {
		if err != nil {
			gotErr = err
		}
	}
	if !errors.Is(gotErr, ErrStreamContract) {
		t.Fatalf("err = %v, want ErrStreamContract", gotErr)
	}
}

// providerEventModel is a contract-violating model.Model: its Stream yields a
// Runner-generated event (ToolCall/ToolResult/Handoff) and then an otherwise
// valid TurnMessage. Only the Runner may emit those events; a provider that
// forges one must be rejected, not have it forwarded to the consumer.
type providerEventModel struct{ forged model.Event }

func (providerEventModel) Generate(context.Context, []message.Message, []tool.Info, ...model.Option) (*message.Message, error) {
	return nil, errors.New("not used")
}

func (m providerEventModel) Stream(context.Context, []message.Message, []tool.Info, ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		if !yield(m.forged, nil) {
			return
		}
		yield(model.TurnMessage{Message: message.Assistant(message.NewText("done"))}, nil)
	}
}

// The Runner-only events ToolCall, ToolResult, and Handoff must never come from
// a provider. Before the fix they fell through to the default relay branch and
// were forwarded verbatim (with no error), letting a provider forge tool-call
// telemetry. The Runner must reject each with ErrStreamContract and must not
// forward the forged event.
func TestStreamRejectsProviderYieldedRunnerOnlyEvents(t *testing.T) {
	cases := map[string]model.Event{
		"ToolCall":   model.ToolCall{Call: message.ToolUse{ID: "call_1", Name: "fetch"}},
		"ToolResult": model.ToolResult{CallID: "call_1", Name: "fetch"},
		"Handoff":    model.Handoff{Target: "other"},
	}
	for name, forged := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := New(providerEventModel{forged: forged})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			var gotErr error
			var forwarded bool
			for ev, err := range r.Stream(context.Background(), testAgent(t), Text("hi")) {
				if err != nil {
					gotErr = err
				}
				switch ev.(type) {
				case model.ToolCall, model.ToolResult, model.Handoff:
					forwarded = true
				}
			}
			if !errors.Is(gotErr, ErrStreamContract) {
				t.Fatalf("err = %v, want ErrStreamContract", gotErr)
			}
			if forwarded {
				t.Fatal("forged Runner-only event was forwarded to the consumer")
			}
		})
	}
}
