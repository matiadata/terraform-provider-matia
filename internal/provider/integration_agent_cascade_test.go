package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

// The shared integrations server reports a fixed source agent, so it cannot
// reproduce a cascade. This one models the backend rules the provider relies on
// (packages/backend/src/app/integrations/source-agent-alignment.ts): a bound
// source's agent is pushed onto its integrations on every source update, an
// omitted integration agent inherits it, and an integration agent that
// disagrees with a bound source is rejected with SOURCE_AGENT_MISMATCH.
type testAccCascadeServer struct {
	mu              sync.Mutex
	sourceAgents    map[string]*string
	integrations    map[string]*testAccCascadeIntegration
	nextAsset       int
	nextIntegration int
}

type testAccCascadeIntegration struct {
	sourceID      string
	destinationID string
	schema        string
	agentID       *string
}

func testAccStartCascadeServer(t *testing.T) string {
	t.Helper()

	cascade := &testAccCascadeServer{
		sourceAgents: map[string]*string{},
		integrations: map[string]*testAccCascadeIntegration{},
	}
	server := httptest.NewServer(http.HandlerFunc(cascade.handle))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

// testAccWrittenAgentID separates an absent agentId from an explicit null,
// which the API reads as a detach.
func testAccWrittenAgentID(body map[string]json.RawMessage) (*string, bool) {
	encoded, ok := body["agentId"]
	if !ok {
		return nil, false
	}
	if string(encoded) == "null" {
		return nil, true
	}
	var value string
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, false
	}
	return &value, true
}

// resolveAgent mirrors the backend's resolveSourceAgent: a bound source wins,
// and an integration that names anything else - a detach included - is refused.
func (s *testAccCascadeServer) resolveAgent(
	sourceID string,
	written *string,
	present bool,
	existing *string,
) (*string, error) {
	sourceAgent := s.sourceAgents[sourceID]
	if !present {
		if sourceAgent != nil {
			return sourceAgent, nil
		}
		return existing, nil
	}
	if sourceAgent == nil {
		return written, nil
	}
	if written == nil || *written != *sourceAgent {
		requested := "(none)"
		if written != nil {
			requested = *written
		}
		return nil, fmt.Errorf(
			"integration agent %s does not match source agent %s",
			requested,
			*sourceAgent,
		)
	}
	return written, nil
}

func (s *testAccCascadeServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	body, _ := io.ReadAll(r.Body)
	var raw map[string]json.RawMessage
	if len(body) > 0 {
		if err := json.Unmarshal(body, &raw); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/assets":
		s.nextAsset++
		id := fmt.Sprintf("asset-%d", s.nextAsset)
		s.sourceAgents[id] = testAccConfiguredAgentID(raw)
		testAccWriteCreated(w, id)

	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/assets/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/assets/")
		agent, ok := s.sourceAgents[id]
		if !ok {
			testAccWriteNotFound(w, "asset not found")
			return
		}
		configuration := &client.AssetConfigurationRequest{}
		if agent != nil {
			configuration.AgentID = *agent
		}
		testAccWriteJSON(w, testAccAssetAgentJSON(
			testAccAssetJSON(id, "source", "postgres", "source"),
			configuration,
		))

	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/assets/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/assets/")
		if _, ok := s.sourceAgents[id]; !ok {
			testAccWriteNotFound(w, "asset not found")
			return
		}
		if _, present := raw["configuration"]; present {
			s.cascadeSourceAgent(id, testAccConfiguredAgentID(raw))
		}
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodPost && r.URL.Path == "/v1/integrations":
		var req client.CreateIntegrationRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		written, present := testAccWrittenAgentID(raw)
		agent, err := s.resolveAgent(req.SourceID, written, present, nil)
		if err != nil {
			testAccWriteAgentMismatch(w, err)
			return
		}
		s.nextIntegration++
		id := fmt.Sprintf("integration-%d", s.nextIntegration)
		s.integrations[id] = &testAccCascadeIntegration{
			sourceID:      req.SourceID,
			destinationID: req.DestinationID,
			schema:        req.DestinationSchema,
			agentID:       agent,
		}
		testAccWriteCreated(w, id)

	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/integrations/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/integrations/")
		integration, ok := s.integrations[id]
		if !ok || strings.Contains(id, "/") {
			testAccWriteNotFound(w, "integration not found")
			return
		}
		testAccWriteJSON(w, testAccIntegrationJSON(
			id,
			integration.sourceID,
			integration.destinationID,
			integration.schema,
			integration.agentID,
		))

	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/integrations/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/integrations/")
		integration, ok := s.integrations[id]
		if !ok {
			testAccWriteNotFound(w, "integration not found")
			return
		}
		written, present := testAccWrittenAgentID(raw)
		agent, err := s.resolveAgent(integration.sourceID, written, present, integration.agentID)
		if err != nil {
			testAccWriteAgentMismatch(w, err)
			return
		}
		integration.agentID = agent
		var req client.ModifyIntegrationRequest
		if unmarshalErr := json.Unmarshal(body, &req); unmarshalErr == nil &&
			req.DestinationSchema != "" {
			integration.schema = req.DestinationSchema
		}
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/integrations/"):
		delete(s.integrations, strings.TrimPrefix(r.URL.Path, "/v1/integrations/"))
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/assets/"):
		delete(s.sourceAgents, strings.TrimPrefix(r.URL.Path, "/v1/assets/"))
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// Assigning or changing a source's agent updates every integration on it;
// unbinding the source leaves their existing assignments in place.
func (s *testAccCascadeServer) cascadeSourceAgent(sourceID string, agent *string) {
	s.sourceAgents[sourceID] = agent
	if agent == nil {
		return
	}
	for _, integration := range s.integrations {
		if integration.sourceID == sourceID {
			integration.agentID = agent
		}
	}
}

func testAccConfiguredAgentID(raw map[string]json.RawMessage) *string {
	encoded, ok := raw["configuration"]
	if !ok {
		return nil
	}
	var configuration map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &configuration); err != nil {
		return nil
	}
	agent, _ := testAccWrittenAgentID(configuration)
	return agent
}

func testAccWriteJSON(w http.ResponseWriter, payload string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(payload))
}

func testAccWriteCreated(w http.ResponseWriter, id string) {
	testAccWriteJSON(w, fmt.Sprintf(`{"code":"success","data":{"id":%q}}`, id))
}

func testAccWriteNotFound(w http.ResponseWriter, message string) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprintf(w, `{"code":"NotFound","message":%q}`, message)
}

func testAccWriteAgentMismatch(w http.ResponseWriter, err error) {
	w.WriteHeader(http.StatusBadRequest)
	_, _ = fmt.Fprintf(
		w,
		`{"code":"SOURCE_AGENT_MISMATCH","message":%q}`,
		err.Error(),
	)
}

func testAccAgent(value string) *string {
	return &value
}

// destination_schema stands in for any ordinary integration edit: it puts the
// integration in the plan alongside the source change.
func testAccCascadeConfig(
	apiURL, apiToken string,
	sourceAgent, integrationAgent *string,
	schema string,
) string {
	sourceAssignment := ""
	if sourceAgent != nil {
		sourceAssignment = fmt.Sprintf("agent_id = %q", *sourceAgent)
	}
	integrationAssignment := ""
	if integrationAgent != nil {
		integrationAssignment = fmt.Sprintf("agent_id = %q", *integrationAgent)
	}

	return testAccProviderConfig(apiURL, apiToken) + fmt.Sprintf(`
resource "matia_source" "pg" {
  name              = "source"
  type              = "postgres"
  connection_config = jsonencode({hostname = "localhost"})
  %s
}

resource "matia_integration" "test" {
  name               = "tf-integration"
  source_id          = matia_source.pg.id
  destination_id     = "dest-1"
  destination_schema = %q
  %s
}
`, sourceAssignment, schema, integrationAssignment)
}

// Reassigning a bound source's agent while the integration is also being
// edited used to abort with "Provider produced inconsistent final plan": the
// plan recorded the source's old agent as a known value, and the re-plan that
// runs after the source updates read the new one.
func TestAccIntegration_sourceAgentReassignmentWithIntegrationUpdate(t *testing.T) {
	apiURL := testAccStartCascadeServer(t)
	apiToken := testAccAPIToken(t)
	const resourceName = "matia_integration.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCascadeConfig(apiURL, apiToken, testAccAgent("agent-a"), nil, "raw"),
				Check: resource.TestCheckResourceAttr(
					resourceName, "agent_id", "agent-a",
				),
			},
			{
				Config:   testAccCascadeConfig(apiURL, apiToken, testAccAgent("agent-a"), nil, "raw"),
				PlanOnly: true,
			},
			{
				Config: testAccCascadeConfig(apiURL, apiToken, testAccAgent("agent-b"), nil, "raw_v2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectUnknownValue(resourceName, tfjsonpath.New("agent_id")),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "agent_id", "agent-b"),
					resource.TestCheckResourceAttr(resourceName, "destination_schema", "raw_v2"),
				),
			},
			{
				Config:   testAccCascadeConfig(apiURL, apiToken, testAccAgent("agent-b"), nil, "raw_v2"),
				PlanOnly: true,
			},
		},
	})
}

// Unbinding the source and detaching the integration in one apply used to be
// rejected at plan time against the source's stale binding, with a diagnostic
// asking for the change the configuration already made.
func TestAccIntegration_detachWhileUnbindingSourceInOneApply(t *testing.T) {
	apiURL := testAccStartCascadeServer(t)
	apiToken := testAccAPIToken(t)
	const resourceName = "matia_integration.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCascadeConfig(apiURL, apiToken, testAccAgent("agent-a"), nil, "raw"),
				Check: resource.TestCheckResourceAttr(
					resourceName, "agent_id", "agent-a",
				),
			},
			{
				Config: testAccCascadeConfig(apiURL, apiToken, nil, testAccAgent(""), "raw"),
				Check: resource.TestCheckResourceAttr(
					resourceName, "agent_id", "",
				),
			},
			{
				Config:   testAccCascadeConfig(apiURL, apiToken, nil, testAccAgent(""), "raw"),
				PlanOnly: true,
			},
		},
	})
}

// The source still takes precedence; the API enforces it now that planning no
// longer does.
func TestAccIntegration_detachRejectedWhileSourceStaysBound(t *testing.T) {
	apiURL := testAccStartCascadeServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCascadeConfig(apiURL, apiToken, testAccAgent("agent-a"), nil, "raw"),
				Check: resource.TestCheckResourceAttr(
					"matia_integration.test", "agent_id", "agent-a",
				),
			},
			{
				Config: testAccCascadeConfig(
					apiURL, apiToken, testAccAgent("agent-a"), testAccAgent(""), "raw",
				),
				ExpectError: regexp.MustCompile("SOURCE_AGENT_MISMATCH"),
			},
		},
	})
}

// An unbound source owns nothing, so an omitted agent_id must keep the
// integration's own agent rather than planning null and detaching it - a source
// Matia Cloud can reach loading into a destination only the agent can reach.
func TestAccIntegration_unboundSourceKeepsOwnAgent(t *testing.T) {
	apiURL := testAccStartCascadeServer(t)
	apiToken := testAccAPIToken(t)
	const resourceName = "matia_integration.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCascadeConfig(apiURL, apiToken, nil, testAccAgent("agent-own"), "raw"),
				Check: resource.TestCheckResourceAttr(
					resourceName, "agent_id", "agent-own",
				),
			},
			{
				Config: testAccCascadeConfig(apiURL, apiToken, nil, nil, "raw_v2"),
				Check: resource.TestCheckResourceAttr(
					resourceName, "agent_id", "agent-own",
				),
			},
			{
				Config:   testAccCascadeConfig(apiURL, apiToken, nil, nil, "raw_v2"),
				PlanOnly: true,
			},
		},
	})
}

// Creating with the detach sentinel sends an explicit null, so a bound source
// refuses it. Dropping the sentinel instead let the backend cascade its agent
// onto an integration whose configuration asked for none.
func TestAccIntegration_createDetachedUnderBoundSourceRejected(t *testing.T) {
	apiURL := testAccStartCascadeServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCascadeConfig(
					apiURL, apiToken, testAccAgent("agent-a"), testAccAgent(""), "raw",
				),
				ExpectError: regexp.MustCompile("SOURCE_AGENT_MISMATCH"),
			},
		},
	})
}

func TestAccIntegration_createDetachedUnderUnboundSource(t *testing.T) {
	apiURL := testAccStartCascadeServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCascadeConfig(apiURL, apiToken, nil, testAccAgent(""), "raw"),
				Check: resource.TestCheckResourceAttr(
					"matia_integration.test", "agent_id", "",
				),
			},
			{
				Config:   testAccCascadeConfig(apiURL, apiToken, nil, testAccAgent(""), "raw"),
				PlanOnly: true,
			},
		},
	})
}

// A source agent change with no other edit leaves the integration a no-op in
// the plan, so the cascade is invisible to that apply. The next refresh picks
// it up with an empty diff, which is the ordinary contract for a value the
// server computes.
func TestAccIntegration_sourceAgentChangeAloneSelfHealsOnRefresh(t *testing.T) {
	apiURL := testAccStartCascadeServer(t)
	apiToken := testAccAPIToken(t)
	const resourceName = "matia_integration.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCascadeConfig(apiURL, apiToken, testAccAgent("agent-a"), nil, "raw"),
				Check: resource.TestCheckResourceAttr(
					resourceName, "agent_id", "agent-a",
				),
			},
			{
				// The integration is not in this apply, so the cascade is not
				// yet in its state.
				Config: testAccCascadeConfig(apiURL, apiToken, testAccAgent("agent-b"), nil, "raw"),
				Check: resource.TestCheckResourceAttr(
					resourceName, "agent_id", "agent-a",
				),
			},
			{
				// The next run refreshes and adopts the cascaded agent.
				Config: testAccCascadeConfig(apiURL, apiToken, testAccAgent("agent-b"), nil, "raw"),
				Check: resource.TestCheckResourceAttr(
					resourceName, "agent_id", "agent-b",
				),
			},
			{
				Config:   testAccCascadeConfig(apiURL, apiToken, testAccAgent("agent-b"), nil, "raw"),
				PlanOnly: true,
			},
		},
	})
}
