package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/stretchr/testify/require"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func testAccAPIToken(t *testing.T) string {
	t.Helper()
	return "test-api-key"
}

func testAccAssetJSON(id, name, assetType, connectionType string) string {
	return fmt.Sprintf(
		`{"code":"success","data":{"id":%q,"name":%q,"type":%q,"description":"","isDraft":false,"authMethod":"direct","connectionType":%q,"connection":{"hostname":"localhost"}}}`,
		id,
		name,
		assetType,
		connectionType,
	)
}

func testAccApplyAssetPatch(payload string, req client.UpdateAssetRequest) string {
	var envelope struct {
		Code string `json:"code"`
		Data struct {
			ID             string         `json:"id"`
			Name           string         `json:"name"`
			Type           string         `json:"type"`
			Description    string         `json:"description"`
			IsDraft        bool           `json:"isDraft"`
			AuthMethod     string         `json:"authMethod"`
			ConnectionType string         `json:"connectionType"`
			Connection     map[string]any `json:"connection"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return payload
	}
	if req.Name != "" {
		envelope.Data.Name = req.Name
	}
	if req.Description != nil {
		envelope.Data.Description = *req.Description
	}
	if req.AuthMethod != "" {
		envelope.Data.AuthMethod = req.AuthMethod
	}
	if req.Connection != nil {
		if envelope.Data.Connection == nil {
			envelope.Data.Connection = map[string]any{}
		}
		maps.Copy(envelope.Data.Connection, req.Connection)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return payload
	}
	return string(encoded)
}

func testAccStartAssetsServer(t *testing.T) string {
	return testAccStartAssetsServerWithConnectionType(t, "")
}

// connectionType overrides what the API reports for every created asset; empty
// echoes the request, as the API does for non-Snowflake connectors.
func testAccStartAssetsServerWithConnectionType(t *testing.T, connectionType string) string {
	t.Helper()

	store := map[string]string{}
	var mu sync.Mutex
	nextID := 1

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/assets":
			body, _ := io.ReadAll(r.Body)
			var req client.CreateAssetRequest
			if err := json.Unmarshal(body, &req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			id := fmt.Sprintf("asset-%d", nextID)
			nextID++
			reported := req.ConnectionType
			if connectionType != "" {
				reported = connectionType
			}
			store[id] = testAccAssetAgentJSON(testAccAssetJSON(id, req.Name, req.Type, reported), req.Configuration)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"code":"success","data":{"id":%q}}`, id)
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
			id := strings.TrimPrefix(r.URL.Path, "/v1/assets/")
			payload, ok := store[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"NotFound","message":"asset not found"}`))
				return
			}
			body, _ := io.ReadAll(r.Body)
			var req client.UpdateAssetRequest
			if err := json.Unmarshal(body, &req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var previous map[string]any
			_ = json.Unmarshal([]byte(payload), &previous)
			updated := testAccApplyAssetPatch(payload, req)
			var next map[string]any
			_ = json.Unmarshal([]byte(updated), &next)
			if oldConfig, hasConfig := previous["data"].(map[string]any)["configuration"]; hasConfig {
				next["data"].(map[string]any)["configuration"] = oldConfig
			}
			if req.Configuration != nil {
				next["data"].(map[string]any)["configuration"] = req.Configuration
			}
			encoded, _ := json.Marshal(next)
			store[id] = string(encoded)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/assets/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/assets/")
			delete(store, id)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

func testAccProviderConfig(apiURL, apiToken string) string {
	return fmt.Sprintf(`
provider "matia" {
  api_token = %q
  api_url   = %q
}
`, apiToken, apiURL)
}

func TestAccSource_basic(t *testing.T) {
	apiURL := testAccStartAssetsServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckAPIResourceDestroyed(apiURL, apiToken, "/assets", "matia_source.test"),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_source" "test" {
  name = "example-postgres-source"
  type = "postgres"

  connection_config = jsonencode({
    hostname = "localhost"
    port     = "5432"
    database = "postgres"
    ssl      = false
  })

  connection_secrets = jsonencode({
    username = "postgres"
    password = "postgres"
  })
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("matia_source.test", "id"),
					resource.TestCheckResourceAttr("matia_source.test", "name", "example-postgres-source"),
					resource.TestCheckResourceAttr("matia_source.test", "type", "postgres"),
				),
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_source" "test" {
  name        = "renamed-postgres-source"
  type        = "postgres"
  description = "updated description"

  connection_config = jsonencode({
    hostname = "localhost"
    port     = "5432"
    database = "postgres"
    ssl      = false
  })

  connection_secrets = jsonencode({
    username = "postgres"
    password = "postgres"
  })
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_source.test", "name", "renamed-postgres-source"),
					resource.TestCheckResourceAttr("matia_source.test", "description", "updated description"),
				),
			},
		},
	})
}

func TestAccDestination_basic(t *testing.T) {
	apiURL := testAccStartAssetsServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: testAccCheckAPIResourceDestroyed(
			apiURL,
			apiToken,
			"/assets",
			"matia_destination.test",
		),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_destination" "test" {
  name = "example-snowflake-destination"
  type = "snowflake"

  connection_config = jsonencode({
    account   = "xy12345"
    database  = "STANDARD_DATABASE"
    warehouse = "STANDARD_WAREHOUSE"
    role      = "STANDARD_ROLE"
    username  = "snowflake_user"
  })

  connection_secrets = jsonencode({
    password = "snowflake_password"
  })
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("matia_destination.test", "id"),
					resource.TestCheckResourceAttr("matia_destination.test", "name", "example-snowflake-destination"),
					resource.TestCheckResourceAttr("matia_destination.test", "type", "snowflake"),
				),
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_destination" "test" {
  name        = "renamed-snowflake-destination"
  type        = "snowflake"
  description = "updated destination"

  connection_config = jsonencode({
    account   = "xy12345"
    database  = "STANDARD_DATABASE"
    warehouse = "STANDARD_WAREHOUSE"
    role      = "STANDARD_ROLE"
    username  = "snowflake_user"
  })

  connection_secrets = jsonencode({
    password = "snowflake_password"
  })
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_destination.test", "name", "renamed-snowflake-destination"),
					resource.TestCheckResourceAttr("matia_destination.test", "description", "updated destination"),
				),
			},
		},
	})
}

func TestAssetToModel_UsesTemplateFields(t *testing.T) {
	t.Parallel()

	model, diags := assetToModel(
		&client.Asset{
			ID:             "asset-1",
			Name:           "api-name",
			Type:           "postgres",
			Description:    "from-api",
			AuthMethod:     "direct",
			Connection:     map[string]any{"hostname": "localhost"},
			ConnectionType: "source",
		},
		assetModel{
			Name:             types.StringValue("planned-name"),
			Type:             types.StringValue("postgres"),
			Description:      types.StringValue("planned-description"),
			ConnectionConfig: types.StringValue(`{"hostname":"localhost"}`),
		},
	)
	require.False(t, diags.HasError())
	require.Equal(t, "planned-name", model.Name.ValueString())
	require.Equal(t, "planned-description", model.Description.ValueString())
}

// A backend with multipurpose support creates every Snowflake asset as
// multi_purpose, and a flat credentials PATCH on such an asset is silently
// dropped, so the legacy resource refuses it.
func TestAccDestination_multiPurposeCredentialsChangeIsRefused(t *testing.T) {
	apiURL := testAccStartAssetsServerWithConnectionType(t, "multi_purpose")
	apiToken := testAccAPIToken(t)

	config := func(name, password string) string {
		return testAccProviderConfig(apiURL, apiToken) + fmt.Sprintf(`
resource "matia_destination" "test" {
  name = %q
  type = "snowflake"

  connection_config = jsonencode({
    account   = "xy12345"
    database  = "STANDARD_DATABASE"
    warehouse = "STANDARD_WAREHOUSE"
    username  = "snowflake_user"
  })

  connection_secrets = jsonencode({
    password = %q
  })
}
`, name, password)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("legacy-snowflake", "old-secret"),
			},
			{
				// Metadata still updates; only the credentials are frozen.
				Config: config("legacy-snowflake-renamed", "old-secret"),
				Check:  resource.TestCheckResourceAttr("matia_destination.test", "name", "legacy-snowflake-renamed"),
			},
			{
				Config:      config("legacy-snowflake-renamed", "rotated"),
				ExpectError: regexp.MustCompile(`Credentials of a multipurpose asset cannot be changed here`),
			},
		},
	})
}

// Agent requests use a nested tri-state value: omitted, assigned, or explicit null.
func testAccAssetAgentJSON(payload string, config *client.AssetConfigurationRequest) string {
	var envelope map[string]any
	_ = json.Unmarshal([]byte(payload), &envelope)
	data := envelope["data"].(map[string]any)
	if config != nil {
		data["configuration"] = config
	}
	result, _ := json.Marshal(envelope)
	return string(result)
}

func TestAccSource_agentLifecycle(t *testing.T) {
	apiURL := testAccStartAssetsServer(t)
	config := func(agent string) string {
		assignment := ""
		if agent != "" {
			assignment = fmt.Sprintf("agent_id = %q", agent)
		}
		return testAccProviderConfig(apiURL, testAccAPIToken(t)) + fmt.Sprintf(`
resource "matia_source" "test" {
  name = "source"
  type = "postgres"
  connection_config = jsonencode({hostname = "localhost"})
  %s
}
`, assignment)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("agent-a"),
				Check:  resource.TestCheckResourceAttr("matia_source.test", "agent_id", "agent-a"),
			},
			{
				ResourceName:            "matia_source.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"connection_config", "connection_secrets"},
			},
			{
				Config: config("agent-b"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_source.test", "agent_id", "agent-b"),
					resource.TestCheckResourceAttr("matia_source.test", "id", "asset-1"),
				),
			},
			{
				Config: config(""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckNoResourceAttr("matia_source.test", "agent_id"),
					resource.TestCheckResourceAttr("matia_source.test", "id", "asset-1"),
				),
			},
		},
	})
}
