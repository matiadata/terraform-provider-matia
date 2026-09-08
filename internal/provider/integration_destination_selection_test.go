package provider

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func TestAccIntegration_destinationSelection(t *testing.T) {
	const resourceName = "matia_integration.test"
	apiURL, createBodies := testAccStartIntegrationsServerRecordingCreates(t)
	apiToken := testAccAPIToken(t)

	config := func(database string) string {
		return testAccProviderConfig(apiURL, apiToken) + fmt.Sprintf(`
resource "matia_integration" "test" {
  source_id             = "source-1"
  destination_id        = "dest-1"
  destination_schema    = "raw"
  destination_database  = %q
  destination_warehouse = "BULK_WH"
  destination_settings  = jsonencode({ stageName = "internal" })
}
`, database)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("STAGING"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "destination_database", "STAGING"),
					resource.TestCheckResourceAttr(resourceName, "destination_warehouse", "BULK_WH"),
					func(_ *terraform.State) error {
						bodies := createBodies()
						if len(bodies) != 1 {
							return fmt.Errorf("expected one create, got %d", len(bodies))
						}
						var req client.CreateIntegrationRequest
						if err := json.Unmarshal(bodies[0], &req); err != nil {
							return err
						}
						want := map[string]any{
							"stageName":         "internal",
							"selectedDatabase":  "STAGING",
							"selectedWarehouse": "BULK_WH",
						}
						if fmt.Sprint(req.DestinationSettings) != fmt.Sprint(want) {
							return fmt.Errorf("destinationSettings = %v, want %v", req.DestinationSettings, want)
						}
						return nil
					},
				),
			},
			{
				Config:                  config("STAGING"),
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"destination_settings"},
			},
			{
				Config: config("ANALYTICS"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionReplace),
					},
				},
			},
		},
	})
}
