package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func TestAccIntegrationSchema_initialSelectionByPolicy(t *testing.T) {
	for _, policy := range []string{"enableAll", "enableColumnChanges", "ignoreAll", "enableNamespaceChanges", "pauseConnection", ""} {
		t.Run(policy, func(t *testing.T) {
			server := testAccStartSchemaCatalogServer(t)
			server.policy = policy
			config := testAccIntegrationSchemaConfig(server.URL, testAccAPIToken(t), "enabled = true")
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: config,
					Check: func(state *terraform.State) error {
						server.mu.Lock()
						defer server.mu.Unlock()
						restrict := policy == "enableColumnChanges" || policy == "ignoreAll"
						if got := server.tableLocked("public", "audit_log")["enabled"]; got != !restrict {
							return fmt.Errorf("unlisted audit_log.enabled = %v for policy %s", got, policy)
						}
						users := server.tableLocked("public", "users")
						columns, _ := users["columns"].(map[string]any)
						email, _ := columns["email"].(map[string]any)
						id, _ := columns["id"].(map[string]any)
						if users["enabled"] != true || id["enabled"] != true ||
							email["enabled"] != (policy != "ignoreAll") {
							return fmt.Errorf("incorrect selected table/column defaults for %s: %v", policy, users)
						}
						readBeforePatch := false
						for _, request := range server.requests {
							if request == "PATCH /v1/integrations/integration-1/schemas" {
								break
							}
							if request == "GET /v1/integrations/integration-1/schemas" {
								readBeforePatch = true
							}
						}
						if readBeforePatch != restrict {
							return fmt.Errorf("unexpected discovery/PATCH order: %v", server.requests)
						}
						// Generated disables must not leak into the required config or later create
						// perpetual Terraform diffs. The harness also checks for an empty next plan.
						stored := state.RootModule().Resources["matia_integration_schema.test"].Primary.Attributes["config"]
						if strings.Contains(stored, "audit_log") || strings.Contains(stored, "email") {
							return fmt.Errorf("automatic defaults leaked into config: %s", stored)
						}
						if got := server.PatchCount.Load(); got != 1 {
							return fmt.Errorf("wanted one initial PATCH, got %d", got)
						}
						return nil
					},
				}},
			})
		})
	}
}

func TestAccIntegrationSchema_initialSelectionDoesNotRepeatOnUpdate(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	apiToken := testAccAPIToken(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccIntegrationSchemaConfig(server.URL, apiToken, "enabled = true"),
				Check: func(*terraform.State) error {
					if got := server.LastPatchedTableField("public", "audit_log", "enabled"); got != false {
						return fmt.Errorf("initial PATCH did not disable audit_log: %v", got)
					}
					return nil
				},
			},
			{
				PreConfig: func() {
					server.SetTableField("public", "audit_log", "enabled", true)
				},
				Config: testAccIntegrationSchemaConfig(
					server.URL,
					apiToken,
					"enabled = true\nsyncMode = \"full_refresh\"",
				),
				Check: func(*terraform.State) error {
					if got := server.LastPatchedTableField("public", "audit_log", "enabled"); got != nil {
						return fmt.Errorf("update repeated initial selection: %v", got)
					}
					if got := server.TableField("public", "audit_log", "enabled"); got != true {
						return fmt.Errorf("update disabled unowned table: %v", got)
					}
					return nil
				},
			},
		},
	})
}

func TestIntegrationSchema_InitialSelectionColumnBlocking(t *testing.T) {
	for _, ignoreDisables := range []bool{false, true} {
		t.Run(strconv.FormatBool(ignoreDisables), func(t *testing.T) {
			server := testAccStartSchemaCatalogServer(t)
			server.policy = "ignoreAll"
			server.ignoreColumnDisables = ignoreDisables
			r := &integrationSchemaResource{client: client.NewMatiaClient(server.URL, "test-token")}
			plan := integrationSchemaModel{
				IntegrationID: types.StringValue(testAccIntegrationSchemaID),
				Config: jsontypes.NewNormalizedValue(
					`{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`,
				),
			}
			state, diags := r.applyInitialSchema(t.Context(), plan)
			require.False(t, diags.HasError(), "%v", diags)
			require.Equal(t, ignoreDisables, diags.WarningsCount() > 0)
			require.Equal(t, plan.Config, state.Config)
			if ignoreDisables {
				require.Contains(t, diags[0].Detail(), `"public"."users"."email"`)
			}
		})
	}
}

func TestIntegrationSchema_InitialSelectionEmptyConfig(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	r := &integrationSchemaResource{client: client.NewMatiaClient(server.URL, "test-token")}
	plan := integrationSchemaModel{
		IntegrationID: types.StringValue(testAccIntegrationSchemaID),
		Config:        jsontypes.NewNormalizedValue(`{"schemas":{}}`),
	}
	_, diags := r.applyInitialSchema(t.Context(), plan)
	require.False(t, diags.HasError(), "%v", diags)
	require.Equal(t, false, server.TableField("public", "users", "enabled"))
	require.Equal(t, false, server.TableField("public", "audit_log", "enabled"))
}

func TestIntegrationSchema_InitialSelectionNoPatchOnDiscoveryFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"empty catalog", http.StatusOK, `{"code":"success","data":{"schemas":{}}}`},
		{"malformed catalog", http.StatusOK, `{"code":"success","data":null}`},
		{"catalog error", http.StatusForbidden, `{"message":"forbidden"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var patches atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPatch {
					patches.Add(1)
					_, _ = w.Write([]byte(`{"data":{}}`))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/schemas") {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				_, _ = w.Write([]byte(testAccIntegrationJSON(testAccIntegrationSchemaID, "source", "dest", "raw")))
			}))
			t.Cleanup(server.Close)
			r := &integrationSchemaResource{client: client.NewMatiaClient(server.URL+"/v1", "test-token")}
			plan := integrationSchemaModel{
				IntegrationID: types.StringValue(testAccIntegrationSchemaID),
				Config:        jsontypes.NewNormalizedValue(`{"schemas":{}}`),
			}
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			_, diags := r.applyInitialSchema(ctx, plan)
			require.True(t, diags.HasError())
			require.Zero(t, patches.Load())
		})
	}
}

func TestIntegrationSchema_InitialSelectionPatchContainsOnlySettableFields(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	server.policy = "ignoreAll"
	server.mu.Lock()
	columns, _ := server.tableLocked("public", "users")["columns"].(map[string]any)
	email, _ := columns["email"].(map[string]any)
	email["hashed"] = true
	server.mu.Unlock()
	r := &integrationSchemaResource{client: client.NewMatiaClient(server.URL, "test-token")}
	plan := integrationSchemaModel{
		IntegrationID: types.StringValue(testAccIntegrationSchemaID),
		Config:        jsontypes.NewNormalizedValue(`{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`),
	}
	_, diags := r.applyInitialSchema(t.Context(), plan)
	require.Empty(t, diags)
	server.mu.Lock()
	defer server.mu.Unlock()
	body, err := json.Marshal(server.lastPatch)
	require.NoError(t, err)
	require.JSONEq(t, `{"schemas":{"public":{"tables":{
      "users":{"enabled":true,"columns":{"email":{"enabled":false,"hashed":true}}},
      "audit_log":{"enabled":false,"columns":{"action":{"enabled":false,"hashed":false}}}
    }}}}`, string(body))
}
