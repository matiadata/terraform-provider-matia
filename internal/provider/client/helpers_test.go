package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveReplicationFrequency(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"hourly", "60"},
		{"daily", "1440"},
		{"manual", "manual"},
		{"cron", "cron"},
		{"60", "60"},
	}

	for _, tc := range tests {
		got, err := ResolveReplicationFrequency(tc.in)
		require.NoError(t, err, "ResolveReplicationFrequency(%q)", tc.in)
		require.Equal(t, tc.want, got, "ResolveReplicationFrequency(%q)", tc.in)
	}
}

func TestErrorFromV1NotFound_SchemaConfig(t *testing.T) {
	body := []byte(
		`{"statusCode":404,"message":{"code":"NotFound_SchemaConfig","message":"Cannot find entity 'SchemaConfig' with schema 'public'"}}`,
	)
	err := errorFromV1NotFound(body, ErrIntegrationNotFound)
	require.Contains(t, err.Error(), "schema not found in integration catalog")
}

func TestErrorFromV1NotFound_Integration(t *testing.T) {
	body := []byte(
		`{"code":"NotFound_Integration","message":"Cannot find entity 'Integration' with id '507f1f77bcf86cd799439011'"}`,
	)
	err := errorFromV1NotFound(body, ErrIntegrationNotFound)
	require.ErrorIs(t, err, ErrIntegrationNotFound)
}

func TestErrorFromV1NotFound_Asset(t *testing.T) {
	body := []byte(`{"statusCode":404,"message":"Asset not found for id: 507f1f77bcf86cd799439011"}`)
	err := errorFromV1NotFound(body, ErrAssetNotFound)
	require.ErrorIs(t, err, ErrAssetNotFound)
}

type testTimeoutError struct{}

func (testTimeoutError) Error() string   { return "timeout" }
func (testTimeoutError) Timeout() bool   { return true }
func (testTimeoutError) Temporary() bool { return false }

func TestIsHTTPTimeoutError(t *testing.T) {
	require.True(t, isHTTPTimeoutError(context.DeadlineExceeded))
	require.True(t, isHTTPTimeoutError(testTimeoutError{}))
	require.False(t, isHTTPTimeoutError(errors.New("connection refused")))
}

func syscallOpError(op string, err error) error {
	return &net.OpError{Op: op, Net: "tcp", Err: os.NewSyscallError(op, err)}
}

func TestIsTransientNetworkError(t *testing.T) {
	require.True(t, isTransientNetworkError(syscallOpError("read", syscall.ECONNRESET)))
	require.True(t, isTransientNetworkError(syscallOpError("write", syscall.EPIPE)))
	require.True(t, isTransientNetworkError(syscallOpError("dial", syscall.ECONNREFUSED)))
	require.True(t, isTransientNetworkError(testTimeoutError{}))
	require.False(t, isTransientNetworkError(errors.New("invalid JSON")))
}

func TestRequestNeverReachedServer(t *testing.T) {
	require.True(t, requestNeverReachedServer(syscallOpError("dial", syscall.ECONNREFUSED)))
	require.True(t, requestNeverReachedServer(syscallOpError("dial", syscall.ETIMEDOUT)))
	require.True(t, requestNeverReachedServer(&net.DNSError{Err: "no such host", IsNotFound: true}))
	require.True(t, requestNeverReachedServer(&net.DNSError{Err: "server misbehaving", IsTemporary: true}))
	require.True(t, requestNeverReachedServer(errors.New("net/http: TLS handshake timeout")))
	require.False(t, requestNeverReachedServer(syscallOpError("read", syscall.ECONNRESET)))
	require.False(t, requestNeverReachedServer(nil))
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)

	delay, ok := parseRetryAfter("120", now)
	require.True(t, ok)
	require.Equal(t, 2*time.Minute, delay)

	delay, ok = parseRetryAfter("  5  ", now)
	require.True(t, ok)
	require.Equal(t, 5*time.Second, delay)

	delay, ok = parseRetryAfter(now.Add(30*time.Second).Format(http.TimeFormat), now)
	require.True(t, ok)
	require.Equal(t, 30*time.Second, delay)

	// A date already in the past is honored as "retry now", not treated as absent.
	delay, ok = parseRetryAfter(now.Add(-time.Hour).Format(http.TimeFormat), now)
	require.True(t, ok)
	require.Zero(t, delay)

	for _, unusable := range []string{"", "   ", "-1", "soon", "1.5"} {
		_, ok = parseRetryAfter(unusable, now)
		require.False(t, ok, "value %q should not be usable", unusable)
	}
}

func TestCreatedButUnreadable(t *testing.T) {
	cause := errors.New("connection reset")
	err := createdButUnreadable("asset", "asset-42", "Remove it before retrying.", cause)

	require.ErrorIs(t, err, cause)
	// The id is the whole point: without it the created resource is unrecoverable.
	require.Contains(t, err.Error(), "asset-42")
	require.Contains(t, err.Error(), "duplicate")
}

func TestShouldRetryNetworkError(t *testing.T) {
	refused := syscallOpError("dial", syscall.ECONNREFUSED)
	dialTimeout := syscallOpError("dial", syscall.ETIMEDOUT)
	reset := syscallOpError("read", syscall.ECONNRESET)

	require.True(t, shouldRetryNetworkError(http.MethodPost, refused), "refused never reached server: safe for POST")
	require.True(t, shouldRetryNetworkError(http.MethodPost, dialTimeout), "dial timeout never reached server")
	require.True(t, shouldRetryNetworkError(http.MethodGet, reset), "reset: retryable for idempotent methods")
	require.False(t, shouldRetryNetworkError(http.MethodPost, reset), "reset must not replay a POST")
	require.False(t, shouldRetryNetworkError(http.MethodGet, errors.New("invalid JSON")))
}

func TestShouldRetryStatus(t *testing.T) {
	require.True(t, shouldRetryStatus(http.MethodPost, http.StatusTooManyRequests), "429 rejected pre-processing")
	require.True(t, shouldRetryStatus(http.MethodDelete, http.StatusBadGateway))
	require.False(t, shouldRetryStatus(http.MethodPost, http.StatusInternalServerError), "5xx unsafe for POST")
	require.False(t, shouldRetryStatus(http.MethodGet, http.StatusBadRequest))
}

func TestIsRetryableHTTPStatus(t *testing.T) {
	require.True(t, isRetryableHTTPStatus(http.StatusInternalServerError))
	require.True(t, isRetryableHTTPStatus(http.StatusTooManyRequests))
	require.False(t, isRetryableHTTPStatus(http.StatusBadRequest))
	require.False(t, isRetryableHTTPStatus(http.StatusConflict))
}
