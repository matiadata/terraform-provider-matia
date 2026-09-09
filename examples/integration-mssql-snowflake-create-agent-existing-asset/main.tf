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

resource "matia_source" "mssql" {
  name = "tf-dev-source-mssql"
  type = "mssql"

  connection_config = jsonencode({
    hostname = var.mssql_hostname
    port     = var.mssql_port
    database = var.mssql_database
    tls      = var.mssql_tls
  })

  connection_secrets = jsonencode({
    username = var.mssql_username
    password = var.mssql_password
  })
}

resource "matia_integration" "mssql_to_snowflake" {
  name               = "tf-dev-mssql-to-snowflake-hybrid"
  source_id          = matia_source.mssql.id
  destination_id     = var.destination_id
  destination_schema = var.destination_schema
  agent_id           = matia_hybrid_deployment_agent.example.id

  # Effectively create-only: source_settings has no RequiresReplace, so editing the mode
  # plans as a clean in-place update and only then fails - the API rejects a sourceSettings
  # update that does not carry customReports. Recreate the integration to change it.
  source_settings = jsonencode({
    mssql_incremental_mode = var.mssql_incremental_mode
  })
}

resource "matia_integration_schedule" "mssql_to_snowflake" {
  integration_id        = matia_integration.mssql_to_snowflake.id
  replication_frequency = "manual"
}

resource "matia_integration_schema" "mssql_to_snowflake" {
  integration_id = matia_integration.mssql_to_snowflake.id
  config = jsonencode({
    schemas = {
      dbo = {
        tables = {
          customers = {
            # The catalog's supportedSyncModes derive from mssql_incremental_mode, and the
            # schemas API rejects a syncMode outside that set, so the two must agree.
            enabled  = true
            syncMode = var.mssql_incremental_mode
          }
        }
      }
    }
  })
}

output "integration_id" {
  description = "Matia integration ID (for manual runs and API validation)"
  value       = matia_integration.mssql_to_snowflake.id
}

output "source_id" {
  description = "SQL Server source asset ID"
  value       = matia_source.mssql.id
}

output "destination_id" {
  description = "Snowflake asset ID the integration loads into"
  value       = var.destination_id
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
  default = "tf-dev-mssql-snowflake-hybrid-agent"
}

variable "hybrid_agent_description" {
  type    = string
  default = "Hybrid agent for SQL Server to Snowflake Terraform e2e"
}

variable "mssql_hostname" {
  type = string
}

variable "mssql_port" {
  type = string
}

variable "mssql_database" {
  type = string
}

variable "mssql_tls" {
  type        = bool
  default     = true
  description = "Encrypt the connection to SQL Server."
}

variable "mssql_username" {
  type = string
}

variable "mssql_password" {
  type      = string
  sensitive = true
}

variable "mssql_incremental_mode" {
  type        = string
  default     = "Change Stream"
  description = <<-EOT
    How Matia reads changes from SQL Server: "Change Stream" (CDC) or
    "Change Tracking". Change Stream requires CDC enabled on the database and
    SQL Server Agent running; Change Tracking works on every edition without
    the Agent. The source setting also accepts "Incremental", which this
    example omits because it needs a cursor-eligible column on every enabled
    table and so cannot apply against the customers table below.
  EOT

  validation {
    condition     = contains(["Change Stream", "Change Tracking"], var.mssql_incremental_mode)
    error_message = "mssql_incremental_mode must be one of: Change Stream, Change Tracking."
  }
}

variable "destination_id" {
  type        = string
  description = "ID of an existing Snowflake asset in Matia."
}

variable "destination_schema" {
  type        = string
  description = "Snowflake schema for synced tables"
}
