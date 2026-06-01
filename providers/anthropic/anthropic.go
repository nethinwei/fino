// Package anthropic implements a model.Model adapter for the Anthropic Messages
// API and any Anthropic-compatible endpoint (such as DeepSeek's
// https://api.deepseek.com/anthropic). It depends only on the standard library.
package anthropic

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

const (
	defaultBaseURL    = "https://api.anthropic.com"
	defaultVersion    = "2023-06-01"
	defaultMaxTokens  = 4096
	messagesURLSuffix = "/v1/messages"
)

// Model is an Anthropic-compatible messages model. Construct it with New.
type Model struct {
	model          string
	baseURL        string
	apiKey         string
	version        string
	maxTokens      int
	httpClient     *http.Client
	connectTimeout time.Duration
	maxRetries     int
	retryBackoff   time.Duration
}

// Option configures a Model.
type Option func(*Model)

// WithBaseURL overrides the API base URL. For DeepSeek's Anthropic-compatible
// endpoint, pass "https://api.deepseek.com/anthropic".
func WithBaseURL(url string) Option {
	return func(m *Model) { m.baseURL = strings.TrimRight(url, "/") }
}

// WithAPIKey sets the key sent in the x-api-key header.
func WithAPIKey(key string) Option { return func(m *Model) { m.apiKey = key } }

// WithVersion overrides the anthropic-version header (default 2023-06-01).
func WithVersion(v string) Option {
	return func(m *Model) {
		if v != "" {
			m.version = v
		}
	}
}

// WithMaxTokens sets the default max_tokens used when a run does not specify
// one. The Anthropic API requires max_tokens, so a positive default is kept.
func WithMaxTokens(n int) Option {
	return func(m *Model) {
		if n > 0 {
			m.maxTokens = n
		}
	}
}

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
		version:        defaultVersion,
		maxTokens:      defaultMaxTokens,
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

type msgRequest struct {
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens"`
	System        string          `json:"system,omitempty"`
	Messages      []msgMessage    `json:"messages"`
	Tools         []msgTool       `json:"tools,omitempty"`
	Temperature   *float32        `json:"temperature,omitempty"`
	TopP          *float32        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Thinking      *thinkingConfig `json:"thinking,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
}

type thinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type (
	thinkingKey      struct{}
	topKKey          struct{}
	stopSequencesKey struct{}
)

// WithThinking enables extended thinking with the given token budget. It is a
// per-call model.Option, usable via runner.WithModelOptions.
func WithThinking(budgetTokens int) model.Option {
	return model.WithExtra(thinkingKey{}, budgetTokens)
}

// WithTopK sets the top_k sampling parameter. It is a per-call model.Option.
func WithTopK(k int) model.Option {
	return model.WithExtra(topKKey{}, k)
}

// WithStopSequences sets the stop_sequences parameter.
func WithStopSequences(seqs ...string) model.Option {
	return model.WithExtra(stopSequencesKey{}, seqs)
}

type msgMessage struct {
	Role    string     `json:"role"`
	Content []reqBlock `json:"content"`
}

type reqBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type msgTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type msgResponse struct {
	Content []respBlock `json:"content"`
}

type respBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
}

// buildRequest assembles the request body shared by Generate and Stream.
func (m *Model) buildRequest(messages []message.Message, tools []tool.Info, opts []model.Option, stream bool) msgRequest {
	cfg := model.ApplyOptions(opts...)
	system, msgs := toAnthropicMessages(messages)
	maxTokens := m.maxTokens
	if cfg.MaxTokens != nil {
		maxTokens = *cfg.MaxTokens
	}
	req := msgRequest{
		Model:       m.model,
		MaxTokens:   maxTokens,
		System:      system,
		Messages:    msgs,
		Tools:       toAnthropicTools(tools),
		Temperature: cfg.Temperature,
		TopP:        cfg.TopP,
		Stream:      stream,
	}
	if v, ok := model.ExtraValue[int](cfg, thinkingKey{}); ok {
		req.Thinking = &thinkingConfig{Type: "enabled", BudgetTokens: v}
	}
	if v, ok := model.ExtraValue[int](cfg, topKKey{}); ok {
		req.TopK = &v
	}
	if v, ok := model.ExtraValue[[]string](cfg, stopSequencesKey{}); ok {
		req.StopSequences = v
	}
	return req
}

// post sends a messages request and returns the response, retrying transient
// failures. It returns an error for transport failures and non-2xx status
// codes.
func (m *Model) post(ctx context.Context, reqBody msgRequest) (*http.Response, error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	return httpx.Send(ctx, m.maxRetries, m.retryBackoff, func() (*http.Response, error, bool) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+messagesURLSuffix, bytes.NewReader(body))
		if err != nil {
			return nil, err, false
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", m.apiKey)
		req.Header.Set("anthropic-version", m.version)
		resp, err := m.httpClient.Do(req)
		if err != nil {
			return nil, err, true
		}
		if resp.StatusCode/100 != 2 {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data))), httpx.RetryableStatus(resp.StatusCode)
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
	var mr msgResponse
	if err := json.Unmarshal(data, &mr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	msg := message.Assistant(respBlocksToBlocks(mr.Content)...)
	return &msg, nil
}

// respBlocksToBlocks converts Anthropic response blocks into fino blocks.
func respBlocksToBlocks(content []respBlock) []message.Block {
	blocks := make([]message.Block, 0, len(content))
	for _, b := range content {
		switch b.Type {
		case "text":
			blocks = append(blocks, message.NewText(b.Text))
		case "thinking":
			blocks = append(blocks, message.NewThinking(b.Thinking))
		case "tool_use":
			input := b.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			blocks = append(blocks, message.NewToolUse(b.ID, b.Name, input))
		}
	}
	return blocks
}
