package message

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTextMessageHelpers(t *testing.T) {
	msg := UserText("hello")
	if msg.Role != RoleUser {
		t.Fatalf("role = %q", msg.Role)
	}
	if got := msg.Text(); got != "hello" {
		t.Fatalf("Text() = %q", got)
	}
}

func TestHasSystem(t *testing.T) {
	if HasSystem(nil) {
		t.Fatal("HasSystem(nil) = true, want false")
	}
	if HasSystem([]Message{UserText("hi"), Assistant(NewText("yo"))}) {
		t.Fatal("HasSystem(no system) = true, want false")
	}
	if !HasSystem([]Message{UserText("hi"), SystemText("be useful")}) {
		t.Fatal("HasSystem(with system) = false, want true")
	}
}

func TestFlatJSONShape(t *testing.T) {
	data, err := json.Marshal(NewText("hello"))
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"type":"text"`) || !strings.Contains(got, `"text":"hello"`) {
		t.Fatalf("unexpected json: %s", got)
	}
	if strings.Contains(got, `"text":{"text"`) {
		t.Fatalf("nested text object is not allowed: %s", got)
	}
}

func TestToolUses(t *testing.T) {
	msg := Assistant(NewToolUse("call_1", "search", json.RawMessage(`{"query":"go"}`)))
	calls := msg.ToolUses()
	if len(calls) != 1 || calls[0].Name != "search" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestToolResultsMessageBatchesBlocks(t *testing.T) {
	msg := ToolResults(
		NewToolResult("call_1", "search", []Block{NewText("one")}, false),
		NewToolResult("call_2", "read", []Block{NewText("two")}, false),
	)
	if msg.Role != RoleTool {
		t.Fatalf("role = %q", msg.Role)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("blocks = %d", len(msg.Content))
	}
	if msg.Content[0].Content[0].Text != "one" {
		t.Fatalf("first result content = %#v", msg.Content[0].Content)
	}
}

func TestMultimodalBlockJSON(t *testing.T) {
	modalities := []struct {
		name      string
		construct func(string, ...MediaOption) Block
		wantType  string
	}{
		{"image", NewImage, `"type":"image"`},
		{"audio", NewAudio, `"type":"audio"`},
		{"video", NewVideo, `"type":"video"`},
		{"file", NewFile, `"type":"file"`},
	}
	sources := []struct {
		name     string
		opt      MediaOption
		contains []string
	}{
		{"base64", WithBase64("AAA="), []string{`"source_type":"base64"`, `"data":"AAA="`}},
		{"url", WithURL("https://x/y.png"), []string{`"source_type":"url"`, `"url":"https://x/y.png"`}},
		{"file_id", WithFileID("file_1"), []string{`"source_type":"file_id"`, `"file_id":"file_1"`}},
	}
	for _, m := range modalities {
		for _, s := range sources {
			t.Run(m.name+"_"+s.name, func(t *testing.T) {
				b := m.construct("image/png", s.opt)
				data, err := json.Marshal(b)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				got := string(data)
				wants := append([]string{m.wantType, `"media_type":"image/png"`}, s.contains...)
				for _, want := range wants {
					if !strings.Contains(got, want) {
						t.Fatalf("json %q missing %q", got, want)
					}
				}
				// 扁平 discriminated union：禁止嵌套 source 子对象。
				if strings.Contains(got, `"source":{`) {
					t.Fatalf("nested source object is not allowed: %s", got)
				}
			})
		}
	}
}

func TestMultimodalBlockOmitempty(t *testing.T) {
	b := NewImage("image/png", WithBase64("AAA="))
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	for _, unwanted := range []string{`"url":`, `"file_id":`} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("base64 source should omit %s: %s", unwanted, got)
		}
	}
}

func TestMultimodalBlockRoundtrip(t *testing.T) {
	orig := NewAudio("audio/wav", WithURL("https://x/a.wav"))
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Block
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Type != TypeAudio || back.SourceType != SourceURL ||
		back.URL != "https://x/a.wav" || back.MediaType != "audio/wav" {
		t.Fatalf("roundtrip mismatch: %#v", back)
	}
}

func TestImageDetailOption(t *testing.T) {
	b := NewImage("image/png", WithURL("https://x/y.png"), WithDetail("high"))
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"detail":"high"`) {
		t.Fatalf("missing detail: %s", data)
	}
}
