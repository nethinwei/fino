package kimi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/providers/openai"
)

func TestNewRoundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	m, err := New("kimi-k2", "k", openai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
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
	m, err := New("kimi-k2", "k", openai.WithBaseURL(srv.URL))
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
