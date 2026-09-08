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

const testMultiPurposeAssetJSON = `{"code":"success","data":{"id":"6aa0382d49359e5d73e424ba","name":"warehouse","type":"snowflake","connectionType":"multi_purpose","configuration":{"etl":{"additionalDatabases":["EXTRA_DB"],"additionalWarehouses":[],"defaultDatabase":"RAW","defaultWarehouse":"LOAD_WH"}}}}`

func TestAssetsClient_MultiPurposeCreateOmitsConnectionTypeAndSendsConfiguration(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var createBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/assets":
			createBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"success","data":{"id":"6aa0382d49359e5d73e424ba"}}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/assets/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(testMultiPurposeAssetJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := newTestClient(server.URL + "/v1")

	emptyWarehouses := []string{}
	created, err := c.Assets.Create(context.Background(), CreateAssetRequest{
		Name: "warehouse",
		Type: "snowflake",
		Connection: map[string]any{
			"account": "acct", "username": "shared", "warehouse": "LOAD_WH", "database": "RAW", "password": "pw",
		},
		ConnectionOverrides: map[string]any{
			"catalog": map[string]any{"account": "acct", "username": "obs", "warehouse": "OBS_WH", "password": "pw"},
		},
		Owners: []string{},
		Configuration: &AssetConfigurationRequest{Etl: &AssetEtlConfigurationRequest{
			AdditionalDatabases:  &[]string{"EXTRA_DB"},
			AdditionalWarehouses: &emptyWarehouses,
		}},
	})
	require.NoError(t, err)

	require.Equal(t, "multi_purpose", created.ConnectionType)
	require.NotNil(t, created.Configuration)
	require.NotNil(t, created.Configuration.Etl)
	require.Equal(t, "RAW", created.Configuration.Etl.DefaultDatabase)
	require.Equal(t, "LOAD_WH", created.Configuration.Etl.DefaultWarehouse)
	require.Equal(t, []string{"EXTRA_DB"}, created.Configuration.Etl.AdditionalDatabases)
	require.Empty(t, created.Configuration.Etl.AdditionalWarehouses)

	mu.Lock()
	defer mu.Unlock()
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(createBody, &raw))
	_, hasConnectionType := raw["connectionType"]
	require.False(t, hasConnectionType, "connectionType must be omitted so the API defaults Snowflake to multi_purpose")
	require.JSONEq(
		t,
		`{"catalog":{"account":"acct","username":"obs","warehouse":"OBS_WH","password":"pw"}}`,
		string(raw["connectionOverrides"]),
	)
	require.JSONEq(
		t,
		`{"etl":{"additionalDatabases":["EXTRA_DB"],"additionalWarehouses":[]}}`,
		string(raw["configuration"]),
	)
}

func TestAssetsClient_UpdateSendsEmptyListsAndOmitsUntouchedFields(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var patchBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/assets/") {
			patchBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := newTestClient(server.URL + "/v1")
	noOwners := []string{}
	err := c.Assets.Update(context.Background(), "6aa0382d49359e5d73e424ba", UpdateAssetRequest{
		Owners: &noOwners,
		Configuration: &AssetConfigurationRequest{Etl: &AssetEtlConfigurationRequest{
			AdditionalDatabases: &[]string{},
		}},
	})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.JSONEq(
		t,
		`{"owners":[],"configuration":{"etl":{"additionalDatabases":[]}}}`,
		string(patchBody),
	)
}
