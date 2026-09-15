# With on_schema_update = "enableColumnChanges" on the integration, initial
# creation disables unlisted configurable tables and keeps column defaults.
# With "ignoreAll", unlisted configurable columns are disabled too.
# With "enableAll", unlisted tables and columns keep their current settings.
# Keep the integration paused until this selection is applied if data must not
# sync beforehand. Later applies manage only explicitly declared fields.
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
