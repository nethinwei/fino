package deepseek

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/providers/anthropic"
	"github.com/nethinwei/fino/providers/openai"
)

func TestNewOpenAIRoundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	m, err := New("deepseek-chat", "k", openai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil)
	if err != nil || msg.Text() != "ok" {
		t.Fatalf("Generate msg=%v err=%v", msg, err)
	}
}

func TestNewAnthropicRoundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()
	m, err := NewAnthropic("deepseek-v4-flash", "k", anthropic.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	msg, err := m.Generate(context.Background(), []message.Message{message.UserText("hi")}, nil)
	if err != nil || msg.Text() != "ok" {
		t.Fatalf("Generate msg=%v err=%v", msg, err)
	}
}
