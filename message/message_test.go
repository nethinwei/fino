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
