package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

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

	client := NewMatiaClient(server.URL+"/v1", "key")
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/assets/asset-1", nil)
	require.NoError(t, err)

	resp, err := client.doRequest(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, int32(2), attempts.Load())
}

func TestDoRequest_DoesNotRetry4xx(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":"BadRequest","message":"invalid"}`)
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/assets/asset-1", nil)
	require.NoError(t, err)

	resp, err := client.doRequest(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, int32(1), attempts.Load())
}
