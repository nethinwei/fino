package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
)

func TestAssistantBlocksMultimodal(t *testing.T) {
	msg := message.Assistant(
		message.NewText("look"),
		message.NewImage("image/png", message.WithBase64("AAA=")),
	)
	blocks := assistantBlocks(msg)
	if len(blocks) != 2 {
		t.Fatalf("len = %d", len(blocks))
	}
	if blocks[1].Type != "image" || blocks[1].Source == nil {
		t.Fatalf("image block = %+v", blocks[1])
	}
	if blocks[1].Source.Type != "base64" || blocks[1].Source.MediaType != "image/png" ||
		blocks[1].Source.Data != "AAA=" {
		t.Fatalf("source = %+v", blocks[1].Source)
	}
}

func TestAssistantBlocksImageURLSource(t *testing.T) {
	msg := message.Assistant(message.NewImage("image/png", message.WithURL("https://x/y.png")))
	blocks := assistantBlocks(msg)
	if blocks[0].Source.Type != "url" || blocks[0].Source.URL != "https://x/y.png" {
		t.Fatalf("source = %+v", blocks[0].Source)
	}
}

func TestAssistantBlocksFileIDSource(t *testing.T) {
	msg := message.Assistant(message.NewFile("application/pdf", message.WithFileID("file_1")))
	blocks := assistantBlocks(msg)
	// Anthropic models PDF/document via a document block; the file id source maps
	// to source.type "file".
	if blocks[0].Source == nil || blocks[0].Source.Type != "file" ||
		blocks[0].Source.FileID != "file_1" {
		t.Fatalf("source = %+v", blocks[0].Source)
	}
}

func TestUserMultimodalMessage(t *testing.T) {
	msg := message.Message{Role: message.RoleUser, Content: []message.Block{
		message.NewText("what is this?"),
		message.NewImage("image/png", message.WithURL("https://x/y.png")),
	}}
	_, out := toAnthropicMessages([]message.Message{msg})
	if len(out) != 1 || len(out[0].Content) != 2 {
		t.Fatalf("out = %+v", out)
	}
	if out[0].Content[1].Type != "image" || out[0].Content[1].Source == nil ||
		out[0].Content[1].Source.URL != "https://x/y.png" {
		t.Fatalf("image = %+v", out[0].Content[1])
	}
}

func TestToolResultMultimodalContent(t *testing.T) {
	msg := message.ToolResults(message.NewToolResult("t1", "snap", []message.Block{
		message.NewText("screenshot"),
		message.NewImage("image/png", message.WithBase64("AAA=")),
	}, false))
	_, out := toAnthropicMessages([]message.Message{msg})
	if len(out) != 1 || len(out[0].Content) != 1 {
		t.Fatalf("out = %+v", out)
	}
	var parts []map[string]any
	if err := json.Unmarshal(out[0].Content[0].Content, &parts); err != nil {
		t.Fatalf("content not an array: %s, err %v", out[0].Content[0].Content, err)
	}
	if len(parts) != 2 || parts[1]["type"] != "image" {
		t.Fatalf("parts = %+v", parts)
	}
}

func TestToolResultTextContentStillString(t *testing.T) {
	msg := message.ToolResults(message.NewToolResult("t1", "echo", []message.Block{message.NewText("hi")}, false))
	_, out := toAnthropicMessages([]message.Message{msg})
	var s string
	if err := json.Unmarshal(out[0].Content[0].Content, &s); err != nil {
		t.Fatalf("content not a string: %s", out[0].Content[0].Content)
	}
	if s != "hi" {
		t.Fatalf("s = %q", s)
	}
}

func TestGenerateParsesImageOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA="}}]}`)
	}))
	defer srv.Close()
	m, _ := New("claude-3-5-sonnet", WithBaseURL(srv.URL), WithAPIKey("k"))
	msg, err := m.Generate(context.Background(), []message.Message{message.UserText("draw")}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(msg.Content) != 1 || msg.Content[0].Type != message.TypeImage {
		t.Fatalf("msg = %+v", msg)
	}
	b := msg.Content[0]
	if b.SourceType != message.SourceBase64 || b.Data != "AAA=" || b.MediaType != "image/png" {
		t.Fatalf("block = %+v", b)
	}
}

func TestStreamParsesImageBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA="}}}`+"\n\n")
		io.WriteString(w, `event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`+"\n\n")
		io.WriteString(w, `event: message_stop`+"\n"+`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer srv.Close()
	m, _ := New("claude-3-5-sonnet", WithBaseURL(srv.URL), WithAPIKey("k"))

	var final *message.Message
	for ev, err := range m.Stream(context.Background(), []message.Message{message.UserText("draw")}, nil) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		if tm, ok := ev.(model.TurnMessage); ok {
			fm := tm.Message
			final = &fm
		}
	}
	if final == nil || len(final.Content) != 1 || final.Content[0].Type != message.TypeImage {
		t.Fatalf("final = %+v", final)
	}
	if final.Content[0].Data != "AAA=" || final.Content[0].MediaType != "image/png" {
		t.Fatalf("block = %+v", final.Content[0])
	}
}

func TestModelCapabilities(t *testing.T) {
	m, _ := New("claude-3-5-sonnet")
	var mm model.Model = m
	caps, ok := mm.(model.Capabilities)
	if !ok {
		t.Fatal("Model should satisfy model.Capabilities")
	}
	info := caps.Capabilities()
	if !hasModality(info.InputModalities, message.TypeImage) {
		t.Fatalf("input modalities missing image: %+v", info.InputModalities)
	}
	if !info.SupportsStreaming {
		t.Fatal("SupportsStreaming should be true")
	}
}

func hasModality(list []message.BlockType, want message.BlockType) bool {
	for _, b := range list {
		if b == want {
			return true
		}
	}
	return false
}
