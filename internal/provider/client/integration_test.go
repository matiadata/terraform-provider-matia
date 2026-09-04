package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testIntegrationID     = "507f1f77bcf86cd799439011"
	testSourceID          = "source-1"
	testDestinationID     = "dest-1"
	testDestinationSchema = "raw"
)

func testIntegrationJSON() string {
	return `{"code":"success","data":{"id":"` + testIntegrationID + `","name":"Postgres → Snowflake","paused":false,"source":{"id":"` + testSourceID + `","name":"pg","type":"postgres"},"destination":{"id":"` + testDestinationID + `","name":"sf","type":"snowflake"},"replicationFrequency":"manual","destinationSchema":"` + testDestinationSchema + `"}}`
}

func TestIntegrationsClient_CreateWithAgentID(t *testing.T) {
	t.Parallel()

	const (
		integrationID = testIntegrationID
		sourceID      = testSourceID
		destID        = testDestinationID
		agentID       = "agent-123"
		schema        = testDestinationSchema
	)

	var createBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/integrations":
			createBody, _ = io.ReadAll(r.Body)

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"success","data":{"id":"` + integrationID + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/integrations/"+integrationID:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(testIntegrationJSON()))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	created, err := client.Integrations.Create(context.Background(), CreateIntegrationRequest{
		SourceID:             sourceID,
		DestinationID:        destID,
		ReplicationFrequency: "manual",
		DestinationSchema:    schema,
		AgentID:              agentID,
	})
	require.NoError(t, err)
	require.Equal(t, integrationID, created.ID)

	var createReq CreateIntegrationRequest
	require.NoError(t, json.Unmarshal(createBody, &createReq))
	require.Equal(t, agentID, createReq.AgentID)
}

func TestIntegrationsClient_ModifyWithAgentID(t *testing.T) {
	t.Parallel()

	const (
		integrationID = "507f1f77bcf86cd799439011"
		agentID       = "agent-456"
	)

	var patchBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/integrations/"+integrationID:
			body, _ := io.ReadAll(r.Body)
			patchBody = body
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	err := client.Integrations.Modify(context.Background(), integrationID, ModifyIntegrationRequest{
		AgentID: agentID,
	})
	require.NoError(t, err)

	var req ModifyIntegrationRequest
	require.NoError(t, json.Unmarshal(patchBody, &req))
	require.Equal(t, agentID, req.AgentID)
}

func TestIntegrationsClient_ModifyClearsAgentID(t *testing.T) {
	t.Parallel()

	const integrationID = "507f1f77bcf86cd799439011"

	var patchBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/integrations/"+integrationID:
			body, _ := io.ReadAll(r.Body)
			patchBody = body
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	err := client.Integrations.Modify(context.Background(), integrationID, ModifyIntegrationRequest{
		AgentID: (*string)(nil),
	})
	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(patchBody, &req))
	value, ok := req["agentId"]
	require.True(t, ok, "expected agentId to be present")
	require.Nil(t, value)
}

func TestIntegrationsClient_CreateGetModifyDelete(t *testing.T) {
	t.Parallel()

	const (
		integrationID = testIntegrationID
		sourceID      = testSourceID
		destID        = testDestinationID
		schema        = testDestinationSchema
	)
	store := map[string]string{
		integrationID: testIntegrationJSON(),
	}
	var mu sync.Mutex
	var createBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/integrations":
			createBody, _ = io.ReadAll(r.Body)

			store[integrationID] = testIntegrationJSON()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"success","data":{"id":"` + integrationID + `"}}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/integrations/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/integrations/")
			if strings.Contains(id, "/") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			payload, ok := store[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"NotFound_Integration","message":"integration not found"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(payload))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/integrations/"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/integrations/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/integrations/")
			delete(store, id)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	ctx := context.Background()

	created, err := client.Integrations.Create(ctx, CreateIntegrationRequest{
		SourceID:             sourceID,
		DestinationID:        destID,
		ReplicationFrequency: "manual",
		DestinationSchema:    schema,
	})
	require.NoError(t, err)
	require.Equal(t, integrationID, created.ID)
	require.Equal(t, "postgres", created.Source.Type)

	var createReq CreateIntegrationRequest
	require.NoError(t, json.Unmarshal(createBody, &createReq))
	require.Equal(t, sourceID, createReq.SourceID)
	require.Equal(t, destID, createReq.DestinationID)
	require.Equal(t, schema, createReq.DestinationSchema)

	got, err := client.Integrations.Get(ctx, integrationID)
	require.NoError(t, err)
	require.Equal(t, schema, got.DestinationSchema)

	err = client.Integrations.Modify(ctx, integrationID, ModifyIntegrationRequest{Name: "renamed"})
	require.NoError(t, err)

	err = client.Integrations.Delete(ctx, integrationID)
	require.NoError(t, err)

	_, err = client.Integrations.Get(ctx, integrationID)
	require.ErrorIs(t, err, ErrIntegrationNotFound)
}

func TestIntegrationsClient_ModifyWithRetryRetriesIntegrationInCreation(t *testing.T) {
	t.Parallel()

	const integrationID = "507f1f77bcf86cd799439011"
	patchCalls := 0
	var mu sync.Mutex
	var patchBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/integrations/"+integrationID:
			patchCalls++
			if patchCalls == 1 {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write(
					[]byte(
						`{"code":"IntegrationInCreation","message":"Integration is in creation. Please try again later."}`,
					),
				)
				return
			}

			patchBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	client.pollInterval = time.Millisecond
	client.integrationOperationTimeout = time.Second
	ctx := context.Background()
	err := client.Integrations.ModifyWithRetry(ctx, integrationID, ModifyIntegrationRequest{
		ReplicationFrequency: "60",
	})
	require.NoError(t, err)
	require.Equal(t, 2, patchCalls)

	var patchReq ModifyIntegrationRequest
	require.NoError(t, json.Unmarshal(patchBody, &patchReq))
	require.Equal(t, "60", patchReq.ReplicationFrequency)
}

func TestIntegrationsClient_ModifyWithRetryTimesOutWhenIntegrationStaysInCreation(t *testing.T) {
	t.Parallel()

	const integrationID = "507f1f77bcf86cd799439011"
	patchCalls := 0
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.Method == http.MethodPatch && r.URL.Path == "/v1/integrations/"+integrationID {
			patchCalls++
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write(
				[]byte(
					`{"code":"IntegrationInCreation","message":"Integration is in creation. Please try again later."}`,
				),
			)
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	client.pollInterval = time.Millisecond
	client.integrationOperationTimeout = 100 * time.Millisecond
	err := client.Integrations.ModifyWithRetry(context.Background(), integrationID, ModifyIntegrationRequest{
		ReplicationFrequency: "60",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrIntegrationInCreation)
	require.Greater(t, patchCalls, 1)
}

func TestIntegrationsClient_ModifyWithRetryRetriesTimedOutPatch(t *testing.T) {
	t.Parallel()

	const integrationID = "507f1f77bcf86cd799439011"

	attempts := 0
	client := newTestClient("https://api.example.test/v1")
	client.pollInterval = time.Millisecond
	client.integrationOperationTimeout = 5 * time.Second
	client.HTTPClient = &http.Client{
		Transport: funcRoundTripper(func(r *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodPatch, r.Method)

			attempts++
			if attempts <= requestRetryMaxAttempts {
				return nil, context.DeadlineExceeded
			}

			body, _ := io.ReadAll(r.Body)
			var req ModifyIntegrationRequest
			require.NoError(t, json.Unmarshal(body, &req))
			require.Equal(t, "60", req.ReplicationFrequency)

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	err := client.Integrations.ModifyWithRetry(context.Background(), integrationID, ModifyIntegrationRequest{
		ReplicationFrequency: "60",
	})
	require.NoError(t, err)
	require.Equal(t, requestRetryMaxAttempts+1, attempts)
}

type funcRoundTripper func(*http.Request) (*http.Response, error)

func (f funcRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestIntegrationsClient_DeleteNotFoundIsIdempotent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NotFound_Integration","message":"integration not found"}`))
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	err := client.Integrations.Delete(context.Background(), "missing")
	require.ErrorIs(t, err, ErrIntegrationNotFound)
}

func TestIntegrationsClient_DeleteTimeoutWhenAlreadyDeleted(t *testing.T) {
	t.Parallel()

	const integrationID = "507f1f77bcf86cd799439011"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/integrations/"+integrationID {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NotFound_Integration","message":"integration not found"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	client.HTTPClient.Transport = funcRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodDelete {
			return nil, context.DeadlineExceeded
		}
		return http.DefaultTransport.RoundTrip(req)
	})

	err := client.Integrations.Delete(context.Background(), integrationID)
	require.NoError(t, err)
}

func TestIntegrationsClient_GetMissingID(t *testing.T) {
	t.Parallel()

	const integrationID = "507f1f77bcf86cd799439011"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/integrations/"+integrationID {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"success","data":{"id":"","name":"Postgres → Snowflake"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	_, err := client.Integrations.Get(context.Background(), integrationID)
	require.EqualError(t, err, "API response missing integration id")
}

func TestIntegrationsClient_GetNotFound(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NotFound_Integration","message":"integration not found"}`))
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	_, err := client.Integrations.Get(context.Background(), "missing")
	require.ErrorIs(t, err, ErrIntegrationNotFound)
}

func TestIntegrationsClient_CreateFollowsUpWithGet(t *testing.T) {
	t.Parallel()

	const integrationID = testIntegrationID
	getCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/integrations":
			_, _ = w.Write([]byte(`{"code":"success","data":{"id":"` + integrationID + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/integrations/"+integrationID:
			getCalls++
			_, _ = w.Write([]byte(testIntegrationJSON()))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	integration, err := client.Integrations.Create(context.Background(), CreateIntegrationRequest{
		SourceID:             "source-1",
		DestinationID:        "dest-1",
		ReplicationFrequency: "manual",
		DestinationSchema:    "raw",
	})
	require.NoError(t, err)
	require.Equal(t, 1, getCalls)
	require.Equal(t, "raw", integration.DestinationSchema)
}

func TestDecodeV1IntegrationWithScheduleFields(t *testing.T) {
	body := `{"code":"success","data":{"id":"507f1f77bcf86cd799439011","name":"Postgres → Snowflake","paused":false,"source":{"id":"aaa","name":"pg","type":"postgres"},"destination":{"id":"bbb","name":"sf","type":"snowflake"},"replicationFrequency":"60","destinationSchema":"raw"}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	integration, err := decodeV1DataResponse[Integration](resp, ErrIntegrationNotFound)
	require.NoError(t, err)
	require.Equal(t, "507f1f77bcf86cd799439011", integration.ID)
	require.Equal(t, "postgres", integration.Source.Type)
	require.Equal(t, "60", integration.ReplicationFrequency)
	require.Equal(t, "raw", integration.DestinationSchema)
}

func TestDecodeV1DataResponse_NullDataReturnsError(t *testing.T) {
	body := `{"code":"success","data":null}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	_, err := decodeV1DataResponse[createIntegrationResponse](resp, ErrIntegrationNotFound)
	require.Error(t, err)
	require.EqualError(t, err, "API response missing data")
}

func TestDecodeV1DataResponse_NullDataAllowedForMap(t *testing.T) {
	body := `{"code":"success","data":null}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	data, err := decodeV1DataResponse[map[string]any](resp, ErrIntegrationNotFound)
	require.NoError(t, err)
	require.Nil(t, data)
}

func TestIntegrationsClient_GetUpdateSchemaConfig(t *testing.T) {
	t.Parallel()

	const (
		integrationID = testIntegrationID
		sourceID      = "source-1"
		destID        = "dest-1"
		schema        = "raw"
	)
	schemaPayload := `{"schemas":{"public":{"tables":{"users":{"syncMode":"Incremental"}}}}}`
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/integrations/"+integrationID:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(testIntegrationJSON()))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/integrations/"+integrationID+"/schemas":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"success","data":` + schemaPayload + `}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/integrations/"+integrationID+"/schemas":
			body, _ := io.ReadAll(r.Body)
			schemaPayload = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"success","data":` + schemaPayload + `}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	ctx := context.Background()

	got, err := client.Integrations.GetSchemaConfig(ctx, integrationID)
	require.NoError(t, err)
	require.JSONEq(t, schemaPayload, got)

	patchBody := json.RawMessage(`{"schemas":{"public":{"tables":{"users":{"syncMode":"Full Refresh"}}}}}`)
	updated, err := client.Integrations.UpdateSchemaConfig(ctx, integrationID, patchBody)
	require.NoError(t, err)
	require.JSONEq(t, `{"schemas":{"public":{"tables":{"users":{"syncMode":"Full Refresh"}}}}}`, updated)

	got, err = client.Integrations.GetSchemaConfig(ctx, integrationID)
	require.NoError(t, err)
	require.JSONEq(t, `{"schemas":{"public":{"tables":{"users":{"syncMode":"Full Refresh"}}}}}`, got)
}

func TestIntegrationsClient_GetSchemaConfigNotFound(t *testing.T) {
	t.Parallel()

	const integrationID = "507f1f77bcf86cd799439011"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/integrations/"+integrationID+"/schemas" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NotFound_Integration","message":"integration not found"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := newTestClient(server.URL + "/v1")
	_, err := client.Integrations.GetSchemaConfig(context.Background(), integrationID)
	require.ErrorIs(t, err, ErrIntegrationNotFound)
}
