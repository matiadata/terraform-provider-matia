package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/stretchr/testify/require"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

// Read refreshes the configured metadata from Matia, so an asset renamed
// outside Terraform is planned back to the configured name instead of the
// rename being absorbed into state as a silent no-op.
func TestMultiPurposeAssetToModel_ReadRefreshesMetadata(t *testing.T) {
	t.Parallel()

	template := multiPurposeAssetModel{
		Name:        types.StringValue("configured"),
		Type:        types.StringValue("snowflake"),
		Description: types.StringValue("configured description"),
	}
	asset := &client.Asset{
		Name:        "renamed-outside-terraform",
		Type:        "snowflake",
		Description: "edited outside terraform",
	}

	refreshed, diags := multiPurposeAssetToModel(t.Context(), asset, template, refreshFromAPI)
	require.False(t, diags.HasError(), "unexpected diags: %v", diags)
	require.Equal(t, "renamed-outside-terraform", refreshed.Name.ValueString())
	require.Equal(t, "edited outside terraform", refreshed.Description.ValueString())

	planned, diags := multiPurposeAssetToModel(t.Context(), asset, template, preservePlanValue)
	require.False(t, diags.HasError(), "unexpected diags: %v", diags)
	require.Equal(t, "configured", planned.Name.ValueString())
	require.Equal(t, "configured description", planned.Description.ValueString())
}

// A description cleared in Matia has to come back as null, or Terraform never
// plans it back and state keeps claiming a description the API no longer holds.
func TestMultiPurposeAssetToModel_ReadSurfacesClearedDescription(t *testing.T) {
	t.Parallel()

	template := multiPurposeAssetModel{
		Name:        types.StringValue("warehouse"),
		Type:        types.StringValue("snowflake"),
		Description: types.StringValue("configured description"),
	}
	asset := &client.Asset{Name: "warehouse", Type: "snowflake"}

	refreshed, diags := multiPurposeAssetToModel(t.Context(), asset, template, refreshFromAPI)
	require.False(t, diags.HasError(), "unexpected diags: %v", diags)
	require.True(t, refreshed.Description.IsNull(), "description = %v, want null", refreshed.Description)
}

func TestIntegrationToModel_ReadRefreshesDestinationSelection(t *testing.T) {
	t.Parallel()

	template := integrationModel{
		Name:                 types.StringValue("configured"),
		DestinationDatabase:  types.StringValue("STAGING"),
		DestinationWarehouse: types.StringValue("BULK_WH"),
	}
	integration := &client.Integration{
		ID:   "integration-1",
		Name: "renamed-outside-terraform",
		DestinationSettings: &client.IntegrationDestinationSettings{
			SelectedDatabase:  "ANALYTICS",
			SelectedWarehouse: "REPORTING_WH",
		},
	}

	refreshed := integrationToModel(integration, template, refreshFromAPI)
	require.Equal(t, "ANALYTICS", refreshed.DestinationDatabase.ValueString())
	require.Equal(t, "REPORTING_WH", refreshed.DestinationWarehouse.ValueString())

	planned := integrationToModel(integration, template, preservePlanValue)
	require.Equal(t, "STAGING", planned.DestinationDatabase.ValueString())
	require.Equal(t, "BULK_WH", planned.DestinationWarehouse.ValueString())
}

// The selection is refreshed only from a settings block the API actually
// returned. A destination that reports none carries no selection to refresh
// from, so the configured value stands rather than being cleared into a
// spurious replacement.
func TestIntegrationToModel_ReadKeepsSelectionWhenAPIOmitsSettings(t *testing.T) {
	t.Parallel()

	template := integrationModel{
		DestinationDatabase:  types.StringValue("STAGING"),
		DestinationWarehouse: types.StringValue("BULK_WH"),
	}
	integration := &client.Integration{ID: "integration-1"}

	refreshed := integrationToModel(integration, template, refreshFromAPI)
	require.Equal(t, "STAGING", refreshed.DestinationDatabase.ValueString())
	require.Equal(t, "BULK_WH", refreshed.DestinationWarehouse.ValueString())
}

// An unconfigured selection refreshes from the API without forcing a
// replacement, because destination_database and destination_warehouse use
// RequiresReplaceIfConfigured.
func TestAccIntegration_unconfiguredSelectionRefreshesWithoutReplacement(t *testing.T) {
	const resourceName = "matia_integration.test"
	apiURL, _ := testAccStartIntegrationsServerRecordingCreates(t)
	apiToken := testAccAPIToken(t)

	config := testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration" "test" {
  source_id            = "source-1"
  destination_id       = "dest-1"
  destination_schema   = "raw"
  destination_settings = jsonencode({ selectedDatabase = "STAGING", selectedWarehouse = "BULK_WH" })
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "destination_database", "STAGING"),
					resource.TestCheckResourceAttr(resourceName, "destination_warehouse", "BULK_WH"),
				),
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}

// A rename made outside Terraform has to plan back to the configured name.
func TestAccAsset_externalRenamePlansBack(t *testing.T) {
	const resourceName = "matia_asset.test"
	apiURL, server := testAccStartMultiPurposeAssetsServer(t)
	apiToken := testAccAPIToken(t)

	config := testAccAssetConfig(apiURL, apiToken, "warehouse", `  description = "configured description"`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "name", "warehouse"),
					resource.TestCheckResourceAttr(resourceName, "description", "configured description"),
				),
			},
			{
				PreConfig: func() {
					server.mu.Lock()
					defer server.mu.Unlock()
					for id, asset := range server.assets {
						asset.Name = "renamed-in-matia"
						asset.Description = "edited in matia"
						server.assets[id] = asset
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "name", "warehouse"),
					resource.TestCheckResourceAttr(resourceName, "description", "configured description"),
				),
			},
		},
	})
}
