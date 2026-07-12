resource "matia_integration_schema" "example" {
  integration_id = var.integration_id
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

variable "integration_id" {
  type = string
}
