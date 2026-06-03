package agui

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Handler returns an http.Handler that decodes a RunAgentInput from the
// request body and streams the AG-UI events as Server-Sent Events. Each event
// is JSON-encoded on a single "data:" line terminated by a blank line. The
// handler sets Content-Type: text/event-stream and flushes after each event
// when the underlying ResponseWriter supports http.Flusher. Client
// cancellation propagates through the request context to the fino runner.
func Handler(rt *Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input RunAgentInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, canFlush := w.(http.Flusher)
		for event, iterErr := range rt.Stream(r.Context(), input) {
			data, encErr := json.Marshal(event)
			if encErr != nil {
				data, _ = json.Marshal(RunErrorEvent{
					BaseEvent: BaseEvent{Type: EventRunError},
					Message:   fmt.Sprintf("encode event: %v", encErr),
				})
				fmt.Fprintf(w, "data: %s\n\n", data)
				if canFlush {
					flusher.Flush()
				}
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			if canFlush {
				flusher.Flush()
			}
			if iterErr != nil {
				return
			}
		}
	})
}
