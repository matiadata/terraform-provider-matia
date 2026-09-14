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

func TestAccAsset_updatePreservesIntegration(t *testing.T) {
	for _, kind := range []string{"source", "destination"} {
		for _, change := range []string{"rename", "rotate_secret"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				assetURL := testAccStartAssetsServer(t)
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
					case r.Method == http.MethodGet && r.URL.Path == "/v1/assets/source-1":
						// Existing source used by the destination-only test.
						integrations.ServeHTTP(w, r)
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

				name, secret := "example-asset", "initial-password"
				updatedName, updatedSecret := name, secret
				patchName := ""
				if change == "rename" {
					updatedName = "renamed-asset"
					patchName = updatedName
				} else {
					updatedSecret = "rotated-password"
				}
				assetAddress := "matia_" + kind + ".test"
				sourceID, destinationID := `"source-1"`, `"dest-1"`
				if kind == "source" {
					sourceID = assetAddress + ".id"
				} else {
					destinationID = assetAddress + ".id"
				}
				config := func(name, secret string) string {
					return testAccProviderConfig(server.URL+"/v1", testAccAPIToken(t)) + fmt.Sprintf(`
resource "matia_%[1]s" "test" {
  name               = %[2]q
  type               = "postgres"
  connection_config  = jsonencode({ hostname = "localhost" })
  connection_secrets = jsonencode({ password = %[3]q })
}

resource "matia_integration" "test" {
  source_id          = %[4]s
  destination_id     = %[5]s
  destination_schema = "raw"
}

resource "matia_integration_schedule" "test" {
  integration_id        = matia_integration.test.id
  replication_frequency = "hourly"
}

resource "matia_integration_schema" "test" {
  integration_id = matia_integration.test.id
  config = jsonencode({ schemas = { public = { tables = {
    users = { enabled = true }
  } } } })
}
`, kind, name, secret, sourceID, destinationID)
				}
				resource.Test(t, resource.TestCase{
					ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
					Steps: []resource.TestStep{
						{Config: config(name, secret)},
						{
							Config: config(updatedName, updatedSecret),
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
								resource.TestCheckResourceAttr(assetAddress, "name", updatedName),
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
									if patch.Name != patchName || patch.Connection["password"] != updatedSecret {
										return errors.New("asset PATCH did not carry the expected name and password")
									}
									return nil
								},
							),
						},
						{
							Config: strings.Replace(
								config(updatedName, updatedSecret),
								`type               = "postgres"`,
								`type               = "mysql"`,
								1,
							),
							PlanOnly:           true,
							ExpectNonEmptyPlan: true,
							ConfigPlanChecks: resource.ConfigPlanChecks{
								PostApplyPreRefresh: []plancheck.PlanCheck{
									plancheck.ExpectResourceAction(assetAddress, plancheck.ResourceActionReplace),
									plancheck.ExpectResourceAction(
										"matia_integration.test",
										plancheck.ResourceActionReplace,
									),
								},
							},
						},
					},
				})
				mu.Lock()
				defer mu.Unlock()
				require.ElementsMatch(t, []string{"/v1/assets/asset-1", "/v1/integrations/integration-1"}, deletes)
			})
		}
	}
}
