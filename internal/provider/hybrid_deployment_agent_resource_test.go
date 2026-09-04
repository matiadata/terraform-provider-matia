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

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

func TestAccHybridDeploymentAgent_basic(t *testing.T) {
	apiURL := testAccStartHybridDeploymentAgentServer(t)
	apiToken := testAccAPIToken(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: testAccCheckAPIResourceDestroyed(
			apiURL,
			apiToken,
			"/agent-gateway/hybrid-deployment-agents",
			"matia_hybrid_deployment_agent.test",
		),
		Steps: []resource.TestStep{
			{
				Config: testAccHybridDeploymentAgentConfig(t, apiURL, "my-agent", "edge agent"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_hybrid_deployment_agent.test", "name", "my-agent"),
					resource.TestCheckResourceAttr("matia_hybrid_deployment_agent.test", "description", "edge agent"),
					resource.TestCheckResourceAttrSet("matia_hybrid_deployment_agent.test", "id"),
					resource.TestCheckResourceAttrSet("matia_hybrid_deployment_agent.test", "created_at"),
					resource.TestCheckResourceAttrSet("matia_hybrid_deployment_agent.test", "token"),
				),
			},
		},
	})
}

// Import supplies no prior state and the API never returns the one-time token,
// so the token is verified as null here rather than round-tripped. The agent
// without a description covers the field the API omits entirely.
func TestAccHybridDeploymentAgent_import(t *testing.T) {
	const resourceName = "matia_hybrid_deployment_agent.test"

	cases := []struct {
		name        string
		description string
	}{
		{name: "with description", description: "edge agent"},
		{name: "without description", description: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiURL := testAccStartHybridDeploymentAgentServer(t)
			apiToken := testAccAPIToken(t)
			config := testAccHybridDeploymentAgentConfig(t, apiURL, "my-agent", tc.description)

			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				CheckDestroy: testAccCheckAPIResourceDestroyed(
					apiURL,
					apiToken,
					"/agent-gateway/hybrid-deployment-agents",
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
						ImportStateVerifyIgnore: []string{"token"},
						ImportStateCheck: func(states []*terraform.InstanceState) error {
							if len(states) != 1 {
								return fmt.Errorf("expected 1 imported state, got %d", len(states))
							}
							if token, ok := states[0].Attributes["token"]; ok {
								return fmt.Errorf("imported state has token %q, want none", token)
							}
							return nil
						},
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
	}
}

func TestAccHybridDeploymentAgent_rejectsEmptyDescription(t *testing.T) {
	apiURL := testAccStartHybridDeploymentAgentServer(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "matia" {
  api_token = %q
  api_url   = %q
}

resource "matia_hybrid_deployment_agent" "test" {
  name        = "my-agent"
  description = ""
}
`, testAccAPIToken(t), apiURL),
				ExpectError: regexp.MustCompile(`Attribute description string length must be at least 1`),
			},
		},
	})
}

func TestHybridDeploymentAgentToModel_ImportHasNoTemplate(t *testing.T) {
	t.Parallel()

	model := hybridDeploymentAgentToModel(
		&client.HybridDeploymentAgent{
			ID:          "agent-1",
			Name:        "my-agent",
			Description: "edge agent",
			CreatedAt:   "2026-06-12T10:00:00Z",
		},
		hybridDeploymentAgentModel{},
	)

	require.Equal(t, "agent-1", model.ID.ValueString())
	require.Equal(t, "my-agent", model.Name.ValueString())
	require.Equal(t, "edge agent", model.Description.ValueString())
	require.Equal(t, "2026-06-12T10:00:00Z", model.CreatedAt.ValueString())
	require.True(t, model.Token.IsNull())
}

func TestHybridDeploymentAgentToModel_ImportWithoutDescription(t *testing.T) {
	t.Parallel()

	model := hybridDeploymentAgentToModel(
		&client.HybridDeploymentAgent{ID: "agent-1", Name: "my-agent"},
		hybridDeploymentAgentModel{},
	)

	require.Equal(t, "my-agent", model.Name.ValueString())
	require.True(t, model.Description.IsNull())
}

func TestHybridDeploymentAgentToModel_PrefersPlannedFieldsOverAPI(t *testing.T) {
	t.Parallel()

	model := hybridDeploymentAgentToModel(
		&client.HybridDeploymentAgent{ID: "agent-1", Name: "renamed", Description: "renamed description"},
		hybridDeploymentAgentModel{
			Name:        types.StringValue("my-agent"),
			Description: types.StringValue("edge agent"),
		},
	)

	require.Equal(t, "my-agent", model.Name.ValueString())
	require.Equal(t, "edge agent", model.Description.ValueString())
}

func TestHybridDeploymentAgentToModel_KeepsEmptyDescriptionFromLegacyState(t *testing.T) {
	t.Parallel()

	model := hybridDeploymentAgentToModel(
		&client.HybridDeploymentAgent{ID: "agent-1", Name: "my-agent"},
		hybridDeploymentAgentModel{Description: types.StringValue("")},
	)

	require.False(t, model.Description.IsNull())
	require.Empty(t, model.Description.ValueString())
}

func testAccHybridDeploymentAgentConfig(t *testing.T, apiURL, name, description string) string {
	t.Helper()
	descriptionAttr := ""
	if description != "" {
		descriptionAttr = fmt.Sprintf("\n  description = %q", description)
	}

	return fmt.Sprintf(`
provider "matia" {
  api_token = %q
  api_url   = %q
}

resource "matia_hybrid_deployment_agent" "test" {
  name = %q%s
}
`, testAccAPIToken(t), apiURL, name, descriptionAttr)
}

type hybridDeploymentAgentStore struct {
	mu     sync.Mutex
	nextID int
	agents map[string]hybridDeploymentAgentRecord
}

type hybridDeploymentAgentRecord struct {
	Name        string
	Description string
	CreatedAt   string
}

func testAccStartHybridDeploymentAgentServer(t *testing.T) string {
	t.Helper()

	store := &hybridDeploymentAgentStore{
		agents: make(map[string]hybridDeploymentAgentRecord),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agent-gateway/hybrid-deployment-agents":
			var body struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create body: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}

			store.mu.Lock()
			store.nextID++
			id := fmt.Sprintf("agent-%d", store.nextID)
			store.agents[id] = hybridDeploymentAgentRecord{
				Name:        body.Name,
				Description: body.Description,
				CreatedAt:   "2026-06-12T10:00:00Z",
			}
			store.mu.Unlock()

			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, fmt.Sprintf(`{"code":"success","data":{"id":%q,"token":"secret-token-abc"}}`, id))

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/agent-gateway/hybrid-deployment-agents/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/agent-gateway/hybrid-deployment-agents/")

			store.mu.Lock()
			agent, ok := store.agents[id]
			store.mu.Unlock()

			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"message":"not found"}`)
				return
			}

			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, fmt.Sprintf(
				`{"code":"success","data":{"id":%q,"name":%q,"description":%q,"createdAt":%q}}`,
				id, agent.Name, agent.Description, agent.CreatedAt,
			))

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/agent-gateway/hybrid-deployment-agents/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/agent-gateway/hybrid-deployment-agents/")

			store.mu.Lock()
			_, ok := store.agents[id]
			if ok {
				delete(store.agents, id)
			}
			store.mu.Unlock()

			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"message":"not found"}`)
				return
			}

			w.WriteHeader(http.StatusOK)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	return server.URL + "/v1"
}
