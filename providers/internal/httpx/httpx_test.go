package httpx

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestRetryableStatus(t *testing.T) {
	cases := map[int]bool{
		http.StatusOK:                  false,
		http.StatusBadRequest:          false,
		http.StatusTooManyRequests:     true,
		http.StatusInternalServerError: true,
		http.StatusBadGateway:          true,
		http.StatusServiceUnavailable:  true,
	}
	for code, want := range cases {
		if got := RetryableStatus(code); got != want {
			t.Errorf("RetryableStatus(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestSendRetriesThenSucceeds(t *testing.T) {
	attempts := 0
	want := &http.Response{StatusCode: 200}
	got, err := Send(context.Background(), 3, time.Millisecond, func() (*http.Response, error, bool) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("transient"), true
		}
		return want, nil, false
	})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if got != want {
		t.Fatalf("got %p, want %p", got, want)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestSendExhaustsRetries(t *testing.T) {
	attempts := 0
	wantErr := errors.New("down")
	_, err := Send(context.Background(), 2, time.Millisecond, func() (*http.Response, error, bool) {
		attempts++
		return nil, wantErr, true
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if attempts != 3 { // initial attempt + 2 retries
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestSendDoesNotRetryNonRetryable(t *testing.T) {
	attempts := 0
	_, err := Send(context.Background(), 5, time.Millisecond, func() (*http.Response, error, bool) {
		attempts++
		return nil, errors.New("bad request"), false
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry)", attempts)
	}
}

func TestSendHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	_, err := Send(ctx, 5, 50*time.Millisecond, func() (*http.Response, error, bool) {
		attempts++
		cancel() // cancel so the backoff wait returns ctx.Err
		return nil, errors.New("transient"), true
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestNewClientSetsConnectionTimeouts(t *testing.T) {
	c := NewClient(7 * time.Second)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", c.Transport)
	}
	if tr.TLSHandshakeTimeout != 7*time.Second {
		t.Fatalf("TLSHandshakeTimeout = %v, want 7s", tr.TLSHandshakeTimeout)
	}
	if c.Timeout != 0 {
		t.Fatalf("client.Timeout = %v, want 0 (must not bound streaming body)", c.Timeout)
	}
}
