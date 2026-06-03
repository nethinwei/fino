package agui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
)

func TestHandlerSetsSSEHeaders(t *testing.T) {
	m := &streamModel{events: []model.Event{
		model.TurnMessage{Message: message.Assistant(message.NewText("hi"))},
	}}
	rt := makeRuntime(t, m, makeAgent(t))

	body, _ := json.Marshal(RunAgentInput{ThreadID: "t1", RunID: "r1"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	Handler(rt).ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
}

func TestHandlerEmitsRunStartedFirst(t *testing.T) {
	m := &streamModel{events: []model.Event{
		model.TurnMessage{Message: message.Assistant(message.NewText("hi"))},
	}}
	rt := makeRuntime(t, m, makeAgent(t))

	body, _ := json.Marshal(RunAgentInput{ThreadID: "t1", RunID: "r1"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	Handler(rt).ServeHTTP(rec, req)

	frames := parseSSEFrames(rec.Body.String())
	if len(frames) == 0 {
		t.Fatal("no SSE frames")
	}
	var first BaseEvent
	if err := json.Unmarshal([]byte(frames[0]), &first); err != nil {
		t.Fatalf("unmarshal first frame: %v", err)
	}
	if first.Type != EventRunStarted {
		t.Fatalf("first event type = %q, want RUN_STARTED", first.Type)
	}
}

func TestHandlerEmitsRunFinishedLast(t *testing.T) {
	m := &streamModel{events: []model.Event{
		model.TurnMessage{Message: message.Assistant(message.NewText("hi"))},
	}}
	rt := makeRuntime(t, m, makeAgent(t))

	body, _ := json.Marshal(RunAgentInput{ThreadID: "t1", RunID: "r1"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	Handler(rt).ServeHTTP(rec, req)

	frames := parseSSEFrames(rec.Body.String())
	if len(frames) == 0 {
		t.Fatal("no SSE frames")
	}
	var last BaseEvent
	if err := json.Unmarshal([]byte(frames[len(frames)-1]), &last); err != nil {
		t.Fatalf("unmarshal last frame: %v", err)
	}
	if last.Type != EventRunFinished {
		t.Fatalf("last event type = %q, want RUN_FINISHED", last.Type)
	}
}

func TestHandlerReturnsBadRequestOnInvalidBody(t *testing.T) {
	m := &streamModel{events: []model.Event{
		model.TurnMessage{Message: message.Assistant(message.NewText("hi"))},
	}}
	rt := makeRuntime(t, m, makeAgent(t))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json"))
	rec := httptest.NewRecorder()

	Handler(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlerFramesAreValidJSON(t *testing.T) {
	m := &streamModel{events: []model.Event{
		model.TextDelta{Text: "hello"},
		model.TurnMessage{Message: message.Assistant(message.NewText("hello"))},
	}}
	rt := makeRuntime(t, m, makeAgent(t))

	body, _ := json.Marshal(RunAgentInput{ThreadID: "t1", RunID: "r1"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	Handler(rt).ServeHTTP(rec, req)

	for i, frame := range parseSSEFrames(rec.Body.String()) {
		if !json.Valid([]byte(frame)) {
			t.Fatalf("frame[%d] is not valid JSON: %s", i, frame)
		}
	}
}

// parseSSEFrames extracts the JSON payload from each "data: ..." SSE frame.
func parseSSEFrames(body string) []string {
	var frames []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if payload, ok := strings.CutPrefix(line, "data: "); ok {
			frames = append(frames, payload)
		}
	}
	return frames
}
