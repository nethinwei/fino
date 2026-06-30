// Command history_trim demonstrates the canonical fino extension pattern:
// wrapping model.Model.
//
//	DEEPSEEK_API_KEY=sk-... go run ./examples/history_trim
//
// fino's hooks are observe-only by design — BeforeModel cannot rewrite the
// outgoing messages. When a long-running conversation would overflow the
// model's context window, the right extension point is the model interface
// itself: wrap any model.Model and trim the history before delegating.
//
// The key property: this wrapper is written once and works for EVERY provider
// (openai, anthropic, deepseek, kimi, glm, qwen, minimax, or your own),
// because they all satisfy the same model.Model interface. There is nothing
// provider-specific to special-case.
//
// Run it and watch the two log lines per turn: the runner's BeforeModel hook
// reports how many messages it handed to the model, and the wrapper's "[trim]"
// line reports how many actually went out after trimming.
package main

import (
	"context"
	"fmt"
	"iter"
	"log"
	"os"
	"strings"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/providers/deepseek"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/react"
)

// trimmingModel wraps any model.Model and trims the message history to an
// approximate character budget before each call. By embedding model.Model it
// inherits the interface and overrides only the two methods that send messages.
type trimmingModel struct {
	model.Model
	budget int // approximate max total characters of message text
}

func (t trimmingModel) Generate(ctx context.Context, msgs []message.Message, tools []tool.Info, opts ...model.Option) (*message.Message, error) {
	return t.Model.Generate(ctx, trim(msgs, t.budget), tools, opts...)
}

func (t trimmingModel) Stream(ctx context.Context, msgs []message.Message, tools []tool.Info, opts ...model.Option) iter.Seq2[model.Event, error] {
	return t.Model.Stream(ctx, trim(msgs, t.budget), tools, opts...)
}

// trim keeps the system message (always first) plus the most recent messages
// that fit within budget, walking backward from the newest.
//
// This is a deliberately simple demonstration. A production trimmer would
// count real tokens (not characters), keep a tool_use and its matching
// tool_result together, and likely summarize dropped turns rather than discard
// them. Those concerns all live here in user code — not in fino's core.
func trim(msgs []message.Message, budget int) []message.Message {
	if len(msgs) <= 1 {
		return msgs
	}
	system, rest := msgs[0], msgs[1:]

	total, cut := 0, len(rest)
	for i := len(rest) - 1; i >= 0; i-- {
		if total += size(rest[i]); total > budget {
			break
		}
		cut = i
	}
	kept := rest[cut:]

	// Never start the kept window on an orphan tool_result: a tool_result must
	// follow the assistant tool_use that produced it, or the API will reject it.
	for len(kept) > 0 && kept[0].Role == message.RoleTool {
		kept = kept[1:]
	}

	if len(kept)+1 < len(msgs) {
		log.Printf("[trim]   %d -> %d messages (budget %d chars)", len(msgs), len(kept)+1, budget)
	}
	return append([]message.Message{system}, kept...)
}

// size approximates a message's token cost by its text length. It is a stand-in
// for a real tokenizer, which is what a production wrapper would use.
func size(m message.Message) int { return len(m.Text()) }

func main() {
	log.SetFlags(log.Ltime)

	base, err := newModel()
	if err != nil {
		log.Fatal(err)
	}
	// Wrap once; works for any provider.
	m := trimmingModel{Model: base, budget: 600}

	mode, err := agent.NewMode("default", "You are a concise Go tutor. Answer in one sentence.")
	if err != nil {
		log.Fatal(err)
	}
	a, err := agent.New("tutor", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		log.Fatal(err)
	}

	r, err := runner.New(m, runner.WithHooks(&hooks.Hooks{
		BeforeModel: func(ctx context.Context, c hooks.ModelCall) context.Context {
			log.Printf("[runner] handing %d message(s) to the model", len(c.Messages))
			return ctx
		},
	}))
	if err != nil {
		log.Fatal(err)
	}
	l, err := react.New(r)
	if err != nil {
		log.Fatal(err)
	}

	// A long prior conversation that would otherwise grow unbounded.
	history := seedHistory()
	log.Printf("[input]  resuming a conversation with %d prior message(s)", len(history))

	result, err := l.Run(context.Background(), a, runner.Messages(history))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\n" + result.Text())
}

// seedHistory fabricates a long multi-turn history (no system message — the
// Runner injects that from the mode) ending in a fresh question.
func seedHistory() []message.Message {
	topics := []string{"goroutines", "channels", "interfaces", "generics", "slices", "maps", "defer", "errors"}
	h := make([]message.Message, 0, len(topics)*2+1)
	for _, topic := range topics {
		h = append(h, message.UserText("Explain Go "+topic+" briefly."))
		h = append(h, message.Assistant(message.NewText(
			"Go "+topic+": "+strings.Repeat("a key detail, ", 8))))
	}
	h = append(h, message.UserText("Given all of the above, what single concept should I study next, and why?"))
	return h
}

// newModel builds a DeepSeek model from environment variables.
func newModel() (model.Model, error) {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("set DEEPSEEK_API_KEY")
	}
	name := os.Getenv("DEEPSEEK_MODEL")
	if name == "" {
		name = "deepseek-v4-flash"
	}
	return deepseek.New(name, key)
}
