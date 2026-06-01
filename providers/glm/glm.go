// Package glm provides a preset constructor and typed options for Zhipu's GLM
// models, which expose an OpenAI-compatible endpoint. It is a thin layer over
// the openai adapter: it fixes the base URL and adds GLM-specific options via
// the OpenAI extra_body mechanism.
package glm

import (
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/providers/openai"
)

// BaseURL is Zhipu's OpenAI-compatible endpoint.
const BaseURL = "https://api.z.ai/api/paas/v4"

// New creates a GLM model over Zhipu's OpenAI-compatible endpoint. Additional
// options (including openai.WithBaseURL for a proxy) are applied after the
// preset base URL and key.
func New(modelName, apiKey string, opts ...openai.Option) (*openai.Model, error) {
	base := []openai.Option{openai.WithBaseURL(BaseURL), openai.WithAPIKey(apiKey)}
	return openai.New(modelName, append(base, opts...)...)
}

// WithThinking toggles GLM's thinking mode, sending {"thinking":{"type":...}}.
// It is a per-call model.Option usable via runner.WithModelOptions.
func WithThinking(enabled bool) model.Option {
	t := "disabled"
	if enabled {
		t = "enabled"
	}
	return openai.WithExtraBody("thinking", map[string]string{"type": t})
}
