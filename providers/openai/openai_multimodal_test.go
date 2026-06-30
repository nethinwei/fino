package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
)

func TestBuildContentTextOnly(t *testing.T) {
	raw := buildContent([]message.Block{message.NewText("hi")})
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("not a string: %s", raw)
	}
	if s != "hi" {
		t.Fatalf("s = %q", s)
	}
}

func TestBuildContentMultimodal(t *testing.T) {
	raw := buildContent([]message.Block{
		message.NewText("look"),
		message.NewImage("image/png", message.WithBase64("AAA=")),
	})
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("not an array: %s", raw)
	}
	if len(parts) != 2 || parts[0]["type"] != "text" || parts[1]["type"] != "image_url" {
		t.Fatalf("parts = %+v", parts)
	}
	imgURL, ok := parts[1]["image_url"].(map[string]any)
	if !ok {
		t.Fatalf("image_url missing: %+v", parts[1])
	}
	if !strings.HasPrefix(imgURL["url"].(string), "data:image/png;base64,AAA=") {
		t.Fatalf("url = %v", imgURL["url"])
	}
}

func TestBuildContentImageURL(t *testing.T) {
	raw := buildContent([]message.Block{message.NewImage("image/png", message.WithURL("https://x/y.png"))})
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("not an array: %s", raw)
	}
	imgURL := parts[0]["image_url"].(map[string]any)
	if imgURL["url"] != "https://x/y.png" {
		t.Fatalf("url = %v", imgURL["url"])
	}
}

func TestBuildContentAudio(t *testing.T) {
	raw := buildContent([]message.Block{message.NewAudio("audio/wav", message.WithBase64("AAA="))})
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("not an array: %s", raw)
	}
	if parts[0]["type"] != "input_audio" {
		t.Fatalf("part = %+v", parts[0])
	}
	ia := parts[0]["input_audio"].(map[string]any)
	if ia["data"] != "AAA=" || ia["format"] != "wav" {
		t.Fatalf("input_audio = %+v", ia)
	}
}

func TestBuildContentVideoDegrades(t *testing.T) {
	raw := buildContent([]message.Block{message.NewVideo("video/mp4", message.WithURL("https://x/y.mp4"))})
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("not an array: %s", raw)
	}
	if parts[0]["type"] != "text" {
		t.Fatalf("video should degrade to text: %+v", parts[0])
	}
}

func TestToOpenAIMessagesUserImage(t *testing.T) {
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	m, _ := New("gpt-4o", WithBaseURL(srv.URL), WithAPIKey("k"))
	msg := message.Message{Role: message.RoleUser, Content: []message.Block{
		message.NewText("what?"),
		message.NewImage("image/png", message.WithURL("https://x/y.png")),
	}}
	if _, err := m.Generate(context.Background(), []message.Message{msg}, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var parts []map[string]any
	if err := json.Unmarshal(got.Messages[0].Content, &parts); err != nil {
		t.Fatalf("content not an array: %s", got.Messages[0].Content)
	}
	if len(parts) != 2 || parts[1]["type"] != "image_url" {
		t.Fatalf("parts = %+v", parts)
	}
}

func TestGenerateParsesImageOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":[`+
			`{"type":"text","text":"here"},`+
			`{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA="}}`+
			`]}}]}`)
	}))
	defer srv.Close()
	m, _ := New("gpt-4o", WithBaseURL(srv.URL), WithAPIKey("k"))
	msg, err := m.Generate(context.Background(), []message.Message{message.UserText("draw")}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(msg.Content) != 2 || msg.Content[1].Type != message.TypeImage {
		t.Fatalf("msg = %+v", msg)
	}
	b := msg.Content[1]
	if b.SourceType != message.SourceBase64 || b.Data != "AAA=" || b.MediaType != "image/png" {
		t.Fatalf("block = %+v", b)
	}
}

func TestGenerateParsesAudioOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":null,`+
			`"audio":{"id":"a1","data":"AAA=","transcript":"hi","format":"wav"}}}]}`)
	}))
	defer srv.Close()
	m, _ := New("gpt-4o-audio-preview", WithBaseURL(srv.URL), WithAPIKey("k"))
	msg, err := m.Generate(context.Background(), []message.Message{message.UserText("speak")}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	found := false
	for _, b := range msg.Content {
		if b.Type == message.TypeAudio && b.Data == "AAA=" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audio block: %+v", msg.Content)
	}
}

func TestStreamParsesAudioDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"audio\":{\"id\":\"a1\",\"delta\":\"AAA\"}}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"audio\":{\"id\":\"a1\",\"delta\":\"=\"}}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	m, _ := New("gpt-4o-audio-preview", WithBaseURL(srv.URL), WithAPIKey("k"))
	var final *message.Message
	for ev, err := range m.Stream(context.Background(), []message.Message{message.UserText("speak")}, nil) {
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		if tm, ok := ev.(model.TurnMessage); ok {
			fm := tm.Message
			final = &fm
		}
	}
	if final == nil {
		t.Fatal("no final message")
	}
	found := false
	for _, b := range final.Content {
		if b.Type == message.TypeAudio && b.Data == "AAA=" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audio block: %+v", final.Content)
	}
}

func TestModelCapabilities(t *testing.T) {
	m, _ := New("gpt-4o")
	var mm model.Model = m
	caps, ok := mm.(model.Capabilities)
	if !ok {
		t.Fatal("Model should satisfy model.Capabilities")
	}
	info := caps.Capabilities()
	hasImage := false
	for _, bt := range info.InputModalities {
		if bt == message.TypeImage {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatalf("input modalities missing image: %+v", info.InputModalities)
	}
}
