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

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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
		`{"code":"success","data":{"id":%q,"name":"tf-integration","paused":false,"source":{"id":%q,"name":"pg","type":"postgres"},"destination":{"id":%q,"name":"sf","type":"snowflake"},"replicationFrequency":"manual","destinationSchema":%q,"agentId":%s}}`,
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
			ID                   string                     `json:"id"`
			Name                 string                     `json:"name"`
			Paused               bool                       `json:"paused"`
			Source               client.IntegrationEndpoint `json:"source"`
			Destination          client.IntegrationEndpoint `json:"destination"`
			ReplicationFrequency string                     `json:"replicationFrequency"`
			CronExpression       string                     `json:"cronExpression,omitempty"`
			BaseTime             string                     `json:"baseTime,omitempty"`
			DestinationSchema    string                     `json:"destinationSchema,omitempty"`
			AgentID              *string                    `json:"agentId"`
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

type testAccIntegrationPatchHook func(id string, req client.ModifyIntegrationRequest, rawReq map[string]json.RawMessage)

func testAccStartIntegrationsServer(t *testing.T) string {
	return testAccStartIntegrationsServerWithPatchHook(t, nil)
}

func testAccStartIntegrationsServerWithPatchHook(t *testing.T, patchHook testAccIntegrationPatchHook) string {
	t.Helper()

	store := map[string]string{
		"integration-1": testAccIntegrationJSON("integration-1", "source-1", "dest-1", "raw"),
	}
	var mu sync.Mutex
	nextID := 1

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/integrations":
			body, _ := io.ReadAll(r.Body)
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
			store[id] = testAccIntegrationJSON(id, req.SourceID, req.DestinationID, req.DestinationSchema, agentID)
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
			DestinationSettingsJSON: types.StringNull(),
		},
	)
	require.Equal(t, "planned-schema", model.DestinationSchema.ValueString())
	require.Equal(t, types.StringValue(`{"key":"value"}`), model.SourceSettingsJSON)
	require.Equal(t, "source-planned", model.SourceID.ValueString())
	require.Equal(t, "dest-planned", model.DestinationID.ValueString())
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
		)

		require.True(t, model.AgentID.IsNull(), "agent_id = %v, want null", model.AgentID)
	})
}

func TestAccIntegration_withAgentID(t *testing.T) {
	var initialID string
	var patchedAgentID string
	var clearedAgentID bool
	apiURL := testAccStartIntegrationsServerWithPatchHook(
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
