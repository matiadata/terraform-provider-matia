package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func TestAccMultiPurposeAsset_updatePreservesIntegration(t *testing.T) {
	for _, change := range []string{"rotate_secret", "change_defaults"} {
		t.Run(change, func(t *testing.T) {
			assetURL, _ := testAccStartMultiPurposeAssetsServer(t)
			integrationURL := testAccStartIntegrationsServer(t)
			catalog := testAccStartSchemaCatalogServer(t)
			proxyFor := func(apiURL string) *httputil.ReverseProxy {
				target, err := url.Parse(strings.TrimSuffix(apiURL, "/v1"))
				require.NoError(t, err)
				return httputil.NewSingleHostReverseProxy(target)
			}
			assets := proxyFor(assetURL)
			integrations := proxyFor(integrationURL)
			schemas := proxyFor(catalog.URL)
			var mu sync.Mutex
			var deletes []string
			var patches [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					mu.Lock()
					deletes = append(deletes, r.URL.Path)
					mu.Unlock()
				}
				switch {
				case strings.HasPrefix(r.URL.Path, "/v1/assets"):
					if r.Method == http.MethodPatch {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							http.Error(w, err.Error(), http.StatusBadRequest)
							return
						}
						_ = r.Body.Close()
						r.Body = io.NopCloser(bytes.NewReader(body))
						mu.Lock()
						patches = append(patches, body)
						mu.Unlock()
					}
					assets.ServeHTTP(w, r)
				case strings.HasSuffix(r.URL.Path, "/schemas"):
					schemas.ServeHTTP(w, r)
				default:
					integrations.ServeHTTP(w, r)
				}
			}))
			defer server.Close()

			secret := "etl-secret"
			updatedSecret := "rotated-password"
			database, warehouse := "RAW", "LOAD_WH"
			if change == "change_defaults" {
				updatedSecret = secret
				database, warehouse = "NEW_RAW", "NEW_WH"
			}
			assetAddress := "matia_asset.test"
			config := func(secret, database, warehouse string) string {
				asset := testAccAssetConfig(server.URL+"/v1", testAccAPIToken(t), "warehouse", "")
				asset = strings.Replace(asset, `password  = "etl-secret"`, fmt.Sprintf("password = %q", secret), 1)
				asset = strings.Replace(asset, `database  = "RAW"`, fmt.Sprintf("database = %q", database), 1)
				asset = strings.Replace(asset, `warehouse = "LOAD_WH"`, fmt.Sprintf("warehouse = %q", warehouse), 1)
				return asset + `
resource "matia_integration" "test" {
  source_id = "source-1"
  destination_id = matia_asset.test.id
  destination_schema = "raw"
}
resource "matia_integration_schedule" "test" {
  integration_id = matia_integration.test.id
  replication_frequency = "hourly"
}
resource "matia_integration_schema" "test" {
  integration_id = matia_integration.test.id
  config = jsonencode({ schemas = { public = { tables = {
    users = { enabled = true }
  } } } })
}
`
			}

			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{Config: config(secret, "RAW", "LOAD_WH")},
					{
						Config: config(updatedSecret, database, warehouse),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{
								plancheck.ExpectResourceAction(assetAddress, plancheck.ResourceActionUpdate),
								plancheck.ExpectResourceAction(
									"matia_integration.test",
									plancheck.ResourceActionNoop,
								),
								plancheck.ExpectResourceAction(
									"matia_integration_schedule.test",
									plancheck.ResourceActionNoop,
								),
								plancheck.ExpectResourceAction(
									"matia_integration_schema.test",
									plancheck.ResourceActionNoop,
								),
							},
						},
						Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr(assetAddress, "id", "asset-1"),
							resource.TestCheckResourceAttr(assetAddress, "default_database", database),
							resource.TestCheckResourceAttr(assetAddress, "default_warehouse", warehouse),
							resource.TestCheckResourceAttr("matia_integration.test", "id", "integration-1"),
							func(_ *terraform.State) error {
								mu.Lock()
								defer mu.Unlock()
								if len(deletes) != 0 || len(patches) != 1 {
									return fmt.Errorf(
										"expected one asset PATCH and no DELETEs, got %d PATCHes and DELETEs %v",
										len(patches),
										deletes,
									)
								}
								var patch client.UpdateAssetRequest
								if err := json.Unmarshal(patches[0], &patch); err != nil {
									return err
								}
								etl, _ := patch.Connection["etl"].(map[string]any)
								if etl["password"] != updatedSecret || etl["database"] != database ||
									etl["warehouse"] != warehouse ||
									len(patch.Connection) != 3 {
									return errors.New("asset PATCH did not carry the expected purpose credentials")
								}
								return nil
							},
						),
					},
				},
			})
			mu.Lock()
			defer mu.Unlock()
			require.ElementsMatch(t, []string{"/v1/assets/asset-1", "/v1/integrations/integration-1"}, deletes)
		})
	}
}

func TestAccMultiPurposeAsset_sharedRotationAndLayoutChanges(t *testing.T) {
	apiURL, server := testAccStartMultiPurposeAssetsServer(t)
	apiToken := testAccAPIToken(t)
	shared := testAccAssetSharedCredentialsConfig(apiURL, apiToken, testAccAssetCatalogOverride)
	rotated := strings.Replace(
		shared,
		`private_key = "PLACEHOLDER-PEM-NOT-A-REAL-KEY\n"`,
		`private_key = "ROTATED-PEM-NOT-A-REAL-KEY\n"`,
		1,
	)
	restored := strings.Replace(rotated, testAccAssetCatalogOverride, "", 1)
	perPurpose := testAccAssetConfig(apiURL, apiToken, "warehouse", "")
	sharedAgain := testAccAssetSharedCredentialsConfig(apiURL, apiToken, "")
	steps := []resource.TestStep{{Config: shared}}
	for i, tc := range []struct {
		config      string
		etlUser     string
		catalogUser string
		privateKey  string
	}{
		{rotated, "MATIA_USER", "OBS_USER", "ROTATED-PEM-NOT-A-REAL-KEY\n"},
		{restored, "MATIA_USER", "MATIA_USER", "ROTATED-PEM-NOT-A-REAL-KEY\n"},
		{perPurpose, "ETL_USER", "OBS_USER", ""},
		{sharedAgain, "MATIA_USER", "MATIA_USER", "PLACEHOLDER-PEM-NOT-A-REAL-KEY\n"},
	} {
		steps = append(steps, resource.TestStep{
			Config: tc.config,
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("matia_asset.test", plancheck.ResourceActionUpdate),
			}},
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("matia_asset.test", "id", "asset-1"),
				func(_ *terraform.State) error {
					server.mu.Lock()
					defer server.mu.Unlock()
					if len(server.createBodies) != 1 || len(server.patchBodies) != i+1 {
						return fmt.Errorf("expected one create and %d PATCHes", i+1)
					}
					var req client.UpdateAssetRequest
					if err := json.Unmarshal(server.patchBodies[i], &req); err != nil {
						return err
					}
					if len(req.Connection) != 3 {
						return errors.New("expected three purpose blocks")
					}
					etl, _ := req.Connection["etl"].(map[string]any)
					catalog, _ := req.Connection["catalog"].(map[string]any)
					if etl["username"] != tc.etlUser || catalog["username"] != tc.catalogUser {
						return errors.New("incorrect shared credentials or override precedence")
					}
					if tc.privateKey != "" && etl["private_key"] != tc.privateKey {
						return errors.New("private key rotation was not sent")
					}
					if tc.privateKey == "" {
						if _, ok := etl["private_key"]; ok {
							return errors.New("old private key must not be resent with password credentials")
						}
						if etl["password"] != "etl-secret" {
							return errors.New("password credentials were not sent")
						}
					}
					return nil
				},
			),
		})
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	})
}
