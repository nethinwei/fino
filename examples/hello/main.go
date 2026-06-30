// Command hello is a minimal end-to-end fino example: an agent with one tool,
// driven by an OpenAI-compatible model.
//
// Run it with your own credentials:
//
//	DEEPSEEK_API_KEY=sk-... go run ./examples/hello
//
// Override the model with DEEPSEEK_MODEL (default: deepseek-v4-flash;
// deepseek-v4-pro is the higher tier — both support thinking modes).
//
// The run is fully traced via hooks: a "[tool] add(...)" line proves the model
// actually invoked the tool rather than doing the arithmetic itself. If you
// only see model turns and a final answer with no tool line, the model
// answered directly.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/providers/deepseek"
	"github.com/nethinwei/fino/providers/openai"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/react"
)

type addInput struct {
	A int `json:"a" jsonschema:"description=first addend"`
	B int `json:"b" jsonschema:"description=second addend"`
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	m, err := newModel()
	if err != nil {
		log.Fatal(err)
	}

	add, err := tool.NewFunc("add", "Add two integers and return the sum.",
		func(ctx context.Context, in addInput) (string, error) {
			sum := in.A + in.B
			log.Printf("[tool]   add(a=%d, b=%d) computed = %d", in.A, in.B, sum)
			return fmt.Sprintf("%d", sum), nil
		})
	if err != nil {
		log.Fatal(err)
	}

	mode, err := agent.NewMode("default",
		"You are a helpful assistant. Use the add tool for arithmetic.",
		agent.WithTools(add))
	if err != nil {
		log.Fatal(err)
	}
	a, err := agent.New("assistant", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		log.Fatal(err)
	}

	r, err := runner.New(m, runner.WithHooks(traceHooks()))
	if err != nil {
		log.Fatal(err)
	}
	l, err := react.New(r)
	if err != nil {
		log.Fatal(err)
	}

	prompt := "What is 2 + 3? Use the add tool."
	log.Printf("[input]  %s", prompt)
	result, err := l.Run(context.Background(), a, runner.Text(prompt))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("[final]  %s", result.Text())
	fmt.Println(result.Text())
}

// traceHooks logs every model turn and tool execution so the ReAct loop is
// observable: model turns show what the model emitted, tool lines show actual
// invocations.
func traceHooks() *hooks.Hooks {
	turn := 0
	return &hooks.Hooks{
		BeforeModel: func(ctx context.Context, c hooks.ModelCall) context.Context {
			turn++
			log.Printf("[model]  turn %d -> sending %d message(s), %d tool(s) available",
				turn, len(c.Messages), len(c.Tools))
			return ctx
		},
		AfterModel: func(ctx context.Context, r hooks.ModelResult) {
			log.Printf("[model]  turn %d <- %s", turn, blockSummary(r.Message))
		},
		BeforeTool: func(ctx context.Context, c hooks.ToolCall) context.Context {
			log.Printf("[tool]   calling %q with input %s", c.Tool.Name, string(c.Input))
			return ctx
		},
		AfterTool: func(ctx context.Context, r hooks.ToolResult) {
			log.Printf("[tool]   %q returned %q", r.Tool.Name, r.Result.Text())
		},
		OnError: func(ctx context.Context, err error) {
			log.Printf("[error]  %v", err)
		},
	}
}

// blockSummary describes the blocks in a model message (e.g. "tool_use:add" vs
// "text"), making it clear whether the model chose to call a tool.
func blockSummary(msg *message.Message) string {
	if msg == nil {
		return "<nil>"
	}
	parts := make([]string, 0, len(msg.Content))
	for _, b := range msg.Content {
		switch b.Type {
		case message.TypeToolUse:
			parts = append(parts, "tool_use:"+b.Name)
		case message.TypeText:
			parts = append(parts, fmt.Sprintf("text(%dB)", len(b.Text)))
		case message.TypeThinking:
			parts = append(parts, fmt.Sprintf("thinking(%dB)", len(b.Text)))
		default:
			parts = append(parts, string(b.Type))
		}
	}
	if len(parts) == 0 {
		return "(empty)"
	}
	return strings.Join(parts, ", ")
}

// newModel builds a DeepSeek model from environment variables.
func newModel() (*openai.Model, error) {
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
