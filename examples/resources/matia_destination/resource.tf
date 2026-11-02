resource "matia_destination" "example" {
  name        = "example-bigquery-destination"
  type        = "bigquery"
  auth_method = "customServiceAccount"

  connection_config = jsonencode({
    project_id          = var.bigquery_project_id
    location            = "us"
    isServiceConnection = false
  })

  connection_secrets = jsonencode({
    private_key  = var.bigquery_private_key
    client_email = var.bigquery_client_email
  })
}

variable "bigquery_project_id" {
  type = string
}

variable "bigquery_private_key" {
  type      = string
  sensitive = true
}

variable "bigquery_client_email" {
  type = string
}
