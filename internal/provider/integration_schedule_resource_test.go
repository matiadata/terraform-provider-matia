package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/stretchr/testify/require"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

// Schedule and schema Deletes are intentionally no-ops: destroying those
// resources stops managing the sub-resource but must leave the integration
// itself intact.
func testAccCheckIntegrationSurvivesDestroy(apiURL, apiToken, resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("%s not found in pre-destroy state", resourceName)
		}
		integrationID := rs.Primary.Attributes["integration_id"]
		if integrationID == "" {
			return fmt.Errorf("%s has no integration_id in pre-destroy state", resourceName)
		}
		resp, err := testAccAPIGet(apiURL+"/integrations/"+integrationID, apiToken)
		if err != nil {
			return fmt.Errorf("checking integration %s after destroy: %w", integrationID, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf(
				"integration %s should remain after destroy: expected GET to return 200, got %d",
				integrationID,
				resp.StatusCode,
			)
		}
		return nil
	}
}

func testAccStartIntegrationScheduleTransientServer(t *testing.T) string {
	t.Helper()

	const integrationID = "integration-1"
	store := map[string]string{
		integrationID: testAccIntegrationJSON(integrationID, "source-1", "dest-1", "raw"),
	}
	patchCalls := 0
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/integrations/"+integrationID:
			payload, ok := store[integrationID]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"NotFound_Integration","message":"integration not found"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(payload))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/integrations/"+integrationID:
			delete(store, integrationID)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/integrations/"+integrationID:
			patchCalls++
			if patchCalls == 1 {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write(
					[]byte(
						`{"code":"IntegrationInCreation","message":"Integration is in creation. Please try again later."}`,
					),
				)
				return
			}

			body, _ := io.ReadAll(r.Body)
			var req client.ModifyIntegrationRequest
			if err := json.Unmarshal(body, &req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			store[integrationID] = testAccApplySchedulePatch(store[integrationID], req)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

func TestAccIntegrationSchedule_basic(t *testing.T) {
	apiURL := testAccStartIntegrationsServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: testAccCheckIntegrationSurvivesDestroy(
			apiURL,
			apiToken,
			"matia_integration_schedule.test",
		),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration_schedule" "test" {
  integration_id        = "integration-1"
  replication_frequency = "hourly"
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(
						"matia_integration_schedule.test",
						"integration_id",
						"integration-1",
					),
					resource.TestCheckResourceAttr(
						"matia_integration_schedule.test",
						"replication_frequency",
						"hourly",
					),
				),
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration_schedule" "test" {
  integration_id        = "integration-1"
  replication_frequency = "daily"
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_integration_schedule.test", "replication_frequency", "daily"),
				),
			},
		},
	})
}

func TestAccIntegrationSchedule_retriesIntegrationInCreation(t *testing.T) {
	apiURL := testAccStartIntegrationScheduleTransientServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: testAccCheckIntegrationSurvivesDestroy(
			apiURL,
			apiToken,
			"matia_integration_schedule.test",
		),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration_schedule" "test" {
  integration_id        = "integration-1"
  replication_frequency = "hourly"
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(
						"matia_integration_schedule.test",
						"integration_id",
						"integration-1",
					),
					resource.TestCheckResourceAttr(
						"matia_integration_schedule.test",
						"replication_frequency",
						"hourly",
					),
				),
			},
		},
	})
}

// Import supplies no prior state, so the alias in state comes purely from
// reversing the API value. Both import kinds are covered here because this is
// the provider's reference import test: ImportBlockWithID additionally asserts
// the post-import plan is a no-op.
func TestAccIntegrationSchedule_import(t *testing.T) {
	const resourceName = "matia_integration_schedule.test"

	apiURL := testAccStartIntegrationsServer(t)
	apiToken := testAccAPIToken(t)
	importID := testAccImportStateIDFunc(resourceName, "integration_id")

	config := testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration_schedule" "test" {
  integration_id        = "integration-1"
  replication_frequency = "hourly"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckIntegrationSurvivesDestroy(apiURL, apiToken, resourceName),
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				Config:                               config,
				ResourceName:                         resourceName,
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateIdFunc:                    importID,
				ImportStateVerifyIdentifierAttribute: "integration_id",
			},
			{
				Config:            config,
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateKind:   resource.ImportBlockWithID,
				ImportStateIdFunc: importID,
			},
		},
	})
}

func testAccStartIntegrationScheduleAPIOwnedServer(t *testing.T) string {
	t.Helper()

	return testAccStartIntegrationsServerWithSeeds(t, nil, map[string]client.ModifyIntegrationRequest{
		"integration-1": {
			ReplicationFrequency: "60",
			CronExpression:       "0 * * * *",
			BaseTime:             "2024-01-01T03:00:00Z",
		},
		"integration-2": {
			ReplicationFrequency: "60",
			CronExpression:       "30 * * * *",
			BaseTime:             "2025-06-06T09:00:00Z",
		},
	})
}

const testAccIntegrationScheduleOmittedConfig = `
resource "matia_integration_schedule" "test" {
  integration_id        = "integration-1"
  replication_frequency = "hourly"
}
`

// The API keeps returning cronExpression/baseTime whether or not the config
// mentions them, so a config that omits them must still converge.
func TestAccIntegrationSchedule_omittedAPIOwnedFields(t *testing.T) {
	const resourceName = "matia_integration_schedule.test"

	apiURL := testAccStartIntegrationScheduleAPIOwnedServer(t)
	apiToken := testAccAPIToken(t)
	config := testAccProviderConfig(apiURL, apiToken) + testAccIntegrationScheduleOmittedConfig

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckIntegrationSurvivesDestroy(apiURL, apiToken, resourceName),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "replication_frequency", "hourly"),
					resource.TestCheckResourceAttr(resourceName, "cron_expression", "0 * * * *"),
					resource.TestCheckResourceAttr(resourceName, "base_time", "2024-01-01T03:00:00Z"),
				),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// Optional+Computed only makes the API value authoritative while the config is
// silent: an explicit value still wins over whatever Matia already had stored.
// The store is seeded with different values and merges the PATCH before the
// provider ever reads it, so only the request body distinguishes precedence.
func TestAccIntegrationSchedule_configOverridesAPIOwnedFields(t *testing.T) {
	const resourceName = "matia_integration_schedule.test"

	var mu sync.Mutex
	var patches []client.ModifyIntegrationRequest
	apiURL := testAccStartIntegrationsServerWithSeeds(
		t,
		func(_ string, req client.ModifyIntegrationRequest, _ map[string]json.RawMessage) {
			mu.Lock()
			defer mu.Unlock()
			patches = append(patches, req)
		},
		map[string]client.ModifyIntegrationRequest{
			"integration-1": {
				ReplicationFrequency: "60",
				CronExpression:       "0 * * * *",
				BaseTime:             "2024-01-01T03:00:00Z",
			},
		},
	)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckIntegrationSurvivesDestroy(apiURL, apiToken, resourceName),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration_schedule" "test" {
  integration_id        = "integration-1"
  replication_frequency = "cron"
  cron_expression       = "15 2 * * *"
  base_time             = "2026-02-02T02:00:00Z"
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "replication_frequency", "cron"),
					resource.TestCheckResourceAttr(resourceName, "cron_expression", "15 2 * * *"),
					resource.TestCheckResourceAttr(resourceName, "base_time", "2026-02-02T02:00:00Z"),
				),
			},
		},
	})

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, patches, "expected the create to PATCH the schedule")
	for _, patch := range patches {
		require.Equal(t, "15 2 * * *", patch.CronExpression)
		require.Equal(t, "2026-02-02T02:00:00Z", patch.BaseTime)
	}
}

func TestAccIntegrationSchedule_importOmittedAPIOwnedFields(t *testing.T) {
	const resourceName = "matia_integration_schedule.test"

	apiURL := testAccStartIntegrationScheduleAPIOwnedServer(t)
	apiToken := testAccAPIToken(t)
	importID := testAccImportStateIDFunc(resourceName, "integration_id")
	config := testAccProviderConfig(apiURL, apiToken) + testAccIntegrationScheduleOmittedConfig

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckIntegrationSurvivesDestroy(apiURL, apiToken, resourceName),
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				Config:                               config,
				ResourceName:                         resourceName,
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateIdFunc:                    importID,
				ImportStateVerifyIdentifierAttribute: "integration_id",
			},
			{
				Config:            config,
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateKind:   resource.ImportBlockWithID,
				ImportStateIdFunc: importID,
			},
		},
	})
}

// Changing replication_frequency must not churn the fields the config omits:
// UseStateForUnknown keeps them at their prior value instead of planning them
// as "(known after apply)".
func TestAccIntegrationSchedule_updateKeepsOmittedAPIOwnedFields(t *testing.T) {
	const resourceName = "matia_integration_schedule.test"

	apiURL := testAccStartIntegrationScheduleAPIOwnedServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckIntegrationSurvivesDestroy(apiURL, apiToken, resourceName),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + testAccIntegrationScheduleOmittedConfig,
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration_schedule" "test" {
  integration_id        = "integration-1"
  replication_frequency = "daily"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectKnownValue(
							resourceName,
							tfjsonpath.New("cron_expression"),
							knownvalue.StringExact("0 * * * *"),
						),
						plancheck.ExpectKnownValue(
							resourceName,
							tfjsonpath.New("base_time"),
							knownvalue.StringExact("2024-01-01T03:00:00Z"),
						),
					},
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "replication_frequency", "daily"),
					resource.TestCheckResourceAttr(resourceName, "cron_expression", "0 * * * *"),
					resource.TestCheckResourceAttr(resourceName, "base_time", "2024-01-01T03:00:00Z"),
				),
			},
		},
	})
}

// The Optional+Computed trade-off, pinned deliberately: Matia keeps its stored
// value when the config stops mentioning the attribute, so the removal plans
// empty rather than clearing the schedule.
func TestAccIntegrationSchedule_removingConfiguredFieldKeepsAPIValue(t *testing.T) {
	const resourceName = "matia_integration_schedule.test"

	apiURL := testAccStartIntegrationsServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckIntegrationSurvivesDestroy(apiURL, apiToken, resourceName),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration_schedule" "test" {
  integration_id        = "integration-1"
  replication_frequency = "hourly"
  base_time             = "2024-01-01T03:00:00Z"
}
`,
				Check: resource.TestCheckResourceAttr(resourceName, "base_time", "2024-01-01T03:00:00Z"),
			},
			{
				Config:   testAccProviderConfig(apiURL, apiToken) + testAccIntegrationScheduleOmittedConfig,
				PlanOnly: true,
			},
		},
	})
}

// cron_expression is unknown rather than null in the plan once it is Computed,
// so the "required when replication_frequency is cron" guard has to hold on a
// config that never mentions it. Prior state is what relaxes the guard, not the
// API, so the update path still errors while state carries no expression.
func TestAccIntegrationSchedule_cronRequiresExpression(t *testing.T) {
	apiURL := testAccStartIntegrationsServer(t)
	apiToken := testAccAPIToken(t)

	cronConfig := testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration_schedule" "test" {
  integration_id        = "integration-1"
  replication_frequency = "cron"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      cronConfig,
				ExpectError: regexp.MustCompile(`cron_expression is required`),
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + testAccIntegrationScheduleOmittedConfig,
			},
			{
				Config:      cronConfig,
				ExpectError: regexp.MustCompile(`cron_expression is required`),
			},
		},
	})
}

// Making cron_expression Computed deliberately relaxes the guard: once an
// expression is in state, switching replication_frequency to cron without
// re-declaring it is allowed. Adopting an integration whose cron schedule the
// API already owns depends on this.
func TestAccIntegrationSchedule_switchToCronReusesStoredExpression(t *testing.T) {
	const resourceName = "matia_integration_schedule.test"

	var mu sync.Mutex
	var cronPatches []string
	apiURL := testAccStartIntegrationsServerWithSeeds(
		t,
		func(_ string, req client.ModifyIntegrationRequest, _ map[string]json.RawMessage) {
			mu.Lock()
			defer mu.Unlock()
			if req.ReplicationFrequency == "cron" {
				cronPatches = append(cronPatches, req.CronExpression)
			}
		},
		map[string]client.ModifyIntegrationRequest{
			"integration-1": {
				ReplicationFrequency: "60",
				CronExpression:       "0 * * * *",
				BaseTime:             "2024-01-01T03:00:00Z",
			},
		},
	)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckIntegrationSurvivesDestroy(apiURL, apiToken, resourceName),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + testAccIntegrationScheduleOmittedConfig,
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration_schedule" "test" {
  integration_id        = "integration-1"
  replication_frequency = "cron"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectKnownValue(
							resourceName,
							tfjsonpath.New("cron_expression"),
							knownvalue.StringExact("0 * * * *"),
						),
					},
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "replication_frequency", "cron"),
					resource.TestCheckResourceAttr(resourceName, "cron_expression", "0 * * * *"),
				),
			},
		},
	})

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, cronPatches, "expected a PATCH switching replication_frequency to cron")
	for _, expression := range cronPatches {
		require.Equal(t, "0 * * * *", expression)
	}
}

// integration_id forces replacement, so UseStateForUnknown must not carry the
// old integration's schedule into the new one.
func TestAccIntegrationSchedule_replacementDropsPriorAPIOwnedFields(t *testing.T) {
	const resourceName = "matia_integration_schedule.test"

	apiURL := testAccStartIntegrationScheduleAPIOwnedServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckIntegrationSurvivesDestroy(apiURL, apiToken, resourceName),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + testAccIntegrationScheduleOmittedConfig,
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration_schedule" "test" {
  integration_id        = "integration-2"
  replication_frequency = "hourly"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectUnknownValue(resourceName, tfjsonpath.New("cron_expression")),
						plancheck.ExpectUnknownValue(resourceName, tfjsonpath.New("base_time")),
					},
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "cron_expression", "30 * * * *"),
					resource.TestCheckResourceAttr(resourceName, "base_time", "2025-06-06T09:00:00Z"),
				),
			},
		},
	})
}

func TestScheduleToModel_ImportHasNoTemplate(t *testing.T) {
	t.Parallel()

	model := scheduleToModel(
		&client.Integration{
			ID:                   "integration-1",
			ReplicationFrequency: "60",
			CronExpression:       "0 * * * *",
			BaseTime:             "2024-01-01T00:00:00Z",
		},
		"integration-1",
		integrationScheduleModel{},
	)
	require.Equal(t, "integration-1", model.IntegrationID.ValueString())
	require.Equal(t, "hourly", model.ReplicationFrequency.ValueString())
	require.Equal(t, "0 * * * *", model.CronExpression.ValueString())
	require.Equal(t, "2024-01-01T00:00:00Z", model.BaseTime.ValueString())
}

func TestScheduleToModel_UsesTemplateFields(t *testing.T) {
	t.Parallel()

	model := scheduleToModel(
		&client.Integration{
			ID:                   "integration-1",
			ReplicationFrequency: "60",
			CronExpression:       "0 * * * *",
			BaseTime:             "2024-01-01T00:00:00Z",
		},
		"integration-1",
		integrationScheduleModel{
			ReplicationFrequency: types.StringValue("hourly"),
			CronExpression:       types.StringValue("planned-cron"),
			BaseTime:             types.StringValue("planned-base"),
		},
	)
	require.Equal(t, "integration-1", model.IntegrationID.ValueString())
	require.Equal(t, "hourly", model.ReplicationFrequency.ValueString())
	require.Equal(t, "0 * * * *", model.CronExpression.ValueString())
	require.Equal(t, "2024-01-01T00:00:00Z", model.BaseTime.ValueString())
}
