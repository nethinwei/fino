// Package openai implements a model.Model adapter for the OpenAI Chat
// Completions API and any OpenAI-compatible endpoint (such as DeepSeek's
// https://api.deepseek.com). It depends only on the standard library.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/providers/internal/httpx"
	"github.com/nethinwei/fino/tool"
)

const defaultBaseURL = "https://api.openai.com/v1"

// Model is an OpenAI-compatible chat model. Construct it with New.
type Model struct {
	model          string
	baseURL        string
	apiKey         string
	httpClient     *http.Client
	connectTimeout time.Duration
	maxRetries     int
	retryBackoff   time.Duration
}

// Option configures a Model.
type Option func(*Model)

// WithBaseURL overrides the API base URL. For DeepSeek's OpenAI-compatible
// endpoint, pass "https://api.deepseek.com".
func WithBaseURL(url string) Option {
	return func(m *Model) { m.baseURL = strings.TrimRight(url, "/") }
}

// WithAPIKey sets the bearer token sent in the Authorization header.
func WithAPIKey(key string) Option { return func(m *Model) { m.apiKey = key } }

// WithHTTPClient sets the HTTP client used for requests. A nil client is
// ignored, preserving the default. A client set here takes precedence over
// WithTimeout, which only tunes the default client.
func WithHTTPClient(c *http.Client) Option {
	return func(m *Model) {
		if c != nil {
			m.httpClient = c
		}
	}
}

// WithTimeout bounds connection setup (dial and TLS handshake) for the default
// client. It deliberately does not bound response-body reads, so streaming
// responses are never cut off. It has no effect when WithHTTPClient is used.
func WithTimeout(d time.Duration) Option {
	return func(m *Model) { m.connectTimeout = d }
}

// WithMaxRetries sets how many additional attempts are made after a transient
// failure (transport errors, HTTP 429, and 5xx) before giving up. Zero
// disables retries.
func WithMaxRetries(n int) Option {
	return func(m *Model) {
		if n >= 0 {
			m.maxRetries = n
		}
	}
}

// New creates a Model targeting the given model name (for DeepSeek, e.g.
// "deepseek-v4-flash"). It returns an error if the model name is empty.
func New(modelName string, opts ...Option) (*Model, error) {
	if strings.TrimSpace(modelName) == "" {
		return nil, errors.New("model name is required")
	}
	m := &Model{
		model:          modelName,
		baseURL:        defaultBaseURL,
		connectTimeout: httpx.DefaultConnectTimeout,
		maxRetries:     httpx.DefaultMaxRetries,
		retryBackoff:   httpx.DefaultRetryBackoff,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	if m.httpClient == nil {
		m.httpClient = httpx.NewClient(m.connectTimeout)
	}
	return m, nil
}

type chatRequest struct {
	Model            string        `json:"model"`
	Messages         []chatMessage `json:"messages"`
	Tools            []chatTool    `json:"tools,omitempty"`
	Temperature      *float32      `json:"temperature,omitempty"`
	TopP             *float32      `json:"top_p,omitempty"`
	MaxTokens        *int          `json:"max_tokens,omitempty"`
	Stop             []string      `json:"stop,omitempty"`
	Seed             *int          `json:"seed,omitempty"`
	FrequencyPenalty *float32      `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float32      `json:"presence_penalty,omitempty"`
	ReasoningEffort  string        `json:"reasoning_effort,omitempty"`
	Stream           bool          `json:"stream,omitempty"`

	// extraBody carries provider-specific top-level fields merged in by
	// MarshalJSON. It is unexported so it never serializes directly.
	extraBody map[string]any
}

// MarshalJSON serializes the request and merges any extra body fields as
// top-level keys, mirroring the OpenAI SDK's extra_body mechanism.
func (r chatRequest) MarshalJSON() ([]byte, error) {
	type alias chatRequest
	data, err := json.Marshal(alias(r))
	if err != nil || len(r.extraBody) == 0 {
		return data, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	for k, v := range r.extraBody {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		m[k] = b
	}
	return json.Marshal(m)
}

type (
	reasoningEffortKey struct{}
	stopKey            struct{}
	seedKey            struct{}
	freqPenaltyKey     struct{}
	presPenaltyKey     struct{}
	extraBodyKey       struct{ field string }
)

// WithReasoningEffort sets the reasoning_effort parameter (e.g. "low",
// "medium", "high") for reasoning-capable models such as the o-series or
// deepseek-reasoner.
func WithReasoningEffort(effort string) model.Option {
	return model.WithExtra(reasoningEffortKey{}, effort)
}

// WithStop sets stop sequences.
func WithStop(stop ...string) model.Option { return model.WithExtra(stopKey{}, stop) }

// WithSeed sets the sampling seed for reproducible outputs.
func WithSeed(seed int) model.Option { return model.WithExtra(seedKey{}, seed) }

// WithFrequencyPenalty sets the frequency_penalty parameter.
func WithFrequencyPenalty(v float32) model.Option { return model.WithExtra(freqPenaltyKey{}, v) }

// WithPresencePenalty sets the presence_penalty parameter.
func WithPresencePenalty(v float32) model.Option { return model.WithExtra(presPenaltyKey{}, v) }

// WithExtraBody adds an arbitrary top-level field to the request body. It is
// the OpenAI-compatible escape hatch for vendor-specific parameters (DeepSeek,
// Kimi, GLM, Qwen, ...); vendor packages wrap it in typed options. Repeated
// calls with different fields accumulate.
func WithExtraBody(field string, value any) model.Option {
	return model.WithExtra(extraBodyKey{field: field}, value)
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []chatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

// openaiContentPart is one element of a multimodal content array. OpenAI
// accepts a string for text-only content and an array for multimodal content.
type openaiContentPart struct {
	Type       string            `json:"type"`
	Text       string            `json:"text,omitempty"`
	ImageURL   *openaiImageURL   `json:"image_url,omitempty"`
	InputAudio *openaiInputAudio `json:"input_audio,omitempty"`
}

type openaiImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type openaiInputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// respAudio carries a model-generated audio payload (GPT-4o audio output).
type respAudio struct {
	ID         string `json:"id"`
	Data       string `json:"data"`
	Transcript string `json:"transcript"`
	Format     string `json:"format"`
	ExpiresAt  int64  `json:"expires_at"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []chatToolCall  `json:"tool_calls"`
			Audio            *respAudio      `json:"audio"`
		} `json:"message"`
	} `json:"choices"`
}

// buildRequest assembles the chat request body shared by Generate and Stream.
func (m *Model) buildRequest(messages []message.Message, tools []tool.Info, opts []model.Option, stream bool) chatRequest {
	cfg := model.ApplyOptions(opts...)
	req := chatRequest{
		Model:       m.model,
		Messages:    toOpenAIMessages(messages),
		Tools:       toOpenAITools(tools),
		Temperature: cfg.Temperature,
		TopP:        cfg.TopP,
		MaxTokens:   cfg.MaxTokens,
		Stream:      stream,
	}
	applyExtras(&req, cfg)
	return req
}

// applyExtras reads provider-specific options from cfg into the request.
func applyExtras(req *chatRequest, cfg model.Config) {
	if v, ok := model.ExtraValue[string](cfg, reasoningEffortKey{}); ok {
		req.ReasoningEffort = v
	}
	if v, ok := model.ExtraValue[[]string](cfg, stopKey{}); ok {
		req.Stop = v
	}
	if v, ok := model.ExtraValue[int](cfg, seedKey{}); ok {
		req.Seed = &v
	}
	if v, ok := model.ExtraValue[float32](cfg, freqPenaltyKey{}); ok {
		req.FrequencyPenalty = &v
	}
	if v, ok := model.ExtraValue[float32](cfg, presPenaltyKey{}); ok {
		req.PresencePenalty = &v
	}
	for k, v := range cfg.Extra {
		if eb, ok := k.(extraBodyKey); ok {
			if req.extraBody == nil {
				req.extraBody = map[string]any{}
			}
			req.extraBody[eb.field] = v
		}
	}
}

// post sends a chat request and returns the raw response body, retrying
// transient failures. It returns an error for transport failures and non-2xx
// status codes.
func (m *Model) post(ctx context.Context, reqBody chatRequest) (*http.Response, error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	return httpx.Send(ctx, m.maxRetries, m.retryBackoff, func() (*http.Response, error, bool) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err, false
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
		resp, err := m.httpClient.Do(req)
		if err != nil {
			return nil, err, true
		}
		if resp.StatusCode/100 != 2 {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("openai: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data))), httpx.RetryableStatus(resp.StatusCode)
		}
		return resp, nil, false
	})
}

// Generate produces a single model response synchronously.
func (m *Model) Generate(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) (*message.Message, error) {
	resp, err := m.post(ctx, m.buildRequest(messages, tools, opts, false))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return nil, errors.New("response contained no choices")
	}
	choice := cr.Choices[0].Message
	blocks := make([]message.Block, 0, 2+len(choice.ToolCalls))
	if choice.ReasoningContent != "" {
		blocks = append(blocks, message.NewThinking(choice.ReasoningContent))
	}
	blocks = append(blocks, respContentToBlocks(choice.Content)...)
	if choice.Audio != nil {
		blocks = append(blocks, audioRespToBlock(choice.Audio))
	}
	for _, tc := range choice.ToolCalls {
		blocks = append(blocks, message.NewToolUse(tc.ID, tc.Function.Name, json.RawMessage(tc.Function.Arguments)))
	}
	msg := message.Assistant(blocks...)
	return &msg, nil
}

// Capabilities reports the modalities and sources this adapter supports. The
// Chat Completions API accepts image and audio input (base64/url) and can
// produce text and audio output. Video and file inputs have no standard
// content part and degrade to text. Values are provider-wide defaults.
func (m *Model) Capabilities() model.CapabilitiesInfo {
	return model.CapabilitiesInfo{
		InputModalities:     []message.BlockType{message.TypeText, message.TypeImage, message.TypeAudio},
		InputSources:        []message.SourceType{message.SourceBase64, message.SourceURL},
		OutputModalities:    []message.BlockType{message.TypeText, message.TypeAudio},
		SupportsPromptCache: true,
		SupportsStreaming:   true,
	}
}
