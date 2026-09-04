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

# The full discovered catalog - every table, column and primary key, including the ones
# config does not declare.
output "discovered_schema" {
  value = jsondecode(matia_integration_schema.example.effective_schema)
}
