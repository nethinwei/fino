// Package deepseek provides preset constructors for DeepSeek, which exposes
// both an OpenAI-compatible and an Anthropic-compatible endpoint. It is a thin
// layer over the openai and anthropic adapters: it only fixes the base URLs.
// deepseek-v4-flash and deepseek-v4-pro are the current model tiers; each
// supports both thinking and non-thinking modes, and thinking output surfaces
// as a thinking block in the response. The legacy deepseek-chat and
// deepseek-reasoner names both map to deepseek-v4-flash (non-thinking and
// thinking respectively) and are deprecated as of 2026/07/24.
package deepseek

import (
	"github.com/nethinwei/fino/providers/anthropic"
	"github.com/nethinwei/fino/providers/openai"
)

const (
	// BaseURL is the OpenAI-compatible endpoint.
	BaseURL = "https://api.deepseek.com"
	// BaseURLAnthropic is the Anthropic-compatible endpoint.
	BaseURLAnthropic = "https://api.deepseek.com/anthropic"
)

// New creates a DeepSeek model over the OpenAI-compatible endpoint. Additional
// options (including openai.WithBaseURL for proxies) are applied after the
// preset base URL and key.
func New(modelName, apiKey string, opts ...openai.Option) (*openai.Model, error) {
	base := []openai.Option{openai.WithBaseURL(BaseURL), openai.WithAPIKey(apiKey)}
	return openai.New(modelName, append(base, opts...)...)
}

// NewAnthropic creates a DeepSeek model over the Anthropic-compatible endpoint.
func NewAnthropic(modelName, apiKey string, opts ...anthropic.Option) (*anthropic.Model, error) {
	base := []anthropic.Option{anthropic.WithBaseURL(BaseURLAnthropic), anthropic.WithAPIKey(apiKey)}
	return anthropic.New(modelName, append(base, opts...)...)
}
