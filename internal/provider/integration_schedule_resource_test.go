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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
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
