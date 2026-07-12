package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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
