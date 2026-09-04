package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestClient disables retry backoff: the retry paths under test assert on how many
// attempts were made, never on how long the client slept between them.
func newTestClient(baseURL string) *MatiaClient {
	c := NewMatiaClient(baseURL, "key")
	c.retryBaseDelay = 0
	return c
}

func TestDoRequest_RetriesTransient5xx(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"code":"success","data":{"id":"asset-1","name":"n","type":"snowflake"}}`)
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/assets/asset-1", nil)
	require.NoError(t, err)

	resp, err := client.doRequest(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, int32(2), attempts.Load())
}

func TestDoRequest_DoesNotRetryPostOn5xx(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/assets", nil)
	require.NoError(t, err)

	resp, err := client.doRequest(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// A 5xx after a POST may mean the create already took effect; retrying would duplicate it.
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Equal(t, int32(1), attempts.Load())
}

func TestDoRequest_RetriesPostOn429(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"code":"success","data":{"id":"asset-1"}}`)
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/assets", nil)
	require.NoError(t, err)

	// 429 means the request was rejected without being processed, so a POST is safe to retry.
	resp, err := client.doRequest(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.Equal(t, int32(2), attempts.Load())
}

func TestDoRequest_PrefersRetryAfterOverBackoff(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	// Backoff long enough that ignoring Retry-After would be unmistakable in the elapsed time.
	client.retryBaseDelay = 2 * time.Second
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/assets/asset-1", nil)
	require.NoError(t, err)

	start := time.Now()
	resp, err := client.doRequest(req)
	elapsed := time.Since(start)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, int32(2), attempts.Load())
	require.Less(t, elapsed, time.Second, "Retry-After: 0 should override the 2s backoff")
}

func TestRetryDelayFor(t *testing.T) {
	client := NewMatiaClient("https://api.example.test/v1", "key")

	withRetryAfter := func(value string) *http.Response {
		return &http.Response{Header: http.Header{"Retry-After": []string{value}}}
	}

	require.Equal(t, 3*time.Second, client.retryDelayFor(withRetryAfter("3"), 1))
	require.Equal(t, maxRetryAfterDelay, client.retryDelayFor(withRetryAfter("86400"), 1),
		"an oversized Retry-After is capped so it cannot stall an apply")
	require.Equal(t, requestRetryBaseDelay, client.retryDelayFor(&http.Response{Header: http.Header{}}, 1),
		"no Retry-After falls back to exponential backoff")
	require.Equal(t, 2*requestRetryBaseDelay, client.retryDelayFor(withRetryAfter("not-a-date"), 2),
		"an unparseable Retry-After falls back to exponential backoff")
}

func TestDoRequest_RetriesDeleteOn5xx(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	req, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/assets/asset-1", nil)
	require.NoError(t, err)

	// DELETE is idempotent, so retrying on 5xx is safe.
	resp, err := client.doRequest(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, int32(2), attempts.Load())
}

type networkErrorTransport struct {
	attempts *atomic.Int32
	err      error
}

func (t *networkErrorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.attempts.Add(1)
	return nil, t.err
}

func TestDoRequest_DoesNotRetryPostOnMidflightNetworkError(t *testing.T) {
	var attempts atomic.Int32
	client := newTestClient("http://matia.invalid/v1")
	client.HTTPClient.Transport = &networkErrorTransport{
		attempts: &attempts,
		err:      syscallOpError("read", syscall.ECONNRESET),
	}
	req, err := http.NewRequest(http.MethodPost, "http://matia.invalid/v1/assets", nil)
	require.NoError(t, err)

	// A reset after the request was sent may mean the create already took effect; replaying would duplicate it.
	_, err = client.doRequest(req)
	require.Error(t, err)
	require.Equal(t, int32(1), attempts.Load())
}

func TestDoRequest_RetriesGetOnMidflightNetworkError(t *testing.T) {
	var attempts atomic.Int32
	client := newTestClient("http://matia.invalid/v1")
	client.HTTPClient.Transport = &networkErrorTransport{
		attempts: &attempts,
		err:      syscallOpError("read", syscall.ECONNRESET),
	}
	req, err := http.NewRequest(http.MethodGet, "http://matia.invalid/v1/assets/asset-1", nil)
	require.NoError(t, err)

	// GET is idempotent, so a mid-flight reset is retried to exhaustion.
	_, err = client.doRequest(req)
	require.Error(t, err)
	require.Equal(t, int32(requestRetryMaxAttempts), attempts.Load())
}

func TestDoRequest_RetriesPostOnDialTimeout(t *testing.T) {
	var attempts atomic.Int32
	client := newTestClient("http://matia.invalid/v1")
	client.HTTPClient.Transport = &networkErrorTransport{
		attempts: &attempts,
		err:      syscallOpError("dial", syscall.ETIMEDOUT),
	}
	req, err := http.NewRequest(http.MethodPost, "http://matia.invalid/v1/assets", nil)
	require.NoError(t, err)

	// A dial timeout fails before any request byte is written, so replaying a POST cannot duplicate the create.
	_, err = client.doRequest(req)
	require.Error(t, err)
	require.Equal(t, int32(requestRetryMaxAttempts), attempts.Load())
}

func TestDoRequest_DoesNotRetry4xx(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":"BadRequest","message":"invalid"}`)
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/assets/asset-1", nil)
	require.NoError(t, err)

	resp, err := client.doRequest(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, int32(1), attempts.Load())
}
