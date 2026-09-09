package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

// testAccMultiPurposeAssetsServer models the v1 assets API for Snowflake:
// creation keeps only the ETL default database and warehouse (credentials are
// never stored or returned), and every request body is kept for assertions.
type testAccMultiPurposeAssetsServer struct {
	mu           sync.Mutex
	assets       map[string]testAccStoredMultiPurposeAsset
	createBodies [][]byte
	patchBodies  [][]byte
	nextID       int
}

type testAccStoredMultiPurposeAsset struct {
	Name                 string
	Description          string
	ConnectionType       string
	DefaultDatabase      string
	DefaultWarehouse     string
	AdditionalDatabases  []string
	AdditionalWarehouses []string
}

func (s *testAccMultiPurposeAssetsServer) payload(id string) string {
	asset := s.assets[id]
	dbs, _ := json.Marshal(asset.AdditionalDatabases)
	whs, _ := json.Marshal(asset.AdditionalWarehouses)
	description := ""
	if asset.Description != "" {
		description = fmt.Sprintf(`,"description":%q`, asset.Description)
	}
	return fmt.Sprintf(
		`{"code":"success","data":{"id":%q,"name":%q%s,"type":"snowflake","connectionType":%q,"configuration":{"etl":{"additionalDatabases":%s,"additionalWarehouses":%s,"defaultDatabase":%q,"defaultWarehouse":%q}}}}`,
		id,
		asset.Name,
		description,
		asset.ConnectionType,
		dbs,
		whs,
		asset.DefaultDatabase,
		asset.DefaultWarehouse,
	)
}

func (s *testAccMultiPurposeAssetsServer) lastCreate(t *testing.T) client.CreateAssetRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.createBodies)
	var req client.CreateAssetRequest
	require.NoError(t, json.Unmarshal(s.createBodies[len(s.createBodies)-1], &req))
	return req
}

func testAccStartMultiPurposeAssetsServer(t *testing.T) (string, *testAccMultiPurposeAssetsServer) {
	t.Helper()

	s := &testAccMultiPurposeAssetsServer{assets: map[string]testAccStoredMultiPurposeAsset{}, nextID: 1}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/assets":
			body, _ := io.ReadAll(r.Body)
			s.createBodies = append(s.createBodies, body)
			var req client.CreateAssetRequest
			if err := json.Unmarshal(body, &req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			etl := req.Connection
			if nested, ok := req.Connection["etl"].(map[string]any); ok {
				etl = nested
			}
			if override, ok := req.ConnectionOverrides["etl"].(map[string]any); ok {
				etl = override
			}
			stored := testAccStoredMultiPurposeAsset{
				Name:                 req.Name,
				Description:          req.Description,
				ConnectionType:       "multi_purpose",
				DefaultDatabase:      fmt.Sprint(etl["database"]),
				DefaultWarehouse:     fmt.Sprint(etl["warehouse"]),
				AdditionalDatabases:  []string{},
				AdditionalWarehouses: []string{},
			}
			if req.Configuration != nil && req.Configuration.Etl != nil {
				if req.Configuration.Etl.AdditionalDatabases != nil {
					stored.AdditionalDatabases = *req.Configuration.Etl.AdditionalDatabases
				}
				if req.Configuration.Etl.AdditionalWarehouses != nil {
					stored.AdditionalWarehouses = *req.Configuration.Etl.AdditionalWarehouses
				}
			}

			id := fmt.Sprintf("asset-%d", s.nextID)
			s.nextID++
			s.assets[id] = stored
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"code":"success","data":{"id":%q}}`, id)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/assets/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/assets/")
			if _, ok := s.assets[id]; !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"NotFound","message":"asset not found"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(s.payload(id)))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/assets/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/assets/")
			stored, ok := s.assets[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			body, _ := io.ReadAll(r.Body)
			s.patchBodies = append(s.patchBodies, body)
			var req client.UpdateAssetRequest
			if err := json.Unmarshal(body, &req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if req.Name != "" {
				stored.Name = req.Name
			}
			if req.Description != nil {
				stored.Description = *req.Description
			}
			if req.Configuration != nil && req.Configuration.Etl != nil {
				if req.Configuration.Etl.AdditionalDatabases != nil {
					stored.AdditionalDatabases = *req.Configuration.Etl.AdditionalDatabases
				}
				if req.Configuration.Etl.AdditionalWarehouses != nil {
					stored.AdditionalWarehouses = *req.Configuration.Etl.AdditionalWarehouses
				}
			}
			s.assets[id] = stored
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/assets/"):
			delete(s.assets, strings.TrimPrefix(r.URL.Path, "/v1/assets/"))
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1", s
}

const testAccAssetPurposeBlocks = `
  etl = {
    account   = "myorg-myaccount"
    username  = "ETL_USER"
    database  = "RAW"
    warehouse = "LOAD_WH"
    password  = "etl-secret"
  }
  reverse_etl = {
    account   = "myorg-myaccount"
    username  = "RETL_USER"
    database  = "MART"
    warehouse = "RETL_WH"
    password  = "retl-secret"
  }
  catalog = {
    account   = "myorg-myaccount"
    username  = "OBS_USER"
    warehouse = "OBS_WH"
    password  = "obs-secret"
  }
`

const testAccAssetEtlSourceBlock = `
  etl_source = {
    account                = "myorg-myaccount"
    username               = "SRC_USER"
    database               = "RAW"
    warehouse              = "SRC_WH"
    private_key            = "PLACEHOLDER-PEM-NOT-A-REAL-KEY\n"
    private_key_passphrase = "src-passphrase"
  }
`

func testAccAssetConfig(apiURL, apiToken, name, extra string) string {
	return testAccProviderConfig(apiURL, apiToken) + fmt.Sprintf(`
resource "matia_asset" "test" {
  name = %q
  type = "snowflake"
%s
%s
}
`, name, testAccAssetPurposeBlocks, extra)
}

func testAccAssetSharedCredentialsConfig(apiURL, apiToken, overrides string) string {
	return testAccProviderConfig(apiURL, apiToken) + fmt.Sprintf(`
resource "matia_asset" "test" {
  name        = "warehouse"
  type        = "snowflake"
  auth_method = "keyPair"
  owners      = ["6aa0382d49359e5d73e424bb"]

  credentials = {
    account     = "myorg-myaccount"
    username    = "MATIA_USER"
    database    = "RAW"
    warehouse   = "LOAD_WH"
    private_key = "PLACEHOLDER-PEM-NOT-A-REAL-KEY\n"
    public_key  = "PLACEHOLDER-PUBLIC-KEY-SHARED"
  }
%s
}
`, overrides)
}

const testAccAssetCatalogOverride = `
  catalog = {
    account     = "myorg-myaccount"
    username    = "OBS_USER"
    warehouse   = "OBS_WH"
    private_key = "PLACEHOLDER-PEM-NOT-A-REAL-KEY\n"
    public_key  = "PLACEHOLDER-PUBLIC-KEY-CATALOG"
  }
`

func TestAccAsset_basic(t *testing.T) {
	apiURL, server := testAccStartMultiPurposeAssetsServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: testAccCheckAPIResourceDestroyed(
			apiURL,
			apiToken,
			"/assets",
			"matia_asset.test",
		),
		Steps: []resource.TestStep{
			{
				Config: testAccAssetConfig(apiURL, apiToken, "warehouse", testAccAssetEtlSourceBlock+`
  description          = "shared warehouse"
  additional_databases = ["STAGING", "staging", " ANALYTICS "]
`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("matia_asset.test", "id"),
					resource.TestCheckResourceAttr("matia_asset.test", "connection_type", "multi_purpose"),
					resource.TestCheckResourceAttr("matia_asset.test", "default_database", "RAW"),
					resource.TestCheckResourceAttr("matia_asset.test", "default_warehouse", "LOAD_WH"),
					resource.TestCheckResourceAttr("matia_asset.test", "additional_databases.#", "3"),
					resource.TestCheckNoResourceAttr("matia_asset.test", "additional_warehouses"),
					resource.TestCheckResourceAttr("matia_asset.test", "etl.password", "etl-secret"),
					resource.TestCheckResourceAttr("matia_asset.test", "etl_source.username", "SRC_USER"),
					func(_ *terraform.State) error {
						server.mu.Lock()
						defer server.mu.Unlock()
						var raw map[string]json.RawMessage
						if err := json.Unmarshal(server.createBodies[0], &raw); err != nil {
							return err
						}
						if _, ok := raw["connectionType"]; ok {
							return fmt.Errorf("create body must omit connectionType: %s", server.createBodies[0])
						}
						if string(raw["owners"]) != "[]" || string(raw["description"]) != `"shared warehouse"` {
							return fmt.Errorf("owners must be [] and description sent: %s", server.createBodies[0])
						}
						if _, ok := raw["connectionOverrides"]; ok {
							return fmt.Errorf("purpose blocks must not be overrides: %s", server.createBodies[0])
						}
						var connection map[string]map[string]any
						if err := json.Unmarshal(raw["connection"], &connection); err != nil {
							return fmt.Errorf("connection is not nested per purpose: %s", raw["connection"])
						}
						wrongRetl := connection["reverseEtl"]["database"] != "MART"
						wrongSource := connection["etlSource"]["username"] != "SRC_USER" ||
							connection["etlSource"]["private_key_passphrase"] != "src-passphrase"
						if wrongRetl || wrongSource {
							return fmt.Errorf("unexpected nested connection: %s", raw["connection"])
						}
						if _, ok := connection["catalog"]["database"]; ok {
							return fmt.Errorf("catalog must not carry an unset database: %s", raw["connection"])
						}
						return nil
					},
				),
			},
			{
				// The API de-duplicates and trims; the reported list differs from
				// the configured one only in ways the resource must not plan.
				PreConfig: func() {
					server.mu.Lock()
					defer server.mu.Unlock()
					asset := server.assets["asset-1"]
					asset.AdditionalDatabases = []string{"STAGING", "ANALYTICS"}
					server.assets["asset-1"] = asset
				},
				Config: testAccAssetConfig(apiURL, apiToken, "warehouse", testAccAssetEtlSourceBlock+`
  description          = "shared warehouse"
  additional_databases = ["STAGING", "staging", " ANALYTICS "]
`),
				PlanOnly: true,
			},
			{
				Config: testAccAssetConfig(apiURL, apiToken, "warehouse-renamed", testAccAssetEtlSourceBlock+`
  additional_databases  = ["STAGING"]
  additional_warehouses = []
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("matia_asset.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_asset.test", "name", "warehouse-renamed"),
					resource.TestCheckNoResourceAttr("matia_asset.test", "description"),
					resource.TestCheckResourceAttr("matia_asset.test", "additional_databases.#", "1"),
					resource.TestCheckResourceAttr("matia_asset.test", "additional_warehouses.#", "0"),
					func(_ *terraform.State) error {
						server.mu.Lock()
						defer server.mu.Unlock()
						if len(server.patchBodies) != 1 {
							return fmt.Errorf("expected one PATCH, got %d", len(server.patchBodies))
						}
						var raw map[string]json.RawMessage
						if err := json.Unmarshal(server.patchBodies[0], &raw); err != nil {
							return err
						}
						if _, ok := raw["connection"]; ok {
							return fmt.Errorf("PATCH must never carry credentials: %s", server.patchBodies[0])
						}
						if string(raw["description"]) != `""` {
							return fmt.Errorf("removing description must clear it: %s", server.patchBodies[0])
						}
						wantConfiguration := `{"etl":{"additionalDatabases":["STAGING"],"additionalWarehouses":[]}}`
						if string(raw["configuration"]) != wantConfiguration {
							return fmt.Errorf("unexpected configuration patch: %s", raw["configuration"])
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "matia_asset.test",
				ImportState:       true,
				ImportStateVerify: true,
				// Credentials never come back from the API, so an imported asset
				// has none until the next apply adopts the configured blocks.
				ImportStateVerifyIgnore: []string{"etl", "reverse_etl", "catalog", "etl_source", "credentials"},
			},
		},
	})
}

func TestAccAsset_sharedCredentialsWithOverride(t *testing.T) {
	apiURL, server := testAccStartMultiPurposeAssetsServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccAssetSharedCredentialsConfig(apiURL, apiToken, testAccAssetCatalogOverride),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_asset.test", "default_database", "RAW"),
					resource.TestCheckResourceAttr(
						"matia_asset.test",
						"credentials.private_key",
						"PLACEHOLDER-PEM-NOT-A-REAL-KEY\n",
					),
					func(_ *terraform.State) error {
						req := server.lastCreate(t)
						if req.AuthMethod != "keyPair" || len(req.Owners) != 1 {
							return fmt.Errorf("authMethod/owners not forwarded: %+v", req)
						}
						if req.Connection["username"] != "MATIA_USER" {
							return fmt.Errorf("shared credentials must be sent flat: %+v", req.Connection)
						}
						if _, ok := req.ConnectionOverrides["catalog"]; !ok || len(req.ConnectionOverrides) != 1 {
							return fmt.Errorf("catalog must be the only override: %+v", req.ConnectionOverrides)
						}
						if req.Connection["public_key"] != "PLACEHOLDER-PUBLIC-KEY-SHARED" {
							return fmt.Errorf("shared public_key not forwarded: %+v", req.Connection)
						}
						catalogOverride, _ := req.ConnectionOverrides["catalog"].(map[string]any)
						if catalogOverride["public_key"] != "PLACEHOLDER-PUBLIC-KEY-CATALOG" {
							return fmt.Errorf("catalog public_key not forwarded: %+v", req.ConnectionOverrides)
						}
						return nil
					},
				),
			},
			{
				// The API cannot add a purpose to an existing asset.
				Config: testAccAssetSharedCredentialsConfig(apiURL, apiToken, testAccAssetCatalogOverride+`
  etl_source = {
    account   = "myorg-myaccount"
    username  = "SRC_USER"
    database  = "RAW"
    warehouse = "SRC_WH"
    password  = "src-secret"
  }
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("matia_asset.test", plancheck.ResourceActionReplace),
					},
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_asset.test", "id", "asset-2"),
					func(_ *terraform.State) error {
						req := server.lastCreate(t)
						if len(req.ConnectionOverrides) != 2 {
							return fmt.Errorf("replacement must carry both overrides: %+v", req.ConnectionOverrides)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccAsset_credentialChangeReplaces(t *testing.T) {
	apiURL, server := testAccStartMultiPurposeAssetsServer(t)
	apiToken := testAccAPIToken(t)

	withSource := testAccAssetConfig(apiURL, apiToken, "warehouse", testAccAssetEtlSourceBlock)
	rotated := strings.Replace(withSource, `password  = "etl-secret"`, `password  = "rotated"`, 1)
	withoutSource := strings.Replace(rotated, testAccAssetEtlSourceBlock, "", 1)

	expectReplace := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{
			plancheck.ExpectResourceAction("matia_asset.test", plancheck.ResourceActionReplace),
		},
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: withSource,
			},
			{
				Config:           rotated,
				ConfigPlanChecks: expectReplace,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_asset.test", "id", "asset-2"),
					resource.TestCheckResourceAttr("matia_asset.test", "etl.password", "rotated"),
				),
			},
			{
				// Dropping a purpose is a credentials change too.
				Config:           withoutSource,
				ConfigPlanChecks: expectReplace,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_asset.test", "id", "asset-3"),
					resource.TestCheckNoResourceAttr("matia_asset.test", "etl_source"),
					func(_ *terraform.State) error {
						server.mu.Lock()
						defer server.mu.Unlock()
						if len(server.patchBodies) != 0 {
							return fmt.Errorf("a credentials change must never PATCH: %d", len(server.patchBodies))
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccAsset_importAdoptsConfiguredCredentials(t *testing.T) {
	apiURL, server := testAccStartMultiPurposeAssetsServer(t)
	apiToken := testAccAPIToken(t)

	created := testAccAssetConfig(apiURL, apiToken, "warehouse", "")
	// A second address for the same Matia asset: the framework cannot drop a
	// resource from state mid-test, so the import lands on its own address.
	both := created + strings.Replace(
		strings.TrimPrefix(created, testAccProviderConfig(apiURL, apiToken)),
		`resource "matia_asset" "test"`, `resource "matia_asset" "imported"`, 1,
	)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: created,
			},
			{
				Config:             both,
				ResourceName:       "matia_asset.imported",
				ImportState:        true,
				ImportStateId:      "asset-1",
				ImportStatePersist: true,
			},
			{
				Config: both,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("matia_asset.imported", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("matia_asset.test", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_asset.imported", "id", "asset-1"),
					resource.TestCheckResourceAttr("matia_asset.imported", "etl.password", "etl-secret"),
					func(_ *terraform.State) error {
						server.mu.Lock()
						defer server.mu.Unlock()
						if len(server.patchBodies) != 0 || len(server.createBodies) != 1 {
							return fmt.Errorf(
								"adoption must not call the API: %d creates, %d patches",
								len(server.createBodies), len(server.patchBodies),
							)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccAsset_importRejectsSinglePurposeAsset(t *testing.T) {
	apiURL, server := testAccStartMultiPurposeAssetsServer(t)
	apiToken := testAccAPIToken(t)
	server.assets["dest-1"] = testAccStoredMultiPurposeAsset{
		Name:                 "legacy",
		ConnectionType:       "destination",
		AdditionalDatabases:  []string{},
		AdditionalWarehouses: []string{},
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        testAccAssetConfig(apiURL, apiToken, "legacy", ""),
				ResourceName:  "matia_asset.test",
				ImportState:   true,
				ImportStateId: "dest-1",
				ExpectError:   regexp.MustCompile(`Not a multipurpose asset`),
			},
		},
	})
}

func TestAccAsset_configValidation(t *testing.T) {
	apiURL, _ := testAccStartMultiPurposeAssetsServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_asset" "test" {
  name = "warehouse"
  type = "snowflake"
  etl = {
    account   = "myorg-myaccount"
    username  = "ETL_USER"
    database  = "RAW"
    warehouse = "LOAD_WH"
    password  = "etl-secret"
  }
}
`,
				ExpectError: regexp.MustCompile(`Missing: reverse_etl, catalog`),
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_asset" "test" {
  name = "warehouse"
  type = "snowflake"
  credentials = {
    account   = "myorg-myaccount"
    username  = "ETL_USER"
    database  = "RAW"
    warehouse = "LOAD_WH"
  }
}
`,
				ExpectError: regexp.MustCompile(`On credentials: set password or private_key`),
			},
			{
				Config: strings.Replace(
					testAccAssetConfig(apiURL, apiToken, "warehouse", ""),
					`    database  = "MART"`, "", 1,
				),
				ExpectError: regexp.MustCompile(`On reverse_etl: set database or database_schemas`),
			},
			{
				// Rows stand in for the database on reverse ETL only. The ETL
				// destination resolver reads the single database, so an ETL
				// block listing rows alone must still be rejected.
				Config: strings.Replace(
					testAccAssetConfig(apiURL, apiToken, "warehouse", ""),
					`    database  = "RAW"`,
					`    database_schemas = [{ database = "RAW", schema = "PUBLIC" }]`, 1,
				),
				ExpectError: regexp.MustCompile(`On etl: set database\.`),
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_asset" "test" {
  name = "warehouse"
  type = "snowflake"
  credentials = {
    account   = "myorg-myaccount"
    username  = "MATIA_USER"
    warehouse = "LOAD_WH"
    password  = "p"
  }
}
`,
				ExpectError: regexp.MustCompile(`On credentials: set database`),
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_asset" "test" {
  name = "warehouse"
  type = "snowflake"
  credentials = {
    account   = ""
    username  = "y"
    warehouse = "z"
    password  = "p"
  }
}
`,
				ExpectError: regexp.MustCompile(`string length must be at least 1`),
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_asset" "test" {
  name = "warehouse"
  type = "bigquery"
  credentials = {
    account   = "x"
    username  = "y"
    warehouse = "z"
    password  = "p"
  }
}
`,
				ExpectError: regexp.MustCompile(`value must be one of: \["snowflake"\]`),
			},
			{
				// EAuthMethod spells it keyPair; the credentials-level enum
				// spells it keypair, so the wrong one is easy to reach for.
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_asset" "test" {
  name        = "warehouse"
  type        = "snowflake"
  auth_method = "keypair"
  credentials = {
    account   = "x"
    username  = "y"
    database  = "d"
    warehouse = "z"
    password  = "p"
  }
}
`,
				ExpectError: regexp.MustCompile(`value must be one of: \["direct" "keyPair"\]`),
			},
		},
	})
}

func TestReconcileResourceList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	configured, diags := types.ListValueFrom(ctx, types.StringType, []string{"staging", " ANALYTICS ", "STAGING"})
	require.False(t, diags.HasError())

	kept, diags := reconcileResourceList(ctx, configured, []string{"ANALYTICS", "STAGING"})
	require.False(t, diags.HasError())
	require.True(t, kept.Equal(configured), "an order/case/whitespace-only difference keeps the configured list")

	replaced, diags := reconcileResourceList(ctx, configured, []string{"STAGING"})
	require.False(t, diags.HasError())
	var replacedValues []string
	require.False(t, replaced.ElementsAs(ctx, &replacedValues, false).HasError())
	require.Equal(t, []string{"STAGING"}, replacedValues)

	imported, diags := reconcileResourceList(ctx, types.ListNull(types.StringType), []string{"EXTRA"})
	require.False(t, diags.HasError())
	var importedValues []string
	require.False(t, imported.ElementsAs(ctx, &importedValues, false).HasError())
	require.Equal(t, []string{"EXTRA"}, importedValues)

	absent, diags := reconcileResourceList(ctx, types.ListUnknown(types.StringType), nil)
	require.False(t, diags.HasError())
	require.True(t, absent.IsNull())
}

func TestBuildMultiPurposeAssetUpdateRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	state := multiPurposeAssetModel{
		Name:                 types.StringValue("warehouse"),
		Description:          types.StringValue("old"),
		Owners:               mustStringList(t, ctx, []string{"6aa0382d49359e5d73e424bb"}),
		AdditionalDatabases:  mustStringList(t, ctx, []string{"STAGING"}),
		AdditionalWarehouses: types.ListNull(types.StringType),
	}

	t.Run("unchanged fields send nothing", func(t *testing.T) {
		t.Parallel()
		req, changed, diags := buildMultiPurposeAssetUpdateRequest(ctx, state, state)
		require.False(t, diags.HasError())
		require.False(t, changed)
		require.Nil(t, req.Configuration)
		require.Nil(t, req.Description)
		require.Nil(t, req.Owners)
	})

	t.Run("an empty list clears, an untouched list is omitted", func(t *testing.T) {
		t.Parallel()
		plan := state
		plan.AdditionalDatabases = mustStringList(t, ctx, []string{})
		req, changed, diags := buildMultiPurposeAssetUpdateRequest(ctx, plan, state)
		require.False(t, diags.HasError())
		require.True(t, changed)
		require.NotNil(t, req.Configuration.Etl.AdditionalDatabases)
		require.Empty(t, *req.Configuration.Etl.AdditionalDatabases)
		require.Nil(t, req.Configuration.Etl.AdditionalWarehouses)
	})

	t.Run("removed owners are left alone", func(t *testing.T) {
		t.Parallel()
		plan := state
		plan.Owners = types.ListNull(types.StringType)
		req, changed, diags := buildMultiPurposeAssetUpdateRequest(ctx, plan, state)
		require.False(t, diags.HasError())
		require.False(t, changed)
		require.Nil(t, req.Owners)
	})

	t.Run("removed description and emptied owners are sent as clears", func(t *testing.T) {
		t.Parallel()
		plan := state
		plan.Description = types.StringNull()
		plan.Owners = mustStringList(t, ctx, []string{})
		req, changed, diags := buildMultiPurposeAssetUpdateRequest(ctx, plan, state)
		require.False(t, diags.HasError())
		require.True(t, changed)
		require.NotNil(t, req.Description)
		require.Empty(t, *req.Description)
		require.NotNil(t, req.Owners)
		require.Empty(t, *req.Owners)
	})
}

func mustStringList(t *testing.T, ctx context.Context, values []string) types.List {
	t.Helper()
	list, diags := types.ListValueFrom(ctx, types.StringType, values)
	require.False(t, diags.HasError(), diagsSummary(diags))
	return list
}

func diagsSummary(diags diag.Diagnostics) string {
	var parts []string
	for _, d := range diags {
		parts = append(parts, d.Summary()+": "+d.Detail())
	}
	return strings.Join(parts, "; ")
}

func TestAccAsset_importWithMetadataResendsOnlyMetadata(t *testing.T) {
	apiURL, server := testAccStartMultiPurposeAssetsServer(t)
	apiToken := testAccAPIToken(t)

	created := testAccAssetSharedCredentialsConfig(apiURL, apiToken, testAccAssetCatalogOverride)
	both := created + strings.Replace(
		strings.TrimPrefix(created, testAccProviderConfig(apiURL, apiToken)),
		`resource "matia_asset" "test"`, `resource "matia_asset" "imported"`, 1,
	)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: created,
			},
			{
				Config:             both,
				ResourceName:       "matia_asset.imported",
				ImportState:        true,
				ImportStateId:      "asset-1",
				ImportStatePersist: true,
			},
			{
				Config: both,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("matia_asset.imported", plancheck.ResourceActionUpdate),
					},
				},
				Check: func(_ *terraform.State) error {
					server.mu.Lock()
					defer server.mu.Unlock()
					if len(server.patchBodies) != 1 {
						return fmt.Errorf("expected exactly one PATCH, got %d", len(server.patchBodies))
					}
					want := `{"authMethod":"keyPair","owners":["6aa0382d49359e5d73e424bb"]}`
					if string(server.patchBodies[0]) != want {
						return fmt.Errorf("PATCH after import = %s, want %s", server.patchBodies[0], want)
					}
					return nil
				},
			},
		},
	})
}

func TestAccAsset_tagsAreSentOnCreate(t *testing.T) {
	apiURL, server := testAccStartMultiPurposeAssetsServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccAssetConfig(apiURL, apiToken, "warehouse", `
  tags = ["6aa0382d49359e5d73e424cc"]
`),
				Check: func(_ *terraform.State) error {
					req := server.lastCreate(t)
					if len(req.Tags) != 1 || req.Tags[0] != "6aa0382d49359e5d73e424cc" {
						return fmt.Errorf("tags not forwarded: %+v", req.Tags)
					}
					return nil
				},
			},
		},
	})
}

func databaseSchemaAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"database": types.StringType,
		"schema":   types.StringType,
	}
}

func snowflakeCredentialsAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"account":                types.StringType,
		"username":               types.StringType,
		"warehouse":              types.StringType,
		"database":               types.StringType,
		"database_schemas":       types.ListType{ElemType: types.ObjectType{AttrTypes: databaseSchemaAttrTypes()}},
		"password":               types.StringType,
		"private_key":            types.StringType,
		"private_key_passphrase": types.StringType,
		"public_key":             types.StringType,
	}
}

func mustDatabaseSchemas(t *testing.T, ctx context.Context, rows ...[2]string) types.List {
	t.Helper()
	models := make([]databaseSchemaModel, 0, len(rows))
	for _, row := range rows {
		models = append(models, databaseSchemaModel{
			Database: types.StringValue(row[0]),
			Schema:   types.StringValue(row[1]),
		})
	}
	list, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: databaseSchemaAttrTypes()}, models)
	require.False(t, diags.HasError(), diagsSummary(diags))
	return list
}

func TestSnowflakeCredentialsToAPISendsDatabaseSchemas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	nullRows := types.ListNull(types.ObjectType{AttrTypes: databaseSchemaAttrTypes()})

	withRows, diags := types.ObjectValueFrom(ctx, snowflakeCredentialsAttrTypes(), snowflakeCredentialsModel{
		Account:         types.StringValue("acct"),
		Username:        types.StringValue("retl"),
		Warehouse:       types.StringValue("RETL_WH"),
		Database:        types.StringNull(),
		DatabaseSchemas: mustDatabaseSchemas(t, ctx, [2]string{"MART", "PUBLIC"}, [2]string{"REPORTING", "ANALYTICS"}),
		Password:        types.StringValue("pw"),
	})
	require.False(t, diags.HasError(), diagsSummary(diags))

	connection, diags := snowflakeCredentialsToAPI(ctx, withRows)
	require.False(t, diags.HasError(), diagsSummary(diags))
	require.Equal(
		t,
		[]map[string]string{
			{"database": "MART", "schema": "PUBLIC"},
			{"database": "REPORTING", "schema": "ANALYTICS"},
		},
		connection["databaseSchemas"],
		"the rows travel in configured order, which is what Matia stores and the Manage tab lists",
	)
	require.NotContains(t, connection, "database", "an unset database is still omitted")

	withoutRows, diags := types.ObjectValueFrom(ctx, snowflakeCredentialsAttrTypes(), snowflakeCredentialsModel{
		Account:         types.StringValue("acct"),
		Username:        types.StringValue("etl"),
		Warehouse:       types.StringValue("LOAD_WH"),
		Database:        types.StringValue("RAW"),
		DatabaseSchemas: nullRows,
		Password:        types.StringValue("pw"),
	})
	require.False(t, diags.HasError(), diagsSummary(diags))

	connection, diags = snowflakeCredentialsToAPI(ctx, withoutRows)
	require.False(t, diags.HasError(), diagsSummary(diags))
	require.NotContains(
		t,
		connection,
		"databaseSchemas",
		"a block that lists none must not send an empty array, which the API rejects",
	)
	require.Equal(t, "RAW", connection["database"])
}

func TestWarnAdoptedCredentials(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	block, blockDiags := types.ObjectValueFrom(ctx, snowflakeCredentialsAttrTypes(), snowflakeCredentialsModel{
		Account:         types.StringValue("acct"),
		Username:        types.StringValue("user"),
		Warehouse:       types.StringValue("wh"),
		Password:        types.StringValue("pw"),
		DatabaseSchemas: types.ListNull(types.ObjectType{AttrTypes: databaseSchemaAttrTypes()}),
	})
	require.False(t, blockDiags.HasError(), diagsSummary(blockDiags))
	nullBlock := types.ObjectNull(snowflakeCredentialsAttrTypes())

	imported := multiPurposeAssetModel{
		Credentials: nullBlock, Etl: nullBlock, ReverseEtl: nullBlock, Catalog: nullBlock, EtlSource: nullBlock,
	}
	plan := imported
	plan.Etl = block
	plan.Catalog = block

	var diags diag.Diagnostics
	warnAdoptedCredentials(plan, imported, &diags)
	require.Len(t, diags, 1)
	require.Contains(t, diags[0].Detail(), "catalog, etl block(s)")

	created := plan
	var none diag.Diagnostics
	warnAdoptedCredentials(plan, created, &none)
	require.Empty(t, none)
}

func TestSnowflakeCredentialsIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	block := func(model snowflakeCredentialsModel) types.Object {
		value, diags := types.ObjectValueFrom(ctx, snowflakeCredentialsAttrTypes(), model)
		require.False(t, diags.HasError(), diagsSummary(diags))
		return value
	}
	base := snowflakeCredentialsModel{
		Account:         types.StringValue("acct"),
		Username:        types.StringValue("user"),
		Warehouse:       types.StringValue("wh"),
		Database:        types.StringValue("db"),
		DatabaseSchemas: types.ListNull(types.ObjectType{AttrTypes: databaseSchemaAttrTypes()}),
	}

	etlRule := databaseRule{required: true}
	reverseEtlRule := databaseRule{required: true, rowsMayReplace: true}
	catalogRule := databaseRule{}

	unknownKey := base
	unknownKey.PrivateKey = types.StringUnknown()
	issue, diags := snowflakeCredentialsIssue(ctx, block(unknownKey), etlRule)
	require.False(t, diags.HasError())
	require.Empty(t, issue, "a key that resolves at apply counts as present")

	noSecret := base
	issue, diags = snowflakeCredentialsIssue(ctx, block(noSecret), etlRule)
	require.False(t, diags.HasError())
	require.Equal(t, "set password or private_key", issue)

	noDatabase := base
	noDatabase.Password = types.StringValue("pw")
	noDatabase.Database = types.StringNull()

	issue, diags = snowflakeCredentialsIssue(ctx, block(noDatabase), etlRule)
	require.False(t, diags.HasError())
	require.Equal(t, "set database", issue)

	issue, diags = snowflakeCredentialsIssue(ctx, block(noDatabase), reverseEtlRule)
	require.False(t, diags.HasError())
	require.Equal(t, "set database or database_schemas", issue)

	issue, diags = snowflakeCredentialsIssue(ctx, block(noDatabase), catalogRule)
	require.False(t, diags.HasError())
	require.Empty(t, issue)

	// Only reverse ETL reads the list where a database would otherwise be read.
	rowsOnly := noDatabase
	rowsOnly.DatabaseSchemas = mustDatabaseSchemas(t, ctx, [2]string{"MART", "PUBLIC"})

	issue, diags = snowflakeCredentialsIssue(ctx, block(rowsOnly), reverseEtlRule)
	require.False(t, diags.HasError())
	require.Empty(t, issue, "rows stand in for the database on reverse ETL")

	issue, diags = snowflakeCredentialsIssue(ctx, block(rowsOnly), etlRule)
	require.False(t, diags.HasError())
	require.Equal(
		t,
		"set database",
		issue,
		"the ETL destination resolver reads the single database, so rows cannot stand in for it",
	)

	// A list that only resolves at apply must not be read as absent.
	unknownRows := noDatabase
	unknownRows.DatabaseSchemas = types.ListUnknown(types.ObjectType{AttrTypes: databaseSchemaAttrTypes()})

	issue, diags = snowflakeCredentialsIssue(ctx, block(unknownRows), reverseEtlRule)
	require.False(t, diags.HasError())
	require.Empty(t, issue, "an unknown list resolves at apply, like an unknown string")

	issue, diags = snowflakeCredentialsIssue(ctx, block(unknownRows), etlRule)
	require.False(t, diags.HasError())
	require.Equal(t, "set database", issue)
}

// database_schemas can come from another resource, so the whole list is unknown
// while planning. Validation must wait for it rather than call it absent.
func TestAccAsset_databaseSchemasUnknownAtPlanTime(t *testing.T) {
	apiURL, server := testAccStartMultiPurposeAssetsServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "terraform_data" "schemas" {
  input = jsonencode([{ database = "MART", schema = "PUBLIC" }])
}

resource "matia_asset" "test" {
  name = "warehouse"
  type = "snowflake"

  etl = {
    account   = "myorg-myaccount"
    username  = "ETL_USER"
    database  = "RAW"
    warehouse = "LOAD_WH"
    password  = "etl-secret"
  }
  reverse_etl = {
    account   = "myorg-myaccount"
    username  = "RETL_USER"
    warehouse = "RETL_WH"
    password  = "retl-secret"

    database_schemas = jsondecode(terraform_data.schemas.output)
  }
  catalog = {
    account   = "myorg-myaccount"
    username  = "OBS_USER"
    warehouse = "OBS_WH"
    password  = "obs-secret"
  }
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(
						"matia_asset.test", "reverse_etl.database_schemas.0.database", "MART",
					),
					func(_ *terraform.State) error {
						server.mu.Lock()
						defer server.mu.Unlock()
						var raw struct {
							ConnectionOverrides map[string]map[string]any `json:"connectionOverrides"`
							Connection          map[string]map[string]any `json:"connection"`
						}
						if err := json.Unmarshal(server.createBodies[0], &raw); err != nil {
							return err
						}
						block := raw.Connection["reverseEtl"]
						if block == nil {
							block = raw.ConnectionOverrides["reverseEtl"]
						}
						got, err := json.Marshal(block["databaseSchemas"])
						if err != nil {
							return err
						}
						want := `[{"database":"MART","schema":"PUBLIC"}]`
						if string(got) != want {
							return fmt.Errorf("reverseEtl databaseSchemas = %s, want %s", got, want)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestSnowflakeCredentialsToAPI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	block, diags := types.ObjectValueFrom(ctx, snowflakeCredentialsAttrTypes(), snowflakeCredentialsModel{
		Account:         types.StringValue("acct"),
		Username:        types.StringValue("user"),
		Warehouse:       types.StringValue("wh"),
		PrivateKey:      types.StringValue("PLACEHOLDER-PEM-NOT-A-REAL-KEY\n"),
		PublicKey:       types.StringValue("PLACEHOLDER-PUBLIC-KEY"),
		DatabaseSchemas: types.ListNull(types.ObjectType{AttrTypes: databaseSchemaAttrTypes()}),
	})
	require.False(t, diags.HasError(), diagsSummary(diags))

	connection, diags := snowflakeCredentialsToAPI(ctx, block)
	require.False(t, diags.HasError(), diagsSummary(diags))
	require.Equal(t, map[string]any{
		"account":     "acct",
		"username":    "user",
		"warehouse":   "wh",
		"private_key": "PLACEHOLDER-PEM-NOT-A-REAL-KEY\n",
		"public_key":  "PLACEHOLDER-PUBLIC-KEY",
	}, connection)
}
