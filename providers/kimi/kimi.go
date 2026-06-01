// Package kimi provides a preset constructor for Moonshot's Kimi models, which
// expose an OpenAI-compatible endpoint. It is a thin layer over the openai
// adapter that fixes the base URL.
package kimi

import "github.com/nethinwei/fino/providers/openai"

const (
	// BaseURL is the mainland China endpoint.
	BaseURL = "https://api.moonshot.cn/v1"
	// BaseURLGlobal is the international endpoint.
	BaseURLGlobal = "https://api.moonshot.ai/v1"
)

// New creates a Kimi model over Moonshot's OpenAI-compatible endpoint.
// Additional options (including openai.WithBaseURL for the global endpoint or a
// proxy) are applied after the preset base URL and key.
func New(modelName, apiKey string, opts ...openai.Option) (*openai.Model, error) {
	base := []openai.Option{openai.WithBaseURL(BaseURL), openai.WithAPIKey(apiKey)}
	return openai.New(modelName, append(base, opts...)...)
}
