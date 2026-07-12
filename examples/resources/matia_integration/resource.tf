resource "matia_integration" "example" {
  name               = "example-postgres-to-snowflake"
  source_id          = var.source_id
  destination_id     = var.destination_id
  destination_schema = "raw"
  source_settings = jsonencode({
    incremental_mode = "Change Stream"
    max_clients      = 4
  })
}

variable "source_id" {
  type = string
}

variable "destination_id" {
  type = string
}
