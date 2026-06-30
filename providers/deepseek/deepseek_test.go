package deepseek

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestMultimodalRequestShape(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	m, err := New("deepseek-chat", "k", openai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg := message.Message{Role: message.RoleUser, Content: []message.Block{
		message.NewText("see"),
		message.NewImage("image/png", message.WithURL("https://x/y.png")),
	}}
	if _, err := m.Generate(context.Background(), []message.Message{msg}, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(body), `"image_url"`) {
		t.Fatalf("request missing image_url: %s", body)
	}
}

func TestAnthropicMultimodalRequestShape(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()
	m, err := NewAnthropic("deepseek-v4-flash", "k", anthropic.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	msg := message.Message{Role: message.RoleUser, Content: []message.Block{
		message.NewText("see"),
		message.NewImage("image/png", message.WithURL("https://x/y.png")),
	}}
	if _, err := m.Generate(context.Background(), []message.Message{msg}, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(body), `"source"`) {
		t.Fatalf("request missing source: %s", body)
	}
}
