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

  # The schema config below syncs public.users — create it in your source
  # database first, or point the config at an existing table.
  connection_config = jsonencode({
    hostname        = "host.docker.internal"
    port            = 5432
    database        = "postgres"
    ssl             = false
    replicationSlot = "matia_slot"
    publication     = "matia_pub"
  })

  connection_secrets = jsonencode({
    username = "postgres"
    password = "postgres"
  })
}

resource "matia_asset" "snowflake" {
  name        = "tf-dev-snowflake"
  type        = "snowflake"
  auth_method = "keyPair"

  credentials = {
    account                = var.snowflake_account
    username               = var.snowflake_username
    database               = "STANDARD_DATABASE"
    warehouse              = "STANDARD_WAREHOUSE"
    private_key            = var.snowflake_private_key
    private_key_passphrase = var.snowflake_private_key_passphrase
  }
}

resource "matia_integration" "example" {
  name               = "tf-dev-postgres-to-snowflake"
  source_id          = matia_source.postgres.id
  destination_id     = matia_asset.snowflake.id
  destination_schema = "raw"
  source_settings = jsonencode({
    incrementalMode = "cdc"
    maxClients      = 4
  })
}

resource "matia_integration_schedule" "example" {
  integration_id        = matia_integration.example.id
  replication_frequency = "manual"
}

resource "matia_integration_schema" "example" {
  integration_id = matia_integration.example.id
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

variable "matia_api_token" {
  type      = string
  sensitive = true
}

variable "matia_api_url" {
  type    = string
  default = "https://api.matia.io/v1"
}

variable "snowflake_account" {
  type = string
}

variable "snowflake_username" {
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
