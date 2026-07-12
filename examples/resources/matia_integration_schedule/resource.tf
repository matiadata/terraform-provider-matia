resource "matia_integration_schedule" "example" {
  integration_id        = var.integration_id
  replication_frequency = "manual"
}

variable "integration_id" {
  type = string
}
