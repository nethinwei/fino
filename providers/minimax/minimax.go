// Package minimax provides a preset constructor and typed options for MiniMax
// models, which expose an OpenAI-compatible endpoint. It is a thin layer over
// the openai adapter: it fixes the base URL and adds MiniMax-specific options
// via the OpenAI extra_body mechanism.
package minimax

import (
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/providers/openai"
)

const (
	// BaseURL is the international OpenAI-compatible endpoint.
	BaseURL = "https://api.minimax.io/v1"
	// BaseURLChina is the mainland China OpenAI-compatible endpoint.
	BaseURLChina = "https://api.minimax.chat/v1"
)

// New creates a MiniMax model over the OpenAI-compatible endpoint. Additional
// options (including openai.WithBaseURL for the China endpoint or a proxy) are
// applied after the preset base URL and key.
func New(modelName, apiKey string, opts ...openai.Option) (*openai.Model, error) {
	base := []openai.Option{openai.WithBaseURL(BaseURL), openai.WithAPIKey(apiKey)}
	return openai.New(modelName, append(base, opts...)...)
}

// WithThinking toggles MiniMax deep thinking, sending
// {"thinking":{"type":"adaptive"|"disabled"}}. Without it, reasoning models
// return thinking inline in the message content.
func WithThinking(enabled bool) model.Option {
	t := "disabled"
	if enabled {
		t = "adaptive"
	}
	return openai.WithExtraBody("thinking", map[string]string{"type": t})
}

// WithReasoningSplit sends reasoning_split, asking M2/M3 models to separate
// thinking into the response's reasoning_details field.
func WithReasoningSplit(enabled bool) model.Option {
	return openai.WithExtraBody("reasoning_split", enabled)
}
