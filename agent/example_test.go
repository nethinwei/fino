package agent_test

import (
	"context"
	"fmt"
	"iter"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
)

type staticModel struct{}

func (staticModel) Generate(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) (*message.Message, error) {
	msg := message.Assistant(message.NewText("hello"))
	return &msg, nil
}

func (staticModel) Stream(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {}
}

func Example() {
	mode, _ := agent.NewMode("default", "Be helpful.")
	a, _ := agent.New("assistant", agent.WithMode(mode), agent.WithDefaultMode("default"))
	r, _ := runner.New(staticModel{})
	result, _ := r.Run(context.Background(), a, runner.Text("hi"))
	fmt.Println(result.Text())
	// Output: hello
}
