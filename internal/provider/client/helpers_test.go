package client

import (
	"context"
	"errors"
	"net/http"
	"testing"

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

func TestIsTransientNetworkError(t *testing.T) {
	require.True(t, isTransientNetworkError(errors.New("connection reset by peer")))
	require.True(t, isTransientNetworkError(testTimeoutError{}))
	require.False(t, isTransientNetworkError(errors.New("invalid JSON")))
}

func TestIsRetryableHTTPStatus(t *testing.T) {
	require.True(t, isRetryableHTTPStatus(http.StatusInternalServerError))
	require.True(t, isRetryableHTTPStatus(http.StatusTooManyRequests))
	require.False(t, isRetryableHTTPStatus(http.StatusBadRequest))
	require.False(t, isRetryableHTTPStatus(http.StatusConflict))
}
