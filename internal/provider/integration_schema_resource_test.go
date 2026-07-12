package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/stretchr/testify/require"
)

const testAccIntegrationSchemaID = "integration-1"

func testAccStartIntegrationSchemaServer(t *testing.T) string {
	t.Helper()

	integrationStore := map[string]string{
		testAccIntegrationSchemaID: testAccIntegrationJSON(testAccIntegrationSchemaID, "source-1", "dest-1", "raw"),
	}
	schemaStore := map[string]string{
		testAccIntegrationSchemaID: `{"schemas":{}}`,
	}
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/integrations/"+testAccIntegrationSchemaID:
			payload, ok := integrationStore[testAccIntegrationSchemaID]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"NotFound_Integration","message":"integration not found"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(payload))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/integrations/"+testAccIntegrationSchemaID:
			delete(integrationStore, testAccIntegrationSchemaID)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/integrations/"+testAccIntegrationSchemaID+"/schemas":
			payload, ok := schemaStore[testAccIntegrationSchemaID]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"NotFound_Integration","message":"integration not found"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"code":"success","data":%s}`, payload)
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/integrations/"+testAccIntegrationSchemaID+"/schemas":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			enrichSchemaCatalogLikeAPI(payload)
			encoded, err := json.Marshal(payload)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			schemaStore[testAccIntegrationSchemaID] = string(encoded)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"code":"success","data":%s}`, string(encoded))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

// enrichSchemaCatalogLikeAPI mimics the real API: it adds server-side fields the user never sent
// (enabled, nameInDestination) plus an undeclared table, none of which may leak into state.
func enrichSchemaCatalogLikeAPI(payload map[string]any) {
	schemas, schemasOK := payload["schemas"].(map[string]any)
	if !schemasOK {
		return
	}
	for _, sv := range schemas {
		sm, smOK := sv.(map[string]any)
		if !smOK {
			continue
		}
		tables, tablesOK := sm["tables"].(map[string]any)
		if !tablesOK {
			continue
		}
		for name, tv := range tables {
			if tm, tmOK := tv.(map[string]any); tmOK {
				tm["enabled"] = true
				tm["nameInDestination"] = name
			}
		}
		tables["audit_log"] = map[string]any{
			"enabled":           true,
			"syncMode":          "FULL_REFRESH",
			"nameInDestination": "audit_log",
		}
	}
}

func TestAccIntegrationSchema_basic(t *testing.T) {
	apiURL := testAccStartIntegrationSchemaServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: testAccCheckIntegrationSurvivesDestroy(
			apiURL,
			apiToken,
			"matia_integration_schema.test",
		),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + fmt.Sprintf(`
resource "matia_integration_schema" "test" {
  integration_id = %q
  config         = jsonencode({
    schemas = {
      public = {
        tables = {
          users = {
            syncMode = "incremental"
          }
        }
      }
    }
  })
}
`, testAccIntegrationSchemaID),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(
						"matia_integration_schema.test",
						"integration_id",
						testAccIntegrationSchemaID,
					),
					resource.TestCheckResourceAttrSet("matia_integration_schema.test", "config"),
				),
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + fmt.Sprintf(`
resource "matia_integration_schema" "test" {
  integration_id = %q
  config         = jsonencode({
    schemas = {
      public = {
        tables = {
          users = {
            syncMode = "full_refresh"
          }
        }
      }
    }
  })
}
`, testAccIntegrationSchemaID),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(
						"matia_integration_schema.test",
						"integration_id",
						testAccIntegrationSchemaID,
					),
					resource.TestCheckResourceAttr(
						"matia_integration_schema.test",
						"config",
						`{"schemas":{"public":{"tables":{"users":{"syncMode":"full_refresh"}}}}}`,
					),
				),
			},
		},
	})
}

func TestIntegrationSchemaToModel_ReflectsUserConfig(t *testing.T) {
	t.Parallel()

	userConfig := `{"schemas":{"public":{"tables":{"users":{"syncMode":"incremental"}}}}}`
	model := integrationSchemaToModel(integrationSchemaModel{
		IntegrationID: types.StringValue(testAccIntegrationSchemaID),
		Config:        types.StringValue(userConfig),
	})
	require.Equal(t, testAccIntegrationSchemaID, model.IntegrationID.ValueString())
	require.Equal(t, userConfig, model.Config.ValueString())
}
