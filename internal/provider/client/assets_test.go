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

	"github.com/stretchr/testify/require"
)

func testAssetJSON(id string) string {
	return `{"code":"success","data":{"id":"` + id + `","name":"tf-test","type":"postgres","description":"warehouse","isDraft":false,"authMethod":"direct","connectionType":"source","connection":{"hostname":"localhost"}}}`
}

func TestAssetsClient_CreateGetUpdateDelete(t *testing.T) {
	t.Parallel()

	const assetID = "507f1f77bcf86cd799439011"
	store := map[string]string{assetID: testAssetJSON(assetID)}
	var mu sync.Mutex
	var createBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/assets":
			createBody, _ = io.ReadAll(r.Body)

			store[assetID] = testAssetJSON(assetID)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"success","data":{"id":"` + assetID + `"}}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/assets/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/assets/")
			payload, ok := store[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"NotFound","message":"asset not found"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(payload))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/assets/"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/assets/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/assets/")
			delete(store, id)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	ctx := context.Background()

	created, err := client.Assets.Create(ctx, CreateAssetRequest{
		Name:           "tf-test",
		Type:           "postgres",
		ConnectionType: "source",
		Connection:     map[string]any{"hostname": "localhost"},
		Owners:         []string{},
	})
	require.NoError(t, err)
	require.Equal(t, assetID, created.AssetID())
	require.Equal(t, "tf-test", created.Name)

	var createReq CreateAssetRequest
	require.NoError(t, json.Unmarshal(createBody, &createReq))
	require.Equal(t, "source", createReq.ConnectionType)

	got, err := client.Assets.Get(ctx, assetID)
	require.NoError(t, err)
	require.Equal(t, "postgres", got.Type)

	err = client.Assets.Update(ctx, assetID, UpdateAssetRequest{Name: "renamed"})
	require.NoError(t, err)

	err = client.Assets.Delete(ctx, assetID)
	require.NoError(t, err)

	_, err = client.Assets.Get(ctx, assetID)
	require.ErrorIs(t, err, ErrAssetNotFound)
}

func TestAssetsClient_GetNotFound(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NotFound","message":"asset not found"}`))
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	_, err := client.Assets.Get(context.Background(), "missing")
	require.ErrorIs(t, err, ErrAssetNotFound)
}

func TestAssetsClient_DeleteTimeoutWhenAlreadyDeleted(t *testing.T) {
	t.Parallel()

	const assetID = "507f1f77bcf86cd799439011"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/assets/"+assetID {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NotFound","message":"asset not found"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	client.HTTPClient.Transport = funcRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodDelete {
			return nil, context.DeadlineExceeded
		}
		return http.DefaultTransport.RoundTrip(req)
	})

	err := client.Assets.Delete(context.Background(), assetID)
	require.NoError(t, err)
}

func TestAssetsClient_GetMissingID(t *testing.T) {
	t.Parallel()

	const assetID = "507f1f77bcf86cd799439011"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/assets/"+assetID {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"success","data":{"id":"","name":"tf-test","type":"postgres"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	_, err := client.Assets.Get(context.Background(), assetID)
	require.EqualError(
		t,
		err,
		`API response missing asset id: {"code":"success","data":{"id":"","name":"tf-test","type":"postgres"}}`,
	)
}

func TestAssetsClient_DeleteNotFoundIsIdempotent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NotFound","message":"asset not found"}`))
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	err := client.Assets.Delete(context.Background(), "missing")
	require.ErrorIs(t, err, ErrAssetNotFound)
}

func TestAssetsClient_CreateFollowsUpWithGet(t *testing.T) {
	t.Parallel()

	const assetID = "507f1f77bcf86cd799439011"
	getCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/assets":
			_, _ = w.Write([]byte(`{"code":"success","data":{"id":"` + assetID + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/assets/"+assetID:
			getCalls++
			_, _ = w.Write([]byte(testAssetJSON(assetID)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewMatiaClient(server.URL+"/v1", "key")
	asset, err := client.Assets.Create(context.Background(), CreateAssetRequest{
		Name:           "tf-test",
		Type:           "postgres",
		ConnectionType: "source",
		Connection:     map[string]any{"hostname": "localhost"},
		Owners:         []string{},
	})
	require.NoError(t, err)
	require.Equal(t, 1, getCalls)
	require.Equal(t, "warehouse", asset.Description)
}

func TestAssetsClient_DecodeInternalAPIFailure(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"ok":false,"error":"Invalid connection"}`)),
	}

	_, err := (&AssetsClient{}).decodeAssetResponse(resp)
	require.Error(t, err)
	require.EqualError(t, err, "API request failed: Invalid connection")
}
