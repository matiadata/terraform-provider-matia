# Shared credentials with per-purpose overrides: `reverse_etl` and `catalog` replace `credentials` for their purpose only.
resource "matia_asset" "example" {
  name        = "analytics-warehouse"
  type        = "snowflake"
  auth_method = "keyPair"

  credentials = {
    account     = var.snowflake_account
    username    = "MATIA_USER"
    database    = "RAW"
    warehouse   = "LOAD_WH"
    private_key = var.snowflake_private_key
  }

  reverse_etl = {
    account     = var.snowflake_account
    username    = "MATIA_RETL"
    database    = "MART"
    warehouse   = "RETL_WH"
    private_key = var.snowflake_private_key
  }

  # Observability runs as its own user, with no default database.
  catalog = {
    account     = var.snowflake_account
    username    = "MATIA_OBSERVABILITY"
    warehouse   = "OBSERVABILITY_WH"
    private_key = var.snowflake_observability_private_key
  }

  # Existing databases and warehouses an integration may pick instead of the defaults.
  additional_databases  = ["STAGING"]
  additional_warehouses = ["BULK_WH"]
}

# A Postgres source also needs source_settings (see matia_integration).
resource "matia_integration" "example" {
  source_id             = var.source_id
  destination_id        = matia_asset.example.id
  destination_schema    = "raw"
  destination_database  = "STAGING"
  destination_warehouse = "BULK_WH"
}

variable "source_id" {
  type = string
}

variable "snowflake_account" {
  type = string
}

variable "snowflake_private_key" {
  type      = string
  sensitive = true
}

variable "snowflake_observability_private_key" {
  type      = string
  sensitive = true
}
