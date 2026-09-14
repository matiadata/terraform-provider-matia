package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/stretchr/testify/require"
)

// Exercise Terraform's dependency planning, not just the integration Update handler:
// an unknown integration ID can replace both children even when Update preserves it.
func TestAccIntegration_stableIDPlan(t *testing.T) {
	integrationURL, err := url.Parse(strings.TrimSuffix(testAccStartIntegrationsServer(t), "/v1"))
	require.NoError(t, err)
	catalog := testAccStartSchemaCatalogServer(t)
	catalogURL, err := url.Parse(strings.TrimSuffix(catalog.URL, "/v1"))
	require.NoError(t, err)
	integrationProxy := httputil.NewSingleHostReverseProxy(integrationURL)
	catalogProxy := httputil.NewSingleHostReverseProxy(catalogURL)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/schemas") {
			r.URL.Path = "/v1/integrations/" + testAccIntegrationSchemaID + "/schemas"
			catalogProxy.ServeHTTP(w, r)
			return
		}
		integrationProxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	apiURL := server.URL + "/v1"
	token := testAccAPIToken(t)
	config := func(source, destination, agent string) string {
		return testAccProviderConfig(apiURL, token) + fmt.Sprintf(`
resource "matia_integration" "test" {
 source_id = %q
 destination_id = %q
 destination_schema = "raw"
 agent_id = %q
}
resource "matia_integration_schedule" "test" {
 integration_id = matia_integration.test.id
 replication_frequency = "manual"
}
resource "matia_integration_schema" "test" {
 integration_id = matia_integration.test.id
 config = jsonencode({schemas = {public = {tables = {users = {enabled = true, syncMode = "change_stream"}}}}})
}
`, source, destination, agent)
	}
	const integration = "matia_integration.test"
	const schedule = "matia_integration_schedule.test"
	const schema = "matia_integration_schema.test"
	stableChecks := []plancheck.PlanCheck{
		plancheck.ExpectResourceAction(integration, plancheck.ResourceActionUpdate),
		plancheck.ExpectKnownValue(integration, tfjsonpath.New("id"), knownvalue.StringExact("integration-1")),
		plancheck.ExpectResourceAction(schedule, plancheck.ResourceActionNoop),
		plancheck.ExpectResourceAction(schema, plancheck.ResourceActionNoop),
	}
	replacementChecks := []plancheck.PlanCheck{
		plancheck.ExpectResourceAction(integration, plancheck.ResourceActionDestroyBeforeCreate),
		plancheck.ExpectUnknownValue(integration, tfjsonpath.New("id")),
		plancheck.ExpectResourceAction(schedule, plancheck.ResourceActionDestroyBeforeCreate),
		plancheck.ExpectResourceAction(schema, plancheck.ResourceActionDestroyBeforeCreate),
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckAPIResourceDestroyed(apiURL, token, "/integrations", integration),
		Steps: []resource.TestStep{
			{Config: config("source-1", "dest-1", "agent-1")},
			{
				Config:           config("source-1", "dest-1", "agent-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: stableChecks},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(integration, "id", "integration-1"),
					resource.TestCheckResourceAttr(integration, "agent_id", "agent-2"),
					resource.TestCheckResourceAttr(schedule, "integration_id", "integration-1"),
					resource.TestCheckResourceAttr(schema, "integration_id", "integration-1"),
				),
			},
			{
				Config: config("source-1", "dest-1", "agent-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config:           config("source-2", "dest-1", "agent-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: replacementChecks},
			},
			{
				Config:           config("source-2", "dest-2", "agent-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: replacementChecks},
			},
		},
	})
}
