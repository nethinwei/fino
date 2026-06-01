// Package httpx holds the HTTP transport tuning shared by the provider
// adapters: a streaming-safe client with connection-phase timeouts and a retry
// loop for transient failures. Centralizing it keeps the retry/backoff policy
// in one place rather than duplicated across each adapter's post method.
package httpx

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Tuning defaults shared by the adapters. They are conservative: the timeout
// bounds only connection establishment (dial + TLS handshake), never body
// reads, so streaming responses are never cut off mid-flight.
const (
	// DefaultConnectTimeout bounds dial and TLS handshake.
	DefaultConnectTimeout = 30 * time.Second
	// DefaultMaxRetries is the number of additional attempts after the first.
	DefaultMaxRetries = 2
	// DefaultRetryBackoff is the base delay; it doubles each retry.
	DefaultRetryBackoff = 300 * time.Millisecond
)

// NewClient returns an http.Client whose transport bounds connection setup with
// connectTimeout (dial and TLS handshake) while leaving response-body reads
// unbounded, which is required for long-lived streaming responses. A
// non-positive connectTimeout disables those bounds.
func NewClient(connectTimeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   connectTimeout,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

// RetryableStatus reports whether an HTTP status code warrants a retry: 429
// (rate limited) and any 5xx (server-side) response.
func RetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// Attempt performs one request attempt. It returns the response (on success),
// an error (on transport failure or a non-2xx status the caller has already
// formatted), and whether that error is transient and worth retrying.
type Attempt func() (*http.Response, error, bool)

// Send runs attempt, retrying transient failures up to maxRetries times with
// exponential backoff starting at backoff. Backoff waits honor ctx
// cancellation. It returns the first successful response or the last error.
func Send(ctx context.Context, maxRetries int, backoff time.Duration, attempt Attempt) (*http.Response, error) {
	for i := 0; ; i++ {
		resp, err, retryable := attempt()
		if err == nil || !retryable || i >= maxRetries {
			return resp, err
		}
		timer := time.NewTimer(backoff << i)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
