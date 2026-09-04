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

resource "matia_hybrid_deployment_agent" "example" {
  name        = var.hybrid_agent_name
  description = var.hybrid_agent_description
}

resource "matia_source" "postgres" {
  name = "tf-dev-source-postgres"
  type = "postgres"

  connection_config = jsonencode({
    hostname    = var.postgres_hostname
    port        = var.postgres_port
    database    = var.postgres_database
    ssl         = var.postgres_ssl
    slot        = var.postgres_slot
    publication = var.postgres_publication
  })

  connection_secrets = jsonencode({
    username = var.postgres_username
    password = var.postgres_password
  })
}

resource "matia_destination" "snowflake" {
  name = "tf-dev-destination-snowflake"
  type = "snowflake"

  connection_config = jsonencode({
    account   = var.snowflake_account
    database  = var.snowflake_database
    warehouse = var.snowflake_warehouse
    role      = var.snowflake_role
    username  = var.snowflake_username
  })

  connection_secrets = jsonencode({
    password = var.snowflake_password
  })
}

resource "matia_integration" "postgres_to_snowflake" {
  name               = "tf-dev-postgres-to-snowflake-hybrid"
  source_id          = matia_source.postgres.id
  destination_id     = matia_destination.snowflake.id
  destination_schema = var.destination_schema
  agent_id           = matia_hybrid_deployment_agent.example.id

  source_settings = jsonencode({
    incremental_mode = "Change Stream"
    max_clients      = 4
  })
}

resource "matia_integration_schedule" "postgres_to_snowflake" {
  integration_id        = matia_integration.postgres_to_snowflake.id
  replication_frequency = "manual"
}

resource "matia_integration_schema" "postgres_to_snowflake" {
  integration_id = matia_integration.postgres_to_snowflake.id
  config = jsonencode({
    schemas = {
      public = {
        tables = {
          users = {
            enabled  = true
            syncMode = "change_stream"
          }
        }
      }
    }
  })
}

output "integration_id" {
  description = "Matia integration ID (for manual runs and API validation)"
  value       = matia_integration.postgres_to_snowflake.id
}

output "source_id" {
  description = "Postgres source asset ID"
  value       = matia_source.postgres.id
}

output "destination_id" {
  description = "Snowflake destination asset ID"
  value       = matia_destination.snowflake.id
}

output "hybrid_agent_id" {
  description = "Hybrid deployment agent ID created by Terraform"
  value       = matia_hybrid_deployment_agent.example.id
}

output "hybrid_agent_token" {
  description = "One-time token to start the hybrid agent process"
  value       = matia_hybrid_deployment_agent.example.token
  sensitive   = true
}

variable "matia_api_token" {
  type      = string
  sensitive = true
}

variable "matia_api_url" {
  type    = string
  default = "https://api.matia.io/v1"
}

variable "hybrid_agent_name" {
  type    = string
  default = "tf-dev-postgres-snowflake-hybrid-agent"
}

variable "hybrid_agent_description" {
  type    = string
  default = "Hybrid agent for Postgres to Snowflake Terraform e2e"
}

variable "postgres_hostname" {
  type = string
}

variable "postgres_port" {
  type = string
}

variable "postgres_database" {
  type = string
}

variable "postgres_ssl" {
  type    = bool
  default = false
}

variable "postgres_slot" {
  type    = string
  default = "matia_slot"
}

variable "postgres_publication" {
  type    = string
  default = "matia_pub"
}

variable "postgres_username" {
  type = string
}

variable "postgres_password" {
  type      = string
  sensitive = true
}

variable "snowflake_account" {
  type = string
}

variable "snowflake_database" {
  type = string
}

variable "snowflake_warehouse" {
  type = string
}

variable "snowflake_role" {
  type = string
}

variable "snowflake_username" {
  type = string
}

variable "snowflake_password" {
  type      = string
  sensitive = true
}

variable "destination_schema" {
  type        = string
  description = "Snowflake schema for synced tables"
}
