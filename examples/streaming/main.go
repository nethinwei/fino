// Command streaming shows how to consume the Runner's streaming events:
// text deltas, tool calls, tool results, and the final message.
//
//	DEEPSEEK_API_KEY=sk-... go run ./examples/streaming
//
// The prompt deliberately asks for a long answer (a tool call followed by a
// short story) so the token-by-token flow is visible in the console: the
// assistant text appears gradually rather than all at once.
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/model"
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

const prompt = "First use the add tool to compute 144 + 233. " +
	"Then write a vivid short story of about 150 words about a night traveler " +
	"who arrives at a hotel and is given the room whose number equals that sum. " +
	"Stream the story as flowing prose."

func main() {
	m, err := newModel()
	if err != nil {
		log.Fatal(err)
	}

	add, err := tool.NewFunc("add", "Add two integers and return the sum.",
		func(ctx context.Context, in addInput) (string, error) {
			sum := in.A + in.B
			log.Printf("[tool] add(a=%d, b=%d) computed = %d", in.A, in.B, sum)
			return fmt.Sprintf("%d", sum), nil
		})
	if err != nil {
		log.Fatal(err)
	}
	mode, err := agent.NewMode("default",
		"You are a helpful assistant. Use the add tool for arithmetic, then answer.",
		agent.WithTools(add))
	if err != nil {
		log.Fatal(err)
	}
	a, err := agent.New("assistant", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		log.Fatal(err)
	}
	r, err := runner.New(m)
	if err != nil {
		log.Fatal(err)
	}
	l, err := react.New(r)
	if err != nil {
		log.Fatal(err)
	}

	rnd := &renderer{out: bufio.NewWriter(os.Stdout)}
	defer rnd.out.Flush()
	stream := l.Stream(context.Background(), a, runner.Text(prompt),
		runner.WithModelOptions(model.WithMaxTokens(1024)))
	for ev, err := range stream {
		if err != nil {
			log.Fatal(err)
		}
		rnd.render(ev)
	}
	rnd.finish()
}

// renderer prints stream events incrementally, flushing after every text delta
// so the assistant's words appear as they arrive.
type renderer struct {
	out      *bufio.Writer
	textOpen bool // currently streaming an assistant text run
}

func (r *renderer) render(ev model.Event) {
	switch e := ev.(type) {
	case model.TextDelta:
		if !r.textOpen {
			fmt.Fprint(r.out, "\n[assistant] ")
			r.textOpen = true
		}
		fmt.Fprint(r.out, e.Text)
		r.out.Flush() // flush each delta so the flow is visible
	case model.ToolCall:
		r.endText()
		fmt.Fprintf(r.out, "[tool call]   %s(%s)\n", e.Call.Name, string(e.Call.Input))
		r.out.Flush()
	case model.ToolResult:
		fmt.Fprintf(r.out, "[tool result] %s -> %s\n", e.Name, e.Result.Text())
		r.out.Flush()
	case model.TurnMessage:
		// Per-turn assistant snapshot; text already streamed via TextDelta, so
		// just close the current line at the turn boundary.
		r.endText()
	case model.FinalMessage:
		// Runner's run-terminal event; nothing extra to print.
	}
}

func (r *renderer) endText() {
	if r.textOpen {
		fmt.Fprintln(r.out)
		r.textOpen = false
	}
}

func (r *renderer) finish() {
	r.endText()
	r.out.Flush()
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
