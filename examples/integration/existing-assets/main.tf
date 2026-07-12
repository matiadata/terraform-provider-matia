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

resource "matia_integration" "example" {
  name               = "tf-dev-existing-assets"
  source_id          = var.source_id
  destination_id     = var.destination_id
  destination_schema = var.destination_schema
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

variable "source_id" {
  type        = string
  description = "Existing Matia source asset ID."
}

variable "destination_id" {
  type        = string
  description = "Existing Matia destination asset ID."
}

variable "destination_schema" {
  type    = string
  default = "raw"
}
