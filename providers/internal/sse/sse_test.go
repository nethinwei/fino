package sse

import (
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/fino/model"
)

// collect drains Stream into a slice, recording the terminal error if any.
func collect(body string, handle Handler, final func() model.Event) ([]model.Event, error) {
	var events []model.Event
	var gotErr error
	for ev, err := range Stream(strings.NewReader(body), handle, final) {
		events = append(events, ev)
		if err != nil {
			gotErr = err
		}
	}
	return events, gotErr
}

func TestStreamEmitsEventsThenFinalAndStopsOnDone(t *testing.T) {
	body := "data: a\n" +
		"ignored: line\n" + // non-data line is skipped
		"event: x\n" + // event: line is skipped
		"data: b\n" +
		"data: [DONE]\n" +
		"data: c\n" // after DONE, must not be processed
	handle := func(p string) ([]model.Event, bool, error) {
		if p == "[DONE]" {
			return nil, true, nil
		}
		return []model.Event{model.TextDelta{Text: p}}, false, nil
	}
	final := func() model.Event { return model.TurnMessage{} }

	events, err := collect(body, handle, final)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(events), events)
	}
	if d, ok := events[0].(model.TextDelta); !ok || d.Text != "a" {
		t.Fatalf("event[0] = %+v, want TextDelta a", events[0])
	}
	if d, ok := events[1].(model.TextDelta); !ok || d.Text != "b" {
		t.Fatalf("event[1] = %+v, want TextDelta b", events[1])
	}
	if _, ok := events[2].(model.TurnMessage); !ok {
		t.Fatalf("event[2] = %+v, want TurnMessage", events[2])
	}
}

func TestStreamHandleErrorYieldsStreamError(t *testing.T) {
	wantErr := errors.New("boom")
	finalCalled := false
	handle := func(p string) ([]model.Event, bool, error) {
		if p == "boom" {
			return nil, false, wantErr
		}
		return []model.Event{model.TextDelta{Text: p}}, false, nil
	}
	final := func() model.Event { finalCalled = true; return model.TurnMessage{} }

	events, err := collect("data: ok\ndata: boom\ndata: after\n", handle, final)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if finalCalled {
		t.Fatal("final must not be called after an error")
	}
	last := events[len(events)-1]
	if se, ok := last.(model.StreamError); !ok || !errors.Is(se.Err, wantErr) {
		t.Fatalf("last event = %+v, want StreamError(%v)", last, wantErr)
	}
}

func TestStreamEmptyBodyYieldsOnlyFinal(t *testing.T) {
	handle := func(p string) ([]model.Event, bool, error) {
		t.Fatalf("handle called for empty body, payload %q", p)
		return nil, false, nil
	}
	final := func() model.Event { return model.TurnMessage{} }

	events, err := collect("", handle, final)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	if _, ok := events[0].(model.TurnMessage); !ok {
		t.Fatalf("event[0] = %+v, want TurnMessage", events[0])
	}
}

func TestStreamScannerErrorYieldsStreamError(t *testing.T) {
	// A line exceeding the scan buffer makes bufio.Scanner fail with
	// ErrTooLong, which must surface as a terminal StreamError, not final.
	body := "data: " + strings.Repeat("x", scanBufferMax+1)
	finalCalled := false
	handle := func(p string) ([]model.Event, bool, error) { return nil, false, nil }
	final := func() model.Event { finalCalled = true; return model.TurnMessage{} }

	events, err := collect(body, handle, final)
	if err == nil {
		t.Fatal("err = nil, want scanner error")
	}
	if finalCalled {
		t.Fatal("final must not be called after a scanner error")
	}
	last := events[len(events)-1]
	if se, ok := last.(model.StreamError); !ok || se.Err == nil {
		t.Fatalf("last event = %+v, want StreamError", last)
	}
}

func TestStreamStopsWhenConsumerStops(t *testing.T) {
	attempts := 0
	handle := func(p string) ([]model.Event, bool, error) {
		attempts++
		return []model.Event{model.TextDelta{Text: p}}, false, nil
	}
	final := func() model.Event { t.Fatal("final must not run after consumer stops"); return nil }

	count := 0
	for range Stream(strings.NewReader("data: a\ndata: b\n"), handle, final) {
		count++
		break
	}
	if count != 1 {
		t.Fatalf("consumed %d events, want 1", count)
	}
	if attempts != 1 {
		t.Fatalf("handle called %d times, want 1", attempts)
	}
}
