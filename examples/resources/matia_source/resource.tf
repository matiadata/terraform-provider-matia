resource "matia_source" "example" {
  name = "example-postgres-source"
  type = "postgres"

  connection_config = jsonencode({
    hostname = var.postgres_hostname
    port     = "5432"
    database = "postgres"
    ssl      = false
  })

  connection_secrets = jsonencode({
    username = var.postgres_username
    password = var.postgres_password
  })
}

variable "postgres_hostname" {
  type = string
}

variable "postgres_username" {
  type = string
}

variable "postgres_password" {
  type      = string
  sensitive = true
}
