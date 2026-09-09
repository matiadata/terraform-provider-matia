# One Snowflake user for every purpose: `credentials` alone, no purpose block.
resource "matia_asset" "shared" {
  name        = "analytics-warehouse"
  type        = "snowflake"
  auth_method = "keyPair"

  credentials = {
    account     = var.snowflake_account
    username    = "MATIA_USER"
    database    = "RAW"
    warehouse   = "LOAD_WH"
    private_key = var.snowflake_private_key
    public_key  = var.snowflake_public_key
  }
}
