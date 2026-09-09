# One Snowflake user per purpose: without `credentials`, `etl`, `reverse_etl` and `catalog` are all required.
resource "matia_asset" "per_purpose" {
  name        = "analytics-warehouse"
  type        = "snowflake"
  auth_method = "keyPair"

  etl = {
    account     = var.snowflake_account
    username    = "MATIA_ETL_USER"
    database    = "RAW"
    warehouse   = "LOAD_WH"
    private_key = var.snowflake_etl_private_key
    public_key  = var.snowflake_etl_public_key
  }

  reverse_etl = {
    account     = var.snowflake_account
    username    = "MATIA_RETL_USER"
    warehouse   = "SYNC_WH"
    private_key = var.snowflake_reverse_etl_private_key
    public_key  = var.snowflake_reverse_etl_public_key

    database_schemas = [
      { database = "MART", schema = "PUBLIC" },
    ]
  }

  # An encrypted private key needs its passphrase alongside.
  catalog = {
    account                = var.snowflake_account
    username               = "MATIA_CATALOG_USER"
    warehouse              = "OBSERVABILITY_WH"
    private_key            = var.snowflake_catalog_private_key
    private_key_passphrase = var.snowflake_catalog_private_key_passphrase
    public_key             = var.snowflake_catalog_public_key

    database_schemas = [
      { database = "RAW", schema = "INFORMATION_SCHEMA" },
    ]
  }
}

variable "snowflake_etl_private_key" {
  type      = string
  sensitive = true
}

variable "snowflake_etl_public_key" {
  type = string
}

variable "snowflake_reverse_etl_private_key" {
  type      = string
  sensitive = true
}

variable "snowflake_reverse_etl_public_key" {
  type = string
}

variable "snowflake_catalog_private_key" {
  type      = string
  sensitive = true
}

variable "snowflake_catalog_private_key_passphrase" {
  type      = string
  sensitive = true
}

variable "snowflake_catalog_public_key" {
  type = string
}
