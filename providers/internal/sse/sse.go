// Package sse holds the Server-Sent Events streaming loop shared by the
// provider adapters. The OpenAI- and Anthropic-style endpoints differ in their
// payload shapes but share the same transport framing: line-oriented "data:"
// records terminated by a blank line. This package centralizes that scan loop
// so each adapter only supplies its own payload-to-event mapping.
package sse

import (
	"bufio"
	"io"
	"iter"
	"strings"

	"github.com/nethinwei/fino/model"
)

// scanBufferMax bounds a single SSE line; large tool-call argument fragments
// can exceed the scanner's default 64KiB token, so the cap is raised to 1MiB.
const scanBufferMax = 1024 * 1024

// Handler folds one SSE "data:" payload into provider-specific state and
// returns the events to emit for it. Returning done=true ends the stream after
// emitting those events (e.g. the OpenAI "[DONE]" sentinel); the final message
// is still yielded afterward. A non-nil error terminates the stream.
type Handler func(payload string) (events []model.Event, done bool, err error)

// Stream scans an SSE response body and drives the model event iterator shared
// by the adapters: it forwards each "data:" record to handle, emits the
// returned events, and yields final once the stream ends cleanly. Lines without
// a "data:" prefix (such as Anthropic's "event:" lines) are ignored. Terminal
// errors are yielded as a model.StreamError alongside a non-nil iterator error,
// matching the model.Model streaming contract.
func Stream(body io.Reader, handle Handler, final func() model.Event) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 0, 64*1024), scanBufferMax)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			events, done, err := handle(strings.TrimSpace(line[len("data:"):]))
			if err != nil {
				yield(model.StreamError{Err: err}, err)
				return
			}
			for _, ev := range events {
				if !yield(ev, nil) {
					return
				}
			}
			if done {
				break
			}
		}
		if err := sc.Err(); err != nil {
			yield(model.StreamError{Err: err}, err)
			return
		}
		yield(final(), nil)
	}
}
