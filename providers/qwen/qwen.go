// Package qwen provides a preset constructor and typed options for Alibaba's
// Qwen models on DashScope, which expose an OpenAI-compatible endpoint. It is a
// thin layer over the openai adapter: it fixes the base URL and adds
// Qwen-specific options via the OpenAI extra_body mechanism.
package qwen

import (
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/providers/openai"
)

const (
	// BaseURL is the mainland China DashScope endpoint.
	BaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	// BaseURLIntl is the international (Singapore) DashScope endpoint.
	BaseURLIntl = "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"
)

// New creates a Qwen model over DashScope's OpenAI-compatible endpoint.
// Additional options (including openai.WithBaseURL for the international
// endpoint or a proxy) are applied after the preset base URL and key.
func New(modelName, apiKey string, opts ...openai.Option) (*openai.Model, error) {
	base := []openai.Option{openai.WithBaseURL(BaseURL), openai.WithAPIKey(apiKey)}
	return openai.New(modelName, append(base, opts...)...)
}

// WithThinking toggles Qwen3 hybrid thinking via extra_body.enable_thinking.
// DashScope requires this set explicitly (false) for non-streaming calls on
// reasoning-capable models.
func WithThinking(enabled bool) model.Option {
	return openai.WithExtraBody("enable_thinking", enabled)
}

// WithThinkingBudget caps the thinking tokens via extra_body.thinking_budget.
func WithThinkingBudget(tokens int) model.Option {
	return openai.WithExtraBody("thinking_budget", tokens)
}

// WithSearch toggles Qwen's built-in web search via extra_body.enable_search.
func WithSearch(enabled bool) model.Option {
	return openai.WithExtraBody("enable_search", enabled)
}
