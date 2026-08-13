package message

// Usage records token accounting for a single model response. Providers
// normalize their usage fields into this shared shape so consumers (budgets,
// tracing, cache optimizers) can reason about cost and cache hits without
// provider branches.
//
// InputTokens always counts the total input, including any cache-hit portion.
// CacheReadTokens is the cache-hit subset of InputTokens, so the cache hit rate
// is CacheReadTokens / InputTokens for every provider. CacheWriteTokens is the
// portion of input newly written to the provider cache; providers that do not
// report cache writes (e.g. OpenAI, DeepSeek) leave it at zero.
type Usage struct {
	InputTokens      int `json:"input_tokens,omitempty"`
	OutputTokens     int `json:"output_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}
