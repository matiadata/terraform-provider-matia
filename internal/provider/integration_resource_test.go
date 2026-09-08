package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func testAccIntegrationJSON(id, sourceID, destID, schema string, agentID ...*string) string {
	agentIDJSON := "null"
	if len(agentID) > 0 && agentID[0] != nil {
		agentIDJSON = fmt.Sprintf("%q", *agentID[0])
	}

	return fmt.Sprintf(
		`{"code":"success","data":{"id":%q,"name":"tf-integration","paused":false,"source":{"id":%q,"name":"pg","type":"postgres"},"destination":{"id":%q,"name":"sf","type":"snowflake"},"replicationFrequency":"manual","destinationSchema":%q,"onSchemaUpdate":"enableColumnChanges","agentId":%s}}`,
		id,
		sourceID,
		destID,
		schema,
		agentIDJSON,
	)
}

func testAccApplySchedulePatch(
	payload string,
	req client.ModifyIntegrationRequest,
	rawReqs ...map[string]json.RawMessage,
) string {
	var rawReq map[string]json.RawMessage
	if len(rawReqs) > 0 {
		rawReq = rawReqs[0]
	}

	var envelope struct {
		Code string `json:"code"`
		Data struct {
			ID                   string                                 `json:"id"`
			Name                 string                                 `json:"name"`
			Paused               bool                                   `json:"paused"`
			Source               client.IntegrationEndpoint             `json:"source"`
			Destination          client.IntegrationEndpoint             `json:"destination"`
			ReplicationFrequency string                                 `json:"replicationFrequency"`
			CronExpression       string                                 `json:"cronExpression,omitempty"`
			BaseTime             string                                 `json:"baseTime,omitempty"`
			DestinationSchema    string                                 `json:"destinationSchema,omitempty"`
			OnSchemaUpdate       string                                 `json:"onSchemaUpdate,omitempty"`
			AgentID              *string                                `json:"agentId"`
			DestinationSettings  *client.IntegrationDestinationSettings `json:"destinationSettings,omitempty"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return payload
	}

	if req.ReplicationFrequency != "" {
		envelope.Data.ReplicationFrequency = req.ReplicationFrequency
	}
	if req.Name != "" {
		envelope.Data.Name = req.Name
	}
	if req.Paused != nil {
		envelope.Data.Paused = *req.Paused
	}
	if req.DestinationSchema != "" {
		envelope.Data.DestinationSchema = req.DestinationSchema
	}
	if req.OnSchemaUpdate != "" {
		envelope.Data.OnSchemaUpdate = req.OnSchemaUpdate
	}
	if req.CronExpression != "" {
		envelope.Data.CronExpression = req.CronExpression
	}
	if req.BaseTime != "" {
		envelope.Data.BaseTime = req.BaseTime
	}
	if rawAgentID, ok := rawReq["agentId"]; ok {
		if string(rawAgentID) == "null" {
			envelope.Data.AgentID = nil
		} else {
			var agentID string
			if err := json.Unmarshal(rawAgentID, &agentID); err == nil {
				envelope.Data.AgentID = &agentID
			}
		}
	}

	encoded, err := json.Marshal(envelope)
	if err != nil {
		return payload
	}
	return string(encoded)
}

// The real API echoes a Snowflake destination's selected database and
// warehouse back on GET; other destinationSettings keys stay write-only.
func testAccWithDestinationSelection(payload string, settings map[string]any) string {
	selectedDatabase, _ := settings[client.SelectedDatabaseKey].(string)
	selectedWarehouse, _ := settings[client.SelectedWarehouseKey].(string)
	if selectedDatabase == "" && selectedWarehouse == "" {
		return payload
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return payload
	}
	var data map[string]any
	if err := json.Unmarshal(envelope["data"], &data); err != nil {
		return payload
	}
	data["destinationSettings"] = client.IntegrationDestinationSettings{
		SelectedDatabase:  selectedDatabase,
		SelectedWarehouse: selectedWarehouse,
	}
	encodedData, err := json.Marshal(data)
	if err != nil {
		return payload
	}
	envelope["data"] = encodedData
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return payload
	}
	return string(encoded)
}

type testAccIntegrationPatchHook func(id string, req client.ModifyIntegrationRequest, rawReq map[string]json.RawMessage)

func testAccStartIntegrationsServer(t *testing.T) string {
	return testAccStartIntegrationsServerWithSeeds(t, nil, nil)
}

// seedSchedules pre-applies schedules to the given integration ids, modelling
// integrations whose cronExpression/baseTime the API already owns before
// Terraform manages them.
func testAccStartIntegrationsServerWithSeeds(
	t *testing.T,
	patchHook testAccIntegrationPatchHook,
	seedSchedules map[string]client.ModifyIntegrationRequest,
) string {
	return testAccStartIntegrationsServerFull(t, patchHook, seedSchedules, nil)
}

// testAccStartIntegrationsServerRecordingCreates returns the server URL and a
// function that yields every POST /v1/integrations body received so far.
func testAccStartIntegrationsServerRecordingCreates(t *testing.T) (string, func() [][]byte) {
	t.Helper()
	var mu sync.Mutex
	var bodies [][]byte
	apiURL := testAccStartIntegrationsServerFull(t, nil, nil, func(body []byte) {
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, body)
	})
	return apiURL, func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), bodies...)
	}
}

func testAccStartIntegrationsServerFull(
	t *testing.T,
	patchHook testAccIntegrationPatchHook,
	seedSchedules map[string]client.ModifyIntegrationRequest,
	createHook func(body []byte),
) string {
	t.Helper()

	store := map[string]string{
		"integration-1": testAccIntegrationJSON("integration-1", "source-1", "dest-1", "raw"),
	}
	for id, schedule := range seedSchedules {
		store[id] = testAccApplySchedulePatch(testAccIntegrationJSON(id, "source-1", "dest-1", "raw"), schedule)
	}
	var mu sync.Mutex
	nextID := 1

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/integrations":
			body, _ := io.ReadAll(r.Body)
			if createHook != nil {
				createHook(body)
			}
			var req client.CreateIntegrationRequest
			if err := json.Unmarshal(body, &req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			id := fmt.Sprintf("integration-%d", nextID)
			nextID++
			var agentID *string
			if req.AgentID != "" {
				agentID = &req.AgentID
			}
			store[id] = testAccWithDestinationSelection(
				testAccIntegrationJSON(id, req.SourceID, req.DestinationID, req.DestinationSchema, agentID),
				req.DestinationSettings,
			)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"code":"success","data":{"id":%q}}`, id)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/integrations/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/integrations/")
			if strings.Contains(id, "/") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			payload, ok := store[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"NotFound_Integration","message":"integration not found"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(payload))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/integrations/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/integrations/")
			payload, ok := store[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"NotFound_Integration","message":"integration not found"}`))
				return
			}

			body, _ := io.ReadAll(r.Body)
			var rawReq map[string]json.RawMessage
			if err := json.Unmarshal(body, &rawReq); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var req client.ModifyIntegrationRequest
			if err := json.Unmarshal(body, &req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			if patchHook != nil {
				patchHook(id, req, rawReq)
			}
			store[id] = testAccApplySchedulePatch(payload, req, rawReq)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/integrations/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/integrations/")
			delete(store, id)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

func TestAccIntegration_basic(t *testing.T) {
	apiURL := testAccStartIntegrationsServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: testAccCheckAPIResourceDestroyed(
			apiURL,
			apiToken,
			"/integrations",
			"matia_integration.test",
		),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration" "test" {
  source_id          = "source-1"
  destination_id     = "dest-1"
  destination_schema = "raw"
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("matia_integration.test", "id"),
					resource.TestCheckResourceAttr("matia_integration.test", "source_id", "source-1"),
					resource.TestCheckResourceAttr("matia_integration.test", "destination_id", "dest-1"),
					resource.TestCheckResourceAttr("matia_integration.test", "destination_schema", "raw"),
					resource.TestCheckResourceAttr("matia_integration.test", "paused", "false"),
				),
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration" "test" {
  source_id          = "source-1"
  destination_id     = "dest-1"
  destination_schema = "raw"
  paused             = true
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_integration.test", "paused", "true"),
				),
			},
		},
	})
}

// The fields the GET returns round-trip on their own; tags and the two settings
// blobs are never returned, so they are ignored here and asserted to be absent.
func TestAccIntegration_import(t *testing.T) {
	const resourceName = "matia_integration.test"

	t.Run("fields the API returns", func(t *testing.T) {
		apiURL := testAccStartIntegrationsServer(t)
		apiToken := testAccAPIToken(t)
		config := testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration" "test" {
  source_id          = "source-1"
  destination_id     = "dest-1"
  destination_schema = "raw"
  on_schema_update   = "enableColumnChanges"
}
`

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			CheckDestroy: testAccCheckAPIResourceDestroyed(
				apiURL,
				apiToken,
				"/integrations",
				resourceName,
			),
			Steps: []resource.TestStep{
				{
					Config: config,
				},
				{
					Config:            config,
					ResourceName:      resourceName,
					ImportState:       true,
					ImportStateVerify: true,
				},
				{
					Config:          config,
					ResourceName:    resourceName,
					ImportState:     true,
					ImportStateKind: resource.ImportBlockWithID,
				},
			},
		})
	})

	t.Run("fields the API does not return", func(t *testing.T) {
		apiURL := testAccStartIntegrationsServer(t)
		apiToken := testAccAPIToken(t)
		config := testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration" "test" {
  source_id            = "source-1"
  destination_id       = "dest-1"
  destination_schema   = "raw"
  source_settings      = jsonencode({ incremental_mode = "Full Refresh" })
  destination_settings = jsonencode({ mode = "append" })
  tags                 = ["tag-1"]
}
`

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			CheckDestroy: testAccCheckAPIResourceDestroyed(
				apiURL,
				apiToken,
				"/integrations",
				resourceName,
			),
			Steps: []resource.TestStep{
				{
					Config: config,
				},
				{
					Config:                  config,
					ResourceName:            resourceName,
					ImportState:             true,
					ImportStateVerify:       true,
					ImportStateVerifyIgnore: []string{"source_settings", "destination_settings", "tags"},
					ImportStateCheck: func(states []*terraform.InstanceState) error {
						if len(states) != 1 {
							return fmt.Errorf("expected 1 imported state, got %d", len(states))
						}
						for _, name := range []string{"source_settings", "destination_settings", "tags.#"} {
							if value, ok := states[0].Attributes[name]; ok {
								return fmt.Errorf("imported state has %s %q, want none", name, value)
							}
						}
						return nil
					},
				},
				{
					Config:             config,
					ResourceName:       resourceName,
					ImportState:        true,
					ImportStateKind:    resource.ImportBlockWithID,
					ExpectNonEmptyPlan: true,
					ImportPlanChecks: resource.ImportPlanChecks{
						PreApply: []plancheck.PlanCheck{
							plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionReplace),
						},
					},
				},
			},
		})
	})

	t.Run("agent_id and paused", func(t *testing.T) {
		apiURL := testAccStartIntegrationsServer(t)
		apiToken := testAccAPIToken(t)
		attached := testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration" "test" {
  source_id          = "source-1"
  destination_id     = "dest-1"
  destination_schema = "raw"
  agent_id           = "agent-1"
}
`
		// The stub reports every new integration as running, so pausing takes a
		// second apply before there is a paused integration to import.
		pausedAndAttached := testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration" "test" {
  source_id          = "source-1"
  destination_id     = "dest-1"
  destination_schema = "raw"
  agent_id           = "agent-1"
  paused             = true
}
`

		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			CheckDestroy: testAccCheckAPIResourceDestroyed(
				apiURL,
				apiToken,
				"/integrations",
				resourceName,
			),
			Steps: []resource.TestStep{
				{Config: attached},
				{
					Config: pausedAndAttached,
					Check: resource.ComposeTestCheckFunc(
						resource.TestCheckResourceAttr(resourceName, "agent_id", "agent-1"),
						resource.TestCheckResourceAttr(resourceName, "paused", "true"),
					),
				},
				{
					Config:            pausedAndAttached,
					ResourceName:      resourceName,
					ImportState:       true,
					ImportStateVerify: true,
					ImportStateCheck: func(states []*terraform.InstanceState) error {
						if len(states) != 1 {
							return fmt.Errorf("expected 1 imported state, got %d", len(states))
						}
						for name, want := range map[string]string{"agent_id": "agent-1", "paused": "true"} {
							if got := states[0].Attributes[name]; got != want {
								return fmt.Errorf("imported %s = %q, want %q", name, got, want)
							}
						}
						return nil
					},
				},
			},
		})
	})
}

// CheckDestroy receives the pre-destroy state, so destruction is verified
// against the backend: the resource's id must no longer resolve.
func testAccCheckAPIResourceDestroyed(apiURL, apiToken, apiPath, resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("%s not found in pre-destroy state", resourceName)
		}
		resp, err := testAccAPIGet(apiURL+apiPath+"/"+rs.Primary.ID, apiToken)
		if err != nil {
			return fmt.Errorf("checking %s after destroy: %w", resourceName, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			return fmt.Errorf(
				"%s (id %s) not destroyed: expected GET to return 404, got %d",
				resourceName,
				rs.Primary.ID,
				resp.StatusCode,
			)
		}
		return nil
	}
}

func testAccAPIGet(url, apiToken string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", apiToken)
	return http.DefaultClient.Do(req)
}

func TestIntegrationToModel_UsesTemplateFields(t *testing.T) {
	t.Parallel()

	plannedTags := types.ListValueMust(types.StringType, []attr.Value{types.StringValue("tag-1")})

	model := integrationToModel(
		&client.Integration{
			ID:                "integration-1",
			Name:              "api-name",
			Paused:            false,
			DestinationSchema: "api-schema",
			Source:            client.IntegrationEndpoint{ID: "source-api"},
			Destination:       client.IntegrationEndpoint{ID: "dest-api"},
		},
		integrationModel{
			SourceID:                types.StringValue("source-planned"),
			DestinationID:           types.StringValue("dest-planned"),
			DestinationSchema:       types.StringValue("planned-schema"),
			SourceSettingsJSON:      types.StringValue(`{"key":"value"}`),
			DestinationSettingsJSON: types.StringValue(`{"mode":"append"}`),
			Tags:                    plannedTags,
		},
		preservePlanValue,
	)
	require.Equal(t, "planned-schema", model.DestinationSchema.ValueString())
	require.Equal(t, types.StringValue(`{"key":"value"}`), model.SourceSettingsJSON)
	require.Equal(t, types.StringValue(`{"mode":"append"}`), model.DestinationSettingsJSON)
	require.Equal(t, plannedTags, model.Tags)
	require.Equal(t, "source-planned", model.SourceID.ValueString())
	require.Equal(t, "dest-planned", model.DestinationID.ValueString())
}

// Import supplies no prior state, so every field in the imported state has to
// come from the API response.
func TestIntegrationToModel_ImportHasNoTemplate(t *testing.T) {
	t.Parallel()

	agentID := "agent-api"
	model := integrationToModel(
		&client.Integration{
			ID:                "integration-1",
			Name:              "tf-integration",
			Paused:            true,
			Source:            client.IntegrationEndpoint{ID: "source-api"},
			Destination:       client.IntegrationEndpoint{ID: "dest-api"},
			DestinationSchema: "api-schema",
			OnSchemaUpdate:    "enableColumnChanges",
			AgentID:           &agentID,
		},
		integrationModel{},
		preservePlanValue,
	)

	require.Equal(t, "integration-1", model.ID.ValueString())
	require.Equal(t, "tf-integration", model.Name.ValueString())
	require.Equal(t, "source-api", model.SourceID.ValueString())
	require.Equal(t, "dest-api", model.DestinationID.ValueString())
	require.Equal(t, "api-schema", model.DestinationSchema.ValueString())
	require.Equal(t, "enableColumnChanges", model.OnSchemaUpdate.ValueString())
	require.Equal(t, "agent-api", model.AgentID.ValueString())
	require.True(t, model.Paused.ValueBool())
}

// The API returns neither tags nor a faithful copy of the settings blobs, so an
// imported integration leaves them null rather than inventing a value.
func TestIntegrationToModel_ImportLeavesUnreturnedFieldsNull(t *testing.T) {
	t.Parallel()

	model := integrationToModel(
		&client.Integration{
			ID:          "integration-1",
			Source:      client.IntegrationEndpoint{ID: "source-api"},
			Destination: client.IntegrationEndpoint{ID: "dest-api"},
		},
		integrationModel{},
		preservePlanValue,
	)

	require.True(t, model.Tags.IsNull(), "tags = %v, want null", model.Tags)
	require.True(t, model.SourceSettingsJSON.IsNull(), "source_settings = %v", model.SourceSettingsJSON)
	require.True(t, model.DestinationSettingsJSON.IsNull(), "destination_settings = %v", model.DestinationSettingsJSON)
}

func TestIntegrationToModel_AgentIDFromAPI(t *testing.T) {
	t.Parallel()

	agentID := "agent-api"
	model := integrationToModel(
		&client.Integration{
			ID:      "integration-1",
			AgentID: &agentID,
		},
		integrationModel{
			AgentID: types.StringValue("agent-planned"),
		},
		preservePlanValue,
	)
	require.Equal(t, "agent-api", model.AgentID.ValueString())
}

func TestIntegrationToModel_NullAgentIDFromAPI(t *testing.T) {
	t.Parallel()

	model := integrationToModel(
		&client.Integration{
			ID: "integration-1",
		},
		integrationModel{
			AgentID: types.StringValue("agent-planned"),
		},
		preservePlanValue,
	)
	require.True(t, model.AgentID.IsNull(), "agent_id = %v, want null", model.AgentID)
}

func TestIntegrationToModel_AgentIDUsesGETAsAuthoritative(t *testing.T) {
	t.Parallel()

	t.Run("remote agent replaces planned agent", func(t *testing.T) {
		t.Parallel()

		remoteAgentID := "agent-from-get"
		model := integrationToModel(
			&client.Integration{
				ID:      "integration-1",
				AgentID: &remoteAgentID,
			},
			integrationModel{
				AgentID: types.StringValue("agent-from-config"),
			},
			preservePlanValue,
		)

		require.Equal(t, "agent-from-get", model.AgentID.ValueString())
	})

	t.Run("remote null clears planned agent", func(t *testing.T) {
		t.Parallel()

		model := integrationToModel(
			&client.Integration{
				ID: "integration-1",
			},
			integrationModel{
				AgentID: types.StringValue("agent-from-config"),
			},
			preservePlanValue,
		)

		require.True(t, model.AgentID.IsNull(), "agent_id = %v, want null", model.AgentID)
	})
}

func TestAccIntegration_withAgentID(t *testing.T) {
	var initialID string
	var patchedAgentID string
	var clearedAgentID bool
	apiURL := testAccStartIntegrationsServerWithSeeds(
		t,
		func(_ string, _ client.ModifyIntegrationRequest, rawReq map[string]json.RawMessage) {
			rawAgentID, ok := rawReq["agentId"]
			if !ok {
				return
			}
			if string(rawAgentID) == "null" {
				clearedAgentID = true
				return
			}

			var agentID string
			if err := json.Unmarshal(rawAgentID, &agentID); err == nil {
				patchedAgentID = agentID
			}
		},
		nil,
	)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: testAccCheckAPIResourceDestroyed(
			apiURL,
			apiToken,
			"/integrations",
			"matia_integration.test",
		),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration" "test" {
  source_id          = "source-1"
  destination_id     = "dest-1"
  destination_schema = "raw"
  agent_id           = "agent-123"
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("matia_integration.test", "id"),
					resource.TestCheckResourceAttr("matia_integration.test", "agent_id", "agent-123"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["matia_integration.test"]
						if !ok {
							return errors.New("matia_integration.test not found in state")
						}
						initialID = rs.Primary.ID
						return nil
					},
				),
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration" "test" {
  source_id          = "source-1"
  destination_id     = "dest-1"
  destination_schema = "raw"
  agent_id           = "agent-456"
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_integration.test", "agent_id", "agent-456"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["matia_integration.test"]
						if !ok {
							return errors.New("matia_integration.test not found in state")
						}
						if rs.Primary.ID != initialID {
							return fmt.Errorf("integration was replaced: got %q, want %q", rs.Primary.ID, initialID)
						}
						if patchedAgentID != "agent-456" {
							return fmt.Errorf("PATCH agentId = %q, want %q", patchedAgentID, "agent-456")
						}
						return nil
					},
				),
			},
			{
				Config: testAccProviderConfig(apiURL, apiToken) + `
resource "matia_integration" "test" {
  source_id          = "source-1"
  destination_id     = "dest-1"
  destination_schema = "raw"
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckNoResourceAttr("matia_integration.test", "agent_id"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources["matia_integration.test"]
						if !ok {
							return errors.New("matia_integration.test not found in state")
						}
						if rs.Primary.ID != initialID {
							return fmt.Errorf("integration was replaced: got %q, want %q", rs.Primary.ID, initialID)
						}
						if !clearedAgentID {
							return errors.New("PATCH agentId did not include explicit null")
						}
						return nil
					},
				),
			},
		},
	})
}
