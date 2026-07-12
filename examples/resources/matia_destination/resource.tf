resource "matia_destination" "example" {
  name = "example-snowflake-destination"
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
