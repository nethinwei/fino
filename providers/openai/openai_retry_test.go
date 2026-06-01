package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
)

// TestStreamEmitsReasoning verifies that reasoning_content surfaces as a
// thinking ContentBlockDelta ahead of the answer text, and is preserved in the
// final message.
func TestStreamEmitsReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	m, _ := New("deepseek-v4-flash", WithBaseURL(srv.URL), WithAPIKey("k"))

	var kinds []string
	var thinking, text string
	var final *message.Message
	for ev, err := range m.Stream(context.Background(), []message.Message{message.UserText("hi")}, nil) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		switch e := ev.(type) {
		case model.ContentBlockDelta:
			if e.Block.Type == message.TypeThinking {
				kinds = append(kinds, "thinking")
				thinking += e.Block.Text
			}
		case model.TextDelta:
			kinds = append(kinds, "text")
			text += e.Text
		case model.FinalMessage:
			fm := e.Message
			final = &fm
		}
	}
	if thinking != "think" || text != "answer" {
		t.Fatalf("thinking=%q text=%q, want think/answer", thinking, text)
	}
	if len(kinds) < 2 || kinds[0] != "thinking" || kinds[1] != "text" {
		t.Fatalf("event order = %v, want thinking before text", kinds)
	}
	if final == nil || len(final.Content) != 2 ||
		final.Content[0].Type != message.TypeThinking || final.Content[0].Text != "think" ||
		final.Content[1].Type != message.TypeText || final.Content[1].Text != "answer" {
		t.Fatalf("final message = %+v", final)
	}
}

func TestPostRetriesOnServerError(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "busy")
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()
	m, _ := New("x", WithBaseURL(srv.URL), WithAPIKey("k"))
	m.retryBackoff = time.Millisecond

	msg, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if msg.Text() != "ok" {
		t.Fatalf("text = %q, want ok", msg.Text())
	}
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("requests = %d, want 2 (one retry)", got)
	}
}

func TestPostDoesNotRetryClientError(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "nope")
	}))
	defer srv.Close()
	m, _ := New("x", WithBaseURL(srv.URL), WithAPIKey("k"))
	m.retryBackoff = time.Millisecond

	if _, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil); err == nil {
		t.Fatal("expected error for 400")
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("requests = %d, want 1 (no retry on 4xx)", got)
	}
}

func TestWithMaxRetriesZeroDisablesRetry(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	m, _ := New("x", WithBaseURL(srv.URL), WithAPIKey("k"), WithMaxRetries(0))
	m.retryBackoff = time.Millisecond

	if _, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil); err == nil {
		t.Fatal("expected error")
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestWithTimeoutBuildsClientAndHTTPClientWins(t *testing.T) {
	m, _ := New("x", WithTimeout(7*time.Second))
	tr, ok := m.httpClient.Transport.(*http.Transport)
	if !ok || tr.TLSHandshakeTimeout != 7*time.Second {
		t.Fatalf("transport = %+v, want TLSHandshakeTimeout 7s", m.httpClient.Transport)
	}
	custom := &http.Client{}
	m2, _ := New("x", WithHTTPClient(custom), WithTimeout(time.Second))
	if m2.httpClient != custom {
		t.Fatal("WithHTTPClient must take precedence over WithTimeout")
	}
}
