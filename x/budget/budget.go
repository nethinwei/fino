// Package budget provides a cost-bounded model decorator for fino.
//
// It is a reference composition for the sufficiency thesis in docs/design.md:
// cost control is a cross-cutting concern handled by wrapping model.Model, not
// by teaching the core about provider billing. When the accumulated cost
// reaches the limit, the next model call returns ErrBudgetExceeded, which the
// Runner surfaces as a run-time terminating error (firing OnError once).
package budget

import (
	"context"
	"errors"
	"iter"
	"sync"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/tool"
)

// ErrBudgetExceeded is returned by a Model once the cumulative cost reaches the
// configured limit.
var ErrBudgetExceeded = errors.New("budget exceeded")

// CostFunc returns the cost of a single model response. A common choice is an
// output-token estimate; the core never interprets the unit.
type CostFunc func(*message.Message) int

// Model is a cost-bounded decorator. Before each call it checks the running
// total against Limit; after each call it adds the response cost.
type Model struct {
	Next  model.Model
	Limit int
	Cost  CostFunc

	mu   sync.Mutex
	used int
}

// New returns a budget Model wrapping next with the given limit and cost.
func New(next model.Model, limit int, cost CostFunc) *Model {
	return &Model{Next: next, Limit: limit, Cost: cost}
}

// Used reports the cumulative cost consumed so far.
func (m *Model) Used() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.used
}

func (m *Model) checkAndReserve() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used >= m.Limit {
		return ErrBudgetExceeded
	}
	return nil
}

func (m *Model) add(c int) {
	m.mu.Lock()
	m.used += c
	m.mu.Unlock()
}

// Generate enforces the budget around the wrapped model.
func (m *Model) Generate(ctx context.Context, msgs []message.Message, tools []tool.Info, opts ...model.Option) (*message.Message, error) {
	if err := m.checkAndReserve(); err != nil {
		return nil, err
	}
	out, err := m.Next.Generate(ctx, msgs, tools, opts...)
	if err != nil {
		return nil, err
	}
	m.add(m.Cost(out))
	return out, nil
}

// Stream enforces the budget around the wrapped model's stream.
func (m *Model) Stream(ctx context.Context, msgs []message.Message, tools []tool.Info, opts ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		if err := m.checkAndReserve(); err != nil {
			yield(model.StreamError{Err: err}, err)
			return
		}
		for ev, err := range m.Next.Stream(ctx, msgs, tools, opts...) {
			if err != nil {
				yield(ev, err)
				return
			}
			if fm, ok := ev.(model.FinalMessage); ok {
				m.add(m.Cost(&fm.Message))
			}
			if !yield(ev, nil) {
				return
			}
		}
	}
}
