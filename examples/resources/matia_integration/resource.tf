resource "matia_integration" "example" {
  name               = "example-postgres-to-snowflake"
  source_id          = var.source_id
  destination_id     = var.destination_id
  destination_schema = "raw"
  source_settings = jsonencode({
    incrementalMode = "cdc"
    maxClients      = 4
  })
}

variable "source_id" {
  type = string
}

variable "destination_id" {
  type = string
}
