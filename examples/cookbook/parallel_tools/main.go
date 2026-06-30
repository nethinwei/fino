// Command parallel_tools shows bounded concurrent tool execution within a
// single tool-call batch via runner.WithMaxConcurrency. The Runner authorizes
// calls serially, runs them concurrently, and still appends results in call
// order — concurrency never reorders the transcript.
//
// It uses a tiny scripted model so it runs offline and deterministically.
//
//	go run ./examples/cookbook/parallel_tools
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"time"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/react"
)

// scriptedModel asks for three tools at once, then finishes.
type scriptedModel struct {
	turns []message.Message
	i     int
}

func (m *scriptedModel) next() message.Message {
	if m.i >= len(m.turns) {
		return message.Assistant(message.NewText("all fetched"))
	}
	msg := m.turns[m.i]
	m.i++
	return msg
}

func (m *scriptedModel) Generate(context.Context, []message.Message, []tool.Info, ...model.Option) (*message.Message, error) {
	msg := m.next()
	return &msg, nil
}

func (m *scriptedModel) Stream(context.Context, []message.Message, []tool.Info, ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		msg := m.next()
		yield(model.TurnMessage{Message: msg}, nil)
	}
}

type fetchInput struct {
	URL string `json:"url" jsonschema:"description=resource to fetch"`
}

func main() {
	// A slow tool: serial execution would take 3×; parallel collapses it.
	fetch, err := tool.NewFunc("fetch", "Fetch a URL (simulated 100ms latency)",
		func(_ context.Context, in fetchInput) (string, error) {
			time.Sleep(100 * time.Millisecond)
			return "200 OK " + in.URL, nil
		})
	if err != nil {
		log.Fatal(err)
	}
	mode, err := agent.NewMode("default", "Fetch the requested URLs.", agent.WithTools(fetch))
	if err != nil {
		log.Fatal(err)
	}
	a, err := agent.New("assistant", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		log.Fatal(err)
	}

	batch := message.Assistant(
		message.NewToolUse("c1", "fetch", json.RawMessage(`{"url":"https://a.example"}`)),
		message.NewToolUse("c2", "fetch", json.RawMessage(`{"url":"https://b.example"}`)),
		message.NewToolUse("c3", "fetch", json.RawMessage(`{"url":"https://c.example"}`)),
	)

	// Up to 3 tools run at once. Results still come back in call order c1,c2,c3.
	r, err := runner.New(&scriptedModel{turns: []message.Message{batch}}, runner.WithMaxConcurrency(3))
	if err != nil {
		log.Fatal(err)
	}
	l, err := react.New(r)
	if err != nil {
		log.Fatal(err)
	}

	start := time.Now()
	res, err := l.Run(context.Background(), a, runner.Text("Fetch a, b and c"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("final: %q (elapsed %v, well under 300ms serial)\n", res.Text(), time.Since(start).Round(time.Millisecond))

	for _, msg := range res.Messages {
		if msg.Role == message.RoleTool {
			for _, b := range msg.Content {
				fmt.Printf("ordered result %s: %s\n", b.ToolUseID, b.Content[0].Text)
			}
		}
	}
}
