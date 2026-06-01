package openai

import (
	"context"
	"os"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
)

// TestLiveDeepSeek hits the real DeepSeek OpenAI-compatible endpoint. It is
// skipped unless DEEPSEEK_API_KEY is set. Override the model via DEEPSEEK_MODEL
// (default deepseek-v4-flash).
func TestLiveDeepSeek(t *testing.T) {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("set DEEPSEEK_API_KEY to run the live DeepSeek test")
	}
	name := os.Getenv("DEEPSEEK_MODEL")
	if name == "" {
		name = "deepseek-v4-flash"
	}
	m, err := New(name, WithBaseURL("https://api.deepseek.com"), WithAPIKey(key))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// v4-flash reasons before answering; give enough budget for both reasoning
	// and the final content, otherwise reasoning alone consumes the budget.
	msg, err := m.Generate(context.Background(),
		[]message.Message{message.UserText("Reply with the single word: pong")},
		nil, model.WithMaxTokens(512))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if blockText(msg) == "" {
		t.Fatal("empty response (no text or thinking)")
	}
	t.Logf("Generate text=%q", msg.Text())

	var streamed string
	for ev, err := range m.Stream(context.Background(),
		[]message.Message{message.UserText("Reply with the single word: pong")},
		nil, model.WithMaxTokens(512)) {
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		if d, ok := ev.(model.TextDelta); ok {
			streamed += d.Text
		}
	}
	t.Logf("Stream text=%q", streamed)
}

// blockText returns the combined text and thinking content of a message.
func blockText(msg *message.Message) string {
	out := ""
	for _, b := range msg.Content {
		if b.Type == message.TypeText || b.Type == message.TypeThinking {
			out += b.Text
		}
	}
	return out
}
