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

resource "matia_source" "mysql" {
  name = "tf-dev-source-mysql"
  type = "mysql"

  connection_config = jsonencode({
    hostname = var.mysql_hostname
    port     = var.mysql_port
    database = var.mysql_database
  })

  connection_secrets = jsonencode({
    username = var.mysql_username
    password = var.mysql_password
  })
}

resource "matia_destination" "bigquery" {
  name        = "tf-dev-destination-bigquery"
  type        = "bigquery"
  auth_method = "customServiceAccount"

  connection_config = jsonencode({
    project_id          = var.bigquery_project_id
    location            = var.bigquery_location
    isServiceConnection = false
  })

  connection_secrets = jsonencode({
    private_key  = var.bigquery_private_key
    client_email = var.bigquery_client_email
  })
}

resource "matia_integration" "mysql_to_bq" {
  name               = "tf-dev-mysql-to-bigquery-hybrid"
  source_id          = matia_source.mysql.id
  destination_id     = matia_destination.bigquery.id
  destination_schema = var.destination_schema
  agent_id           = matia_hybrid_deployment_agent.example.id

  source_settings = jsonencode({
    incremental_mode = "Change Stream"
    max_clients      = 4
  })
}

resource "matia_integration_schedule" "mysql_to_bq" {
  integration_id        = matia_integration.mysql_to_bq.id
  replication_frequency = "manual"
}

resource "matia_integration_schema" "mysql_to_bq" {
  integration_id = matia_integration.mysql_to_bq.id
  config = jsonencode({
    schemas = {
      schema = {
        tables = {
          users = {
            enabled  = true
            syncMode = "incremental"
          }
        }
      }
    }
  })
}

output "integration_id" {
  description = "Matia integration ID (for manual runs and API validation)"
  value       = matia_integration.mysql_to_bq.id
}

output "source_id" {
  description = "MySQL source asset ID"
  value       = matia_source.mysql.id
}

output "destination_id" {
  description = "BigQuery destination asset ID"
  value       = matia_destination.bigquery.id
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
  default = "tf-dev-hybrid-agent"
}

variable "hybrid_agent_description" {
  type    = string
  default = "Hybrid agent for MySQL to BigQuery Terraform e2e"
}

variable "mysql_hostname" {
  type = string
}

variable "mysql_port" {
  type = string
}

variable "mysql_database" {
  type = string
}

variable "mysql_username" {
  type = string
}

variable "mysql_password" {
  type      = string
  sensitive = true
}

variable "bigquery_project_id" {
  type = string
}

variable "bigquery_location" {
  type    = string
  default = "us"
}

variable "bigquery_private_key" {
  type      = string
  sensitive = true
}

variable "bigquery_client_email" {
  type = string
}

variable "destination_schema" {
  type        = string
  description = "BigQuery dataset for synced tables"
}
