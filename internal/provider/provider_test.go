package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"matia": providerserver.NewProtocol6WithError(New("test")()),
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
