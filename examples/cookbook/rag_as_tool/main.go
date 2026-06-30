// Command rag_as_tool shows that retrieval-augmented generation needs no core
// support: a retriever is just a tool.Tool. The model decides when to search,
// the Runner runs the retrieval like any other tool, and the snippets flow back
// as a tool_result. Swap the in-memory store for a vector DB or hybrid search
// and nothing about the loop changes.
//
// It uses a tiny scripted model so it runs offline and deterministically.
//
//	go run ./examples/cookbook/rag_as_tool
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"strings"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/react"
)

// docs is a stand-in knowledge base. A real retriever would embed and rank.
var docs = map[string]string{
	"refunds":  "Refunds are issued within 5 business days to the original method.",
	"shipping": "Standard shipping takes 3-5 days; express is next-day.",
	"returns":  "Items can be returned unused within 30 days with a receipt.",
}

// scriptedModel: turn 0 searches the KB, turn 1 answers from the result.
type scriptedModel struct {
	turns []message.Message
	i     int
}

func (m *scriptedModel) next() message.Message {
	if m.i >= len(m.turns) {
		return message.Assistant(message.NewText("(no more script)"))
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

type searchInput struct {
	Query string `json:"query" jsonschema:"description=what to look up in the knowledge base"`
}

func main() {
	search, err := tool.NewFunc("kb_search", "Search the support knowledge base",
		func(_ context.Context, in searchInput) (string, error) {
			q := strings.ToLower(in.Query)
			for key, text := range docs {
				if strings.Contains(q, key) {
					return text, nil
				}
			}
			return "no matching document", nil
		})
	if err != nil {
		log.Fatal(err)
	}
	mode, err := agent.NewMode("default",
		"Answer support questions. Use kb_search before answering.",
		agent.WithTools(search))
	if err != nil {
		log.Fatal(err)
	}
	a, err := agent.New("support", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		log.Fatal(err)
	}

	script := []message.Message{
		message.Assistant(message.NewToolUse("c1", "kb_search", json.RawMessage(`{"query":"refunds policy"}`))),
		message.Assistant(message.NewText("Refunds go back to your original payment method within 5 business days.")),
	}
	r, err := runner.New(&scriptedModel{turns: script})
	if err != nil {
		log.Fatal(err)
	}
	l, err := react.New(r)
	if err != nil {
		log.Fatal(err)
	}

	res, err := l.Run(context.Background(), a, runner.Text("How long do refunds take?"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("answer: %s\n", res.Text())
}
