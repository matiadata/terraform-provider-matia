terraform {
  required_providers {
    matia = {
      source = "matiadata/matia"
    }
  }
}

provider "matia" {
  api_token = var.matia_api_token
  api_url   = var.matia_api_url
}
# Hybrid deployment agents are served at /v1/agent-gateway/hybrid-deployment-agents (provider adds the prefix).
# Local dev: backend on :3001 proxies to agent-gateway (:3048); run `nx serve agent-gateway`.
# A 504 "Error occurred while trying to proxy" usually means agent-gateway is down.
resource "matia_hybrid_deployment_agent" "example" {
  name        = "dev-getting-started-agent"
  description = "Created while following CONTRIBUTING.md"
}

variable "matia_api_token" {
  type      = string
  sensitive = true
}

variable "matia_api_url" {
  type    = string
  default = "https://api.matia.io/v1"
}
