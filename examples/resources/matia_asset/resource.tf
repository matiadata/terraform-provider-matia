# Shared credentials with per-purpose overrides: `reverse_etl` and `catalog` replace `credentials` for their purpose only.
resource "matia_asset" "example" {
  name        = "analytics-warehouse"
  type        = "snowflake"
  auth_method = "keyPair"

  # The public key is for the dashboard's setup script; Matia authenticates with the private key.
  credentials = {
    account     = var.snowflake_account
    username    = "MATIA_USER"
    database    = "RAW"
    warehouse   = "LOAD_WH"
    private_key = var.snowflake_private_key
    public_key  = var.snowflake_public_key
  }

  # Reverse ETL and observability store their databases as database/schema
  # pairs, which is what the asset's Manage tab lists.
  reverse_etl = {
    account     = var.snowflake_account
    username    = "MATIA_RETL"
    warehouse   = "RETL_WH"
    private_key = var.snowflake_private_key

    database_schemas = [
      { database = "MART", schema = "PUBLIC" },
      { database = "REPORTING", schema = "ANALYTICS" },
    ]
  }

  # Observability runs as its own user, with no default database.
  catalog = {
    account     = var.snowflake_account
    username    = "MATIA_OBSERVABILITY"
    warehouse   = "OBSERVABILITY_WH"
    private_key = var.snowflake_observability_private_key

    database_schemas = [
      { database = "RAW", schema = "INFORMATION_SCHEMA" },
    ]
  }

  # Existing databases and warehouses an integration may pick instead of the defaults.
  additional_databases  = ["STAGING"]
  additional_warehouses = ["BULK_WH"]
}

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

variable "snowflake_public_key" {
  type = string
}

variable "snowflake_observability_private_key" {
  type      = string
  sensitive = true
}
