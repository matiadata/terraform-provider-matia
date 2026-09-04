package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"
)

const testAccIntegrationSchemaID = "integration-1"

// testAccSchemaCatalogServer serves the schemas endpoints in the shape the real API uses: GET
// returns the full enriched catalog (every table, its columns and primary keys, syncMode in the
// upper-case form GET emits), and PATCH applies the sparse request onto it and returns the same
// catalog.
type testAccSchemaCatalogServer struct {
	URL        string
	PatchCount *atomic.Int64

	mu        sync.Mutex
	catalog   map[string]any
	lastPatch map[string]any
	// coerceSyncMode makes PATCH accept the request but keep the stored syncMode, the way the real
	// API treats a mode the source does not support: 200, with the old value silently retained.
	coerceSyncMode bool
}

func testAccCatalogTable(name string, enabled bool, syncMode string, columns ...string) map[string]any {
	cols := map[string]any{}
	for i, column := range columns {
		cols[column] = map[string]any{
			"nameInDestination": column,
			"enabled":           true,
			"hashed":            false,
			"isPrimaryKey":      i == 0,
		}
	}
	return map[string]any{
		"nameInDestination":    name,
		"enabled":              enabled,
		"enabledPatchSettings": map[string]any{"allowed": true},
		"syncMode":             syncMode,
		"columns":              cols,
	}
}

func testAccStartSchemaCatalogServer(t *testing.T) *testAccSchemaCatalogServer {
	t.Helper()

	s := &testAccSchemaCatalogServer{
		PatchCount: &atomic.Int64{},
		catalog: map[string]any{
			"schemas": map[string]any{
				"public": map[string]any{
					"nameInDestination": "public",
					"tables": map[string]any{
						"users":     testAccCatalogTable("users", true, "CHANGE_STREAM", "id", "email"),
						"audit_log": testAccCatalogTable("audit_log", true, "FULL_REFRESH", "id", "action"),
					},
				},
			},
		},
	}

	integrationJSON := testAccIntegrationJSON(testAccIntegrationSchemaID, "source-1", "dest-1", "raw")
	integrationDeleted := false
	schemasPath := "/v1/integrations/" + testAccIntegrationSchemaID + "/schemas"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/integrations/"+testAccIntegrationSchemaID:
			if integrationDeleted {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"NotFound_Integration","message":"integration not found"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(integrationJSON))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/integrations/"+testAccIntegrationSchemaID:
			integrationDeleted = true
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == schemasPath:
			s.writeCatalogLocked(w)
		case r.Method == http.MethodPatch && r.URL.Path == schemasPath:
			s.PatchCount.Add(1)
			body, _ := io.ReadAll(r.Body)
			var request map[string]any
			if err := json.Unmarshal(body, &request); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.lastPatch = request
			s.applyLocked(request)
			s.writeCatalogLocked(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	s.URL = server.URL + "/v1"
	return s
}

func (s *testAccSchemaCatalogServer) writeCatalogLocked(w http.ResponseWriter) {
	encoded, err := json.Marshal(s.catalog)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"code":"success","data":%s}`, encoded)
}

// applyLocked mirrors the backend: it writes the requested fields onto the stored catalog and
// stores syncMode in GET's upper-case form, so a value written by PATCH reads back transformed.
func (s *testAccSchemaCatalogServer) applyLocked(request map[string]any) {
	requestSchemas, ok := request["schemas"].(map[string]any)
	if !ok {
		return
	}
	catalogSchemas, _ := s.catalog["schemas"].(map[string]any)

	for schemaName, schemaVal := range requestSchemas {
		requestSchema, schemaOK := schemaVal.(map[string]any)
		catalogSchema, catalogOK := catalogSchemas[schemaName].(map[string]any)
		if !schemaOK || !catalogOK {
			continue
		}
		requestTables, tablesOK := requestSchema["tables"].(map[string]any)
		catalogTables, catalogTablesOK := catalogSchema["tables"].(map[string]any)
		if !tablesOK || !catalogTablesOK {
			continue
		}

		for tableName, tableVal := range requestTables {
			requestTable, tableOK := tableVal.(map[string]any)
			catalogTable, catalogTableOK := catalogTables[tableName].(map[string]any)
			if !tableOK || !catalogTableOK {
				continue
			}
			s.applyTableLocked(requestTable, catalogTable)
		}
	}
}

func (s *testAccSchemaCatalogServer) applyTableLocked(requestTable, catalogTable map[string]any) {
	if enabled, ok := requestTable["enabled"].(bool); ok {
		catalogTable["enabled"] = enabled
	}
	if syncMode, ok := requestTable["syncMode"].(string); ok && syncMode != "" && !s.coerceSyncMode {
		catalogTable["syncMode"] = catalogGETForm(resolveSyncModeForAPI(syncMode))
	}
	if cursorField, ok := requestTable["cursorField"].(string); ok && cursorField != "" {
		catalogTable["cursorField"] = cursorField
	}

	requestColumns, ok := requestTable["columns"].(map[string]any)
	catalogColumns, catalogOK := catalogTable["columns"].(map[string]any)
	if !ok || !catalogOK {
		return
	}
	for columnName, columnVal := range requestColumns {
		requestColumn, columnOK := columnVal.(map[string]any)
		catalogColumn, catalogColumnOK := catalogColumns[columnName].(map[string]any)
		if !columnOK || !catalogColumnOK {
			continue
		}
		for _, field := range []string{"enabled", "hashed"} {
			if value, fieldOK := requestColumn[field].(bool); fieldOK {
				catalogColumn[field] = value
			}
		}
	}
}

// SetTableField changes a value behind Terraform's back, standing in for an edit made in the
// Matia UI between runs.
func (s *testAccSchemaCatalogServer) SetTableField(schemaName, tableName, field string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tableLocked(schemaName, tableName)[field] = value
}

func (s *testAccSchemaCatalogServer) TableField(schemaName, tableName, field string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tableLocked(schemaName, tableName)[field]
}

func (s *testAccSchemaCatalogServer) CoerceSyncMode() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coerceSyncMode = true
}

// LastPatchedTableField returns a field exactly as the provider sent it, before the server-side
// syncMode transform. Asserting on this rather than on stored state keeps the check independent
// of the resolution the fake shares with production code.
func (s *testAccSchemaCatalogServer) LastPatchedTableField(schemaName, tableName, field string) any {
	s.mu.Lock()
	defer s.mu.Unlock()

	schemas, _ := s.lastPatch["schemas"].(map[string]any)
	schema, _ := schemas[schemaName].(map[string]any)
	tables, _ := schema["tables"].(map[string]any)
	table, _ := tables[tableName].(map[string]any)
	return table[field]
}

func (s *testAccSchemaCatalogServer) tableLocked(schemaName, tableName string) map[string]any {
	schemas, _ := s.catalog["schemas"].(map[string]any)
	schema, _ := schemas[schemaName].(map[string]any)
	tables, _ := schema["tables"].(map[string]any)
	table, _ := tables[tableName].(map[string]any)
	return table
}

func testAccIntegrationSchemaConfig(apiURL, apiToken, tableBody string) string {
	return testAccIntegrationSchemaTablesConfig(apiURL, apiToken, fmt.Sprintf(`          users = {
%s
          }`, tableBody))
}

func testAccIntegrationSchemaTablesConfig(apiURL, apiToken, tables string) string {
	return testAccProviderConfig(apiURL, apiToken) + fmt.Sprintf(`
resource "matia_integration_schema" "test" {
  integration_id = %q
  config         = jsonencode({
    schemas = {
      public = {
        tables = {
%s
        }
      }
    }
  })
}
`, testAccIntegrationSchemaID, tables)
}

func TestAccIntegrationSchema_basic(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: testAccCheckIntegrationSurvivesDestroy(
			server.URL,
			apiToken,
			"matia_integration_schema.test",
		),
		Steps: []resource.TestStep{
			{
				Config: testAccIntegrationSchemaConfig(server.URL, apiToken, `            syncMode = "incremental"`),
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
				Config: testAccIntegrationSchemaConfig(server.URL, apiToken, `            syncMode = "full_refresh"`),
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

func TestAccIntegrationSchema_effectiveSchemaExposesFullCatalog(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccIntegrationSchemaConfig(server.URL, apiToken, `            syncMode = "incremental"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(
						"matia_integration_schema.test",
						"config",
						`{"schemas":{"public":{"tables":{"users":{"syncMode":"incremental"}}}}}`,
					),
					testAccCheckEffectiveSchemaExposesCatalog("matia_integration_schema.test"),
				),
			},
		},
	})
}

func testAccCheckEffectiveSchemaExposesCatalog(name string) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		raw := state.RootModule().Resources[name].Primary.Attributes["effective_schema"]

		var catalog map[string]any
		if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
			return fmt.Errorf("effective_schema is not valid JSON: %w", err)
		}

		schemas, ok := catalog["schemas"].(map[string]any)
		if !ok {
			return fmt.Errorf("effective_schema has no schemas object: %s", raw)
		}
		public, ok := schemas["public"].(map[string]any)
		if !ok {
			return fmt.Errorf("effective_schema has no public schema: %s", raw)
		}
		tables, ok := public["tables"].(map[string]any)
		if !ok {
			return fmt.Errorf("effective_schema has no tables: %s", raw)
		}
		if _, hasAuditLog := tables["audit_log"]; !hasAuditLog {
			return fmt.Errorf("effective_schema is missing the undeclared audit_log table: %s", raw)
		}

		users, ok := tables["users"].(map[string]any)
		if !ok {
			return fmt.Errorf("effective_schema is missing the users table: %s", raw)
		}
		columns, ok := users["columns"].(map[string]any)
		if !ok || len(columns) == 0 {
			return fmt.Errorf("effective_schema is missing users columns: %s", raw)
		}
		id, ok := columns["id"].(map[string]any)
		if !ok || id["isPrimaryKey"] != true {
			return fmt.Errorf("effective_schema lost the primary key flag: %s", raw)
		}
		return nil
	}
}

func TestAccIntegrationSchema_serverDriftIsDetectedAndRepaired(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	apiToken := testAccAPIToken(t)
	config := testAccIntegrationSchemaConfig(
		server.URL,
		apiToken,
		"            enabled  = true\n            syncMode = \"cdc\"",
	)

	var patchesAfterCreate int64
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(*terraform.State) error {
					patchesAfterCreate = server.PatchCount.Load()
					return nil
				},
			},
			{
				// Someone flips the table in the Matia UI: Terraform must notice on refresh and
				// PATCH it back, instead of reporting "No changes" forever.
				PreConfig: func() {
					server.SetTableField("public", "users", "enabled", false)
					server.SetTableField("public", "users", "syncMode", "FULL_REFRESH")
				},
				Config: config,
				Check: func(*terraform.State) error {
					if extra := server.PatchCount.Load() - patchesAfterCreate; extra != 1 {
						return fmt.Errorf("expected exactly one repair PATCH, got %d", extra)
					}
					if got := server.TableField("public", "users", "enabled"); got != true {
						return fmt.Errorf("enabled was not repaired, server still has %v", got)
					}
					if got := server.TableField("public", "users", "syncMode"); got != "CHANGE_STREAM" {
						return fmt.Errorf("syncMode was not repaired, server still has %v", got)
					}
					// The alias must be resolved before it leaves the provider: the real PATCH
					// validates against ESyncMode and rejects "cdc".
					if got := server.LastPatchedTableField("public", "users", "syncMode"); got != "Change Stream" {
						return fmt.Errorf("PATCH body carried %v, want the canonical API value", got)
					}
					return nil
				},
			},
		},
	})
}

func TestAccIntegrationSchema_driftOnUndeclaredTableIsIgnored(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	apiToken := testAccAPIToken(t)
	config := testAccIntegrationSchemaConfig(server.URL, apiToken, `            syncMode = "cdc"`)

	var patchesAfterCreate int64
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(*terraform.State) error {
					patchesAfterCreate = server.PatchCount.Load()
					return nil
				},
			},
			{
				// audit_log is discovered, not declared: changing it must not produce a diff, or
				// every discovered table in the source would fight Terraform.
				PreConfig: func() {
					server.SetTableField("public", "audit_log", "enabled", false)
				},
				Config: config,
				Check: func(*terraform.State) error {
					if extra := server.PatchCount.Load() - patchesAfterCreate; extra != 0 {
						return fmt.Errorf("expected no PATCH for an undeclared table, got %d", extra)
					}
					return nil
				},
			},
		},
	})
}

func TestAccIntegrationSchema_removingDeclaredTableDisablesIt(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	apiToken := testAccAPIToken(t)

	declared := func(tables string) string {
		return testAccIntegrationSchemaTablesConfig(server.URL, apiToken, tables)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: declared(`          users     = { enabled = true }
          audit_log = { enabled = true }`),
			},
			{
				// audit_log was declared on the previous apply, so Terraform owns it: dropping the
				// line must disable it rather than silently leave it syncing.
				Config: declared(`          users = { enabled = true }`),
				Check: func(*terraform.State) error {
					if sent := server.LastPatchedTableField("public", "audit_log", "enabled"); sent != false {
						return fmt.Errorf("PATCH body audit_log.enabled = %v, want false", sent)
					}
					if stored := server.TableField("public", "audit_log", "enabled"); stored != false {
						return fmt.Errorf("stored audit_log.enabled = %v, want false", stored)
					}
					return nil
				},
			},
		},
	})
}

// Ownership is per field: a table declared without `enabled` was never turned on by Terraform, so
// removing it must leave it running rather than stop a sync Terraform never started.
func TestAccIntegrationSchema_removingTableWhoseEnabledWasNeverDeclaredLeavesItEnabled(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	apiToken := testAccAPIToken(t)

	declared := func(tables string) string {
		return testAccIntegrationSchemaTablesConfig(server.URL, apiToken, tables)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: declared(`          users     = { enabled = true }
          audit_log = { syncMode = "full_refresh" }`),
			},
			{
				Config: declared(`          users = { enabled = true }`),
				Check: func(*terraform.State) error {
					if stored := server.TableField("public", "audit_log", "enabled"); stored != true {
						return fmt.Errorf("stored audit_log.enabled = %v, want true (never declared enabled)", stored)
					}
					return nil
				},
			},
		},
	})
}

func TestAccIntegrationSchema_equivalentSyncModeIsNoOp(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	apiToken := testAccAPIToken(t)

	// Switching a table between two equivalent syncMode aliases (change_stream <-> cdc, both the
	// API's "Change Stream") is a semantic no-op: it must apply cleanly without a PATCH and without
	// tripping "provider produced inconsistent result after apply".
	var patchesAfterCreate int64
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccIntegrationSchemaConfig(server.URL, apiToken, `            syncMode = "change_stream"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(
						"matia_integration_schema.test", "config",
						`{"schemas":{"public":{"tables":{"users":{"syncMode":"change_stream"}}}}}`,
					),
					func(*terraform.State) error {
						patchesAfterCreate = server.PatchCount.Load()
						return nil
					},
				),
			},
			{
				Config: testAccIntegrationSchemaConfig(server.URL, apiToken, `            syncMode = "cdc"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(
						"matia_integration_schema.test", "config",
						`{"schemas":{"public":{"tables":{"users":{"syncMode":"cdc"}}}}}`,
					),
					func(*terraform.State) error {
						if extra := server.PatchCount.Load() - patchesAfterCreate; extra != 0 {
							return fmt.Errorf("expected no PATCH for the equivalent-alias no-op, got %d", extra)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccIntegrationSchema_columnOnlyChangeIsPatched(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	apiToken := testAccAPIToken(t)

	// A column-only edit (hashed false -> true, syncMode unchanged) is a real change, not a no-op:
	// it must fire exactly one PATCH so the setting reaches the API instead of being dropped.
	columnConfig := func(hashed string) string {
		return strings.Join([]string{
			`            syncMode = "incremental"`,
			`            columns = {`,
			`              email = {`,
			`                hashed = ` + hashed,
			`              }`,
			`            }`,
		}, "\n")
	}

	var patchesAfterCreate int64
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccIntegrationSchemaConfig(server.URL, apiToken, columnConfig("false")),
				Check: func(*terraform.State) error {
					patchesAfterCreate = server.PatchCount.Load()
					return nil
				},
			},
			{
				// A successful apply here also proves state stays consistent: the test framework fails
				// the step on its own if the provider returns a result inconsistent with the plan.
				Config: testAccIntegrationSchemaConfig(server.URL, apiToken, columnConfig("true")),
				Check: func(*terraform.State) error {
					if extra := server.PatchCount.Load() - patchesAfterCreate; extra != 1 {
						return fmt.Errorf("expected exactly one PATCH for the column-only change, got %d", extra)
					}
					return nil
				},
			},
		},
	})
}

func TestAccIntegrationSchema_coercedValueAppliesThenShowsDrift(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	apiToken := testAccAPIToken(t)
	server.CoerceSyncMode()

	// The schemas API answers 200 while silently keeping a syncMode the source does not support.
	// Create must still store the planned config - projecting the server's value here would return
	// a result inconsistent with the plan and abort the apply. The mismatch is drift, so it belongs
	// in the next plan, which is what ExpectNonEmptyPlan asserts.
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccIntegrationSchemaConfig(server.URL, apiToken, `            syncMode = "full_refresh"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(
						"matia_integration_schema.test",
						"config",
						`{"schemas":{"public":{"tables":{"users":{"syncMode":"full_refresh"}}}}}`,
					),
					func(*terraform.State) error {
						if got := server.TableField("public", "users", "syncMode"); got != "CHANGE_STREAM" {
							return fmt.Errorf("fake server should have kept CHANGE_STREAM, got %v", got)
						}
						return nil
					},
				),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAccIntegrationSchema_reformattedConfigDoesNotChurn(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	apiToken := testAccAPIToken(t)

	// config is jsontypes.Normalized, so a hand-formatted JSON literal must survive Read rewriting
	// it into compact sorted form. Without semantic equality this step fails on a non-empty plan.
	prettyConfig := testAccProviderConfig(server.URL, apiToken) + fmt.Sprintf(`
resource "matia_integration_schema" "test" {
  integration_id = %q
  config         = <<-EOT
    {
      "schemas": {
        "public": {
          "tables": {
            "users": { "syncMode": "cdc", "enabled": true }
          }
        }
      }
    }
  EOT
}
`, testAccIntegrationSchemaID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: prettyConfig,
				Check: resource.TestCheckResourceAttrWith(
					"matia_integration_schema.test", "config",
					func(value string) error {
						if !strings.Contains(value, "\n") {
							return fmt.Errorf("expected the user's multi-line JSON to survive, got %q", value)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestIntegrationSchemaToModel_KeepsPlannedConfigVerbatim(t *testing.T) {
	t.Parallel()

	// Create and Update must echo the planned config: it is Required, so Terraform rejects a
	// post-apply value that differs from the plan. Reconciling against the API belongs in Read.
	userConfig := `{"schemas":{"public":{"tables":{"users":{"syncMode":"cdc"}}}}}`
	catalog := `{"schemas":{"public":{"tables":{"users":{"syncMode":"FULL_REFRESH"}}}}}`

	model := integrationSchemaToModel(catalog, integrationSchemaModel{
		IntegrationID: types.StringValue(testAccIntegrationSchemaID),
		Config:        jsontypes.NewNormalizedValue(userConfig),
	})

	require.Equal(t, testAccIntegrationSchemaID, model.IntegrationID.ValueString())
	require.JSONEq(t, userConfig, model.Config.ValueString())
	require.JSONEq(t, catalog, model.EffectiveSchema.ValueString())
}

func TestAccIntegrationSchema_undiscoveredTableConverges(t *testing.T) {
	server := testAccStartSchemaCatalogServer(t)
	apiToken := testAccAPIToken(t)

	// A table the catalog does not list (renamed upstream, or a typo) must not put Terraform in a
	// loop: the API answers 200 and skips it, so if Read dropped it from config every plan would
	// show the same change forever. The framework fails this step if the post-apply plan is dirty.
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(server.URL, apiToken) + fmt.Sprintf(`
resource "matia_integration_schema" "test" {
  integration_id = %q
  config         = jsonencode({
    schemas = {
      public = {
        tables = {
          users        = { syncMode = "cdc" }
          orders_typo  = { enabled  = true }
        }
      }
    }
  })
}
`, testAccIntegrationSchemaID),
				Check: resource.TestCheckResourceAttr(
					"matia_integration_schema.test",
					"config",
					`{"schemas":{"public":{"tables":{"orders_typo":{"enabled":true},"users":{"syncMode":"cdc"}}}}}`,
				),
			},
		},
	})
}

func TestRefreshIntegrationSchemaModel_ProjectsCatalogOntoDeclaredKeys(t *testing.T) {
	t.Parallel()

	state := integrationSchemaModel{
		IntegrationID: types.StringValue(testAccIntegrationSchemaID),
		Config:        jsontypes.NewNormalizedValue(`{"schemas":{"public":{"tables":{"users":{"syncMode":"cdc"}}}}}`),
	}
	catalog := `{"schemas":{"public":{"tables":{` +
		`"users":{"syncMode":"FULL_REFRESH","enabled":false},` +
		`"audit_log":{"syncMode":"CHANGE_STREAM"}}}}}`

	model, diags := refreshIntegrationSchemaModel(catalog, state)

	require.False(t, diags.HasError(), "%v", diags)
	require.JSONEq(
		t,
		`{"schemas":{"public":{"tables":{"users":{"syncMode":"Full Refresh"}}}}}`,
		model.Config.ValueString(),
	)
	require.JSONEq(t, catalog, model.EffectiveSchema.ValueString())
}

// refreshIntegrationSchemaModel must carry warnings out on its SUCCESS path, not just errors -
// narrowing that return to nil would silently drop the whole feature, since Read only forwards
// what it returns. Nothing above this layer can catch it: the acceptance framework
// (terraform-plugin-testing v1.16.0) asserts step errors but not warnings.
func TestRefreshIntegrationSchemaModel_PropagatesMissingSchemaWarning(t *testing.T) {
	t.Parallel()

	state := integrationSchemaModel{
		IntegrationID: types.StringValue(testAccIntegrationSchemaID),
		Config: jsontypes.NewNormalizedValue(
			`{"schemas":{"public":{"tables":{"users":{"enabled":true}}},"gone":{"tables":{"x":{"enabled":true}}}}}`,
		),
	}
	catalog := `{"schemas":{"public":{"tables":{"users":{"enabled":true}}}}}`

	_, diags := refreshIntegrationSchemaModel(catalog, state)

	require.False(t, diags.HasError(), "%v", diags)
	require.Equal(t, 1, diags.WarningsCount())
	require.Contains(t, diags.Warnings()[0].Detail(), `"gone"`)
}
