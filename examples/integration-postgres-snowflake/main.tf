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

resource "matia_source" "postgres" {
  name        = "tf-dev-source-postgres"
  type        = "postgres"
  auth_method = "direct"

  connection_config = jsonencode({
    hostname        = var.postgres_hostname
    port            = var.postgres_port
    database        = var.postgres_database
    ssl             = var.postgres_ssl
    replicationSlot = var.postgres_slot
    publication     = var.postgres_publication
  })

  connection_secrets = jsonencode({
    username = var.postgres_username
    password = var.postgres_password
  })
}

resource "matia_asset" "snowflake" {
  name        = "tf-dev-snowflake"
  type        = "snowflake"
  auth_method = "keyPair"

  # One user for every purpose.
  credentials = {
    account                = var.snowflake_account
    username               = var.snowflake_username
    database               = var.snowflake_database
    warehouse              = var.snowflake_warehouse
    private_key            = var.snowflake_private_key
    private_key_passphrase = var.snowflake_private_key_passphrase
  }
}

resource "matia_integration" "postgres_to_snowflake" {
  name               = "tf-dev-postgres-to-snowflake"
  source_id          = matia_source.postgres.id
  destination_id     = matia_asset.snowflake.id
  destination_schema = var.destination_schema

  source_settings = jsonencode({
    incrementalMode = "cdc"
    maxClients      = 4
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
  description = "Snowflake asset ID the integration loads into"
  value       = matia_asset.snowflake.id
}

variable "matia_api_token" {
  type      = string
  sensitive = true
}

variable "matia_api_url" {
  type    = string
  default = "https://api.matia.io/v1"
}

variable "postgres_hostname" {
  type = string
}

variable "postgres_port" {
  type    = number
  default = 5432
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

variable "snowflake_username" {
  type = string
}

variable "snowflake_database" {
  type = string
}

variable "snowflake_warehouse" {
  type = string
}

variable "snowflake_private_key" {
  type        = string
  sensitive   = true
  description = "PKCS#8 PEM private key, newlines included."
}

variable "snowflake_private_key_passphrase" {
  type        = string
  sensitive   = true
  default     = null
  description = "Passphrase of the private key, when it is encrypted."
}

variable "destination_schema" {
  type        = string
  description = "Snowflake schema for synced tables"
}
