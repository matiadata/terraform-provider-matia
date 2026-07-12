package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateHybridDeploymentAgent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agent-gateway/hybrid-deployment-agents":
			var body CreateHybridDeploymentAgentRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "my-agent", body.Name)
			assert.Equal(t, "edge agent", body.Description)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"code":"success","data":{"id":"agent-123","token":"secret-token-abc"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agent-gateway/hybrid-deployment-agents/agent-123":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(
				w,
				`{"code":"success","data":{"id":"agent-123","name":"my-agent","description":"edge agent","createdAt":"2026-06-12T10:00:00Z"}}`,
			)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	agent, err := client.HybridDeploymentAgents.Create(context.Background(), CreateHybridDeploymentAgentRequest{
		Name:        "my-agent",
		Description: "edge agent",
	})
	require.NoError(t, err)
	require.Equal(t, "agent-123", agent.ID)
	require.Equal(t, "my-agent", agent.Name)
	require.Equal(t, "edge agent", agent.Description)
	require.Equal(t, "2026-06-12T10:00:00Z", agent.CreatedAt)
	require.Equal(t, "secret-token-abc", agent.Token)
}

func TestCreateHybridDeploymentAgent_MissingID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"code":"success","data":{"id":""}}`)
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	_, err := client.HybridDeploymentAgents.Create(context.Background(), CreateHybridDeploymentAgentRequest{
		Name: "my-agent",
	})
	require.Error(t, err)
	require.Equal(t, "API response missing data", err.Error())
}

func TestGetHybridDeploymentAgent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/agent-gateway/hybrid-deployment-agents/agent-123", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(
			w,
			`{"code":"success","data":{"id":"agent-123","name":"my-agent","createdAt":"2026-06-12T10:00:00Z"}}`,
		)
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	agent, err := client.HybridDeploymentAgents.Get(context.Background(), "agent-123")
	require.NoError(t, err)
	require.Equal(t, "agent-123", agent.ID)
	require.Equal(t, "my-agent", agent.Name)
}

func TestGetHybridDeploymentAgent_NotFound(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"not found"}`)
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	_, err := client.HybridDeploymentAgents.Get(context.Background(), "missing")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrHybridDeploymentAgentNotFound)
}

func TestGetHybridDeploymentAgent_SoftNotFound(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(
			w,
			`{"code":"error","message":"Agent not found","error":{"context":{"agentId":"missing"},"name":"AgentNotFoundError","code":"AGENT_NOT_FOUND"}}`,
		)
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	_, err := client.HybridDeploymentAgents.Get(context.Background(), "missing")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrHybridDeploymentAgentNotFound)
}

func TestGetHybridDeploymentAgent_MissingID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"code":"success","data":{"name":"my-agent"}}`)
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	_, err := client.HybridDeploymentAgents.Get(context.Background(), "agent-123")
	require.Error(t, err)
	require.Equal(t, "API response missing hybrid deployment agent id", err.Error())
}

func TestDeleteHybridDeploymentAgent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/v1/agent-gateway/hybrid-deployment-agents/agent-123", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	err := client.HybridDeploymentAgents.Delete(context.Background(), "agent-123")
	require.NoError(t, err)
}

func TestDeleteHybridDeploymentAgent_TimeoutThenNotFound(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			assert.Equal(t, "/v1/agent-gateway/hybrid-deployment-agents/agent-123", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"not found"}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	client.HTTPClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodDelete {
				return nil, testTimeoutError{}
			}
			return http.DefaultTransport.RoundTrip(req)
		}),
	}

	err := client.HybridDeploymentAgents.Delete(context.Background(), "agent-123")
	require.NoError(t, err)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDeleteHybridDeploymentAgent_TimeoutStillExists(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"code":"success","data":{"id":"agent-123","name":"my-agent"}}`)
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	client.HTTPClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodDelete {
				return nil, testTimeoutError{}
			}
			return http.DefaultTransport.RoundTrip(req)
		}),
	}

	err := client.HybridDeploymentAgents.Delete(context.Background(), "agent-123")
	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout")
}
