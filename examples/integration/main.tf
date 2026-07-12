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
  name = "tf-dev-source-postgres"
  type = "postgres"

  # The schema config below syncs public.users — create it in your source
  # database first, or point the config at an existing table.
  connection_config = jsonencode({
    hostname = "host.docker.internal"
    port     = "5432"
    database = "postgres"
    ssl      = false
  })

  connection_secrets = jsonencode({
    username = "postgres"
    password = "postgres"
  })
}

resource "matia_destination" "snowflake" {
  name = "tf-dev-destination-snowflake"
  type = "snowflake"

  connection_config = jsonencode({
    account   = var.snowflake_account
    database  = "STANDARD_DATABASE"
    warehouse = "STANDARD_WAREHOUSE"
    role      = "STANDARD_ROLE"
    username  = var.snowflake_username
  })

  connection_secrets = jsonencode({
    password = var.snowflake_password
  })
}

resource "matia_integration" "example" {
  name               = "tf-dev-postgres-to-snowflake"
  source_id          = matia_source.postgres.id
  destination_id     = matia_destination.snowflake.id
  destination_schema = "raw"
  source_settings = jsonencode({
    incremental_mode = "Change Stream"
    max_clients      = 4
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

variable "snowflake_password" {
  type      = string
  sensitive = true
}
