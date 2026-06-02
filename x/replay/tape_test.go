package replay_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/replay"
)

// TestLogJSONRoundTripPreservesEvents pins spec test #6: a JSON round trip
// preserves Model, Tools, and the new Events tape.
func TestLogJSONRoundTripPreservesEvents(t *testing.T) {
	log := &replay.Log{
		Model: []message.Message{message.Assistant(message.NewText("hi"))},
		Tools: []replay.ToolRecord{{
			Name:   "search",
			Input:  json.RawMessage(`{"q":"go"}`),
			Result: tool.Result{Content: []message.Block{message.NewText("r")}},
		}},
		Events: []replay.Event{
			{Kind: replay.EventModelResponse, ModelResponse: &replay.ModelResponseEvent{
				Message: message.Assistant(message.NewText("hi")),
			}},
			{Kind: replay.EventTermination, Termination: &replay.TerminationEvent{
				Status:    replay.StatusCompleted,
				FinalText: "hi",
			}},
		},
	}

	data, err := log.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := replay.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !reflect.DeepEqual(log.Model, got.Model) {
		t.Errorf("Model not preserved\nwant: %v\ngot:  %v", log.Model, got.Model)
	}
	if !reflect.DeepEqual(log.Tools, got.Tools) {
		t.Errorf("Tools not preserved\nwant: %v\ngot:  %v", log.Tools, got.Tools)
	}
	if !reflect.DeepEqual(log.Events, got.Events) {
		t.Errorf("Events not preserved\nwant: %v\ngot:  %v", log.Events, got.Events)
	}
}

// TestUnmarshalLegacyFixtureWithoutEvents pins spec test #7: a fixture written
// before the tape existed (no "events" field) stays valid; Events is nil and
// Model/Tools load normally.
func TestUnmarshalLegacyFixtureWithoutEvents(t *testing.T) {
	legacy := []byte(`{"model":[{"role":"assistant","content":[{"type":"text","text":"done"}]}],"tools":[]}`)

	got, err := replay.Unmarshal(legacy)
	if err != nil {
		t.Fatalf("Unmarshal legacy: %v", err)
	}
	if got.Events != nil {
		t.Errorf("Events = %v, want nil for legacy fixture", got.Events)
	}
	if len(got.Model) != 1 || got.Model[0].Content[0].Text != "done" {
		t.Errorf("Model not loaded from legacy fixture: %v", got.Model)
	}
}
