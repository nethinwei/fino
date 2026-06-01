package anthropic

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

// TestStreamEmitsThinking verifies that thinking_delta events surface as a
// thinking ContentBlockDelta ahead of the answer text and reach the final
// message.
func TestStreamEmitsThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"ponder\"}}\n\n")
		io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
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
	if thinking != "ponder" || text != "answer" {
		t.Fatalf("thinking=%q text=%q, want ponder/answer", thinking, text)
	}
	if len(kinds) < 2 || kinds[0] != "thinking" || kinds[1] != "text" {
		t.Fatalf("event order = %v, want thinking before text", kinds)
	}
	if final == nil || len(final.Content) != 2 ||
		final.Content[0].Type != message.TypeThinking || final.Content[0].Text != "ponder" ||
		final.Content[1].Type != message.TypeText || final.Content[1].Text != "answer" {
		t.Fatalf("final message = %+v", final)
	}
}

func TestPostRetriesOnServerError(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "busy")
			return
		}
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
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

func TestWithTimeoutBuildsClientAndHTTPClientWins(t *testing.T) {
	m, _ := New("x", WithTimeout(5*time.Second))
	tr, ok := m.httpClient.Transport.(*http.Transport)
	if !ok || tr.TLSHandshakeTimeout != 5*time.Second {
		t.Fatalf("transport = %+v, want TLSHandshakeTimeout 5s", m.httpClient.Transport)
	}
	custom := &http.Client{}
	m2, _ := New("x", WithHTTPClient(custom), WithTimeout(time.Second))
	if m2.httpClient != custom {
		t.Fatal("WithHTTPClient must take precedence over WithTimeout")
	}
}
