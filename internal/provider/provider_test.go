package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"matia": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccImportStateIDFunc reads the import identifier out of the resource's own
// state. matia_integration_schedule and matia_integration_schema have no `id`
// attribute, so the identifier the framework defaults to is the unusable
// "id-attribute-not-set" sentinel; their import steps must pass the id
// explicitly and set ImportStateVerifyIdentifierAttribute, which also
// defaults to `id`.
func testAccImportStateIDFunc(resourceName, attribute string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("%s not found in state", resourceName)
		}
		id := rs.Primary.Attributes[attribute]
		if id == "" {
			return "", fmt.Errorf("%s has no %s in state", resourceName, attribute)
		}
		return id, nil
	}
}

func testAccPreCheck(t *testing.T) {
	t.Helper()
}

func TestProviderProtocolVersion(t *testing.T) {
	testAccPreCheck(t)
	if _, ok := testAccProtoV6ProviderFactories["matia"]; !ok {
		t.Fatal("missing matia provider factory")
	}
}

// TestAccProvider_EnvVarAuth exercises the configuration path the docs
// showcase: an empty provider block with credentials supplied only through
// the MATIA_API_TOKEN / MATIA_API_URL environment variables.
func TestAccProvider_EnvVarAuth(t *testing.T) {
	apiURL := testAccStartHybridDeploymentAgentServer(t)
	apiToken := testAccAPIToken(t)

	t.Setenv("MATIA_API_TOKEN", apiToken)
	t.Setenv("MATIA_API_URL", apiURL)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: testAccCheckAPIResourceDestroyed(
			apiURL,
			apiToken,
			"/agent-gateway/hybrid-deployment-agents",
			"matia_hybrid_deployment_agent.env_auth",
		),
		Steps: []resource.TestStep{
			{
				Config: `
provider "matia" {}

resource "matia_hybrid_deployment_agent" "env_auth" {
  name = "env-auth-agent"
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("matia_hybrid_deployment_agent.env_auth", "name", "env-auth-agent"),
					resource.TestCheckResourceAttrSet("matia_hybrid_deployment_agent.env_auth", "id"),
				),
			},
		},
	})
}
