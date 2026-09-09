---
page_title: "matia_asset Resource - matia"
subcategory: ""
description: |-
  A multipurpose Matia asset: one connector that integrations can use as an ETL destination, a reverse-ETL source, an observability target and optionally an ETL source. Currently supported for Snowflake. Give it shared credentials, or one credentials block per purpose; a purpose block set alongside credentials replaces the shared credentials for that purpose only. Matia cannot change the credentials of an existing multipurpose asset, so any credentials change forces resource replacement, and every integration bound to the asset is replaced with it.
---

# matia_asset (Resource)

A multipurpose Matia asset: one connector that integrations can use as an ETL destination, a reverse-ETL source, an observability target and optionally an ETL source. Currently supported for Snowflake. Give it shared `credentials`, or one credentials block per purpose; a purpose block set alongside `credentials` replaces the shared credentials for that purpose only. Matia cannot change the credentials of an existing multipurpose asset, so any credentials change forces resource replacement, and every integration bound to the asset is replaced with it.

## Example Usage

```terraform
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
```

```terraform
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
```

```terraform
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
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `name` (String) The display name of the asset.
- `type` (String) The connector type. Only snowflake is supported.

### Optional

- `additional_databases` (List of String) Existing Snowflake databases integrations may load into besides the ETL credentials' default database. Omit it to leave the list unmanaged; set [] to clear it. Matia trims and de-duplicates names case-insensitively without planning a change.
- `additional_warehouses` (List of String) Existing Snowflake warehouses integrations may run on besides the ETL credentials' default warehouse. Omit it to leave the list unmanaged; set [] to clear it.
- `auth_method` (String) The authentication method of the credentials: direct for passwords, keyPair for private keys.
- `catalog` (Attributes) Credentials Matia uses for observability. Required unless credentials is set. Changing or removing this block after creation forces resource replacement. (see [below for nested schema](#nestedatt--catalog))
- `credentials` (Attributes) Credentials shared by every purpose. When set, etl, reverse_etl, catalog and etl_source are optional and each one given replaces the shared credentials for that purpose. Changing or removing this block after creation forces resource replacement. (see [below for nested schema](#nestedatt--credentials))
- `description` (String) A human-readable description of the asset. Removing it clears the description in Matia.
- `etl` (Attributes) Credentials Matia uses to load data into Snowflake. Required unless credentials is set. Changing or removing this block after creation forces resource replacement. (see [below for nested schema](#nestedatt--etl))
- `etl_source` (Attributes) Credentials Matia uses to read Snowflake as an ETL source. Supplying them enables that purpose. Changing or removing this block after creation forces resource replacement. (see [below for nested schema](#nestedatt--etl_source))
- `owners` (List of String) User IDs that own the asset, as published by GET /v1/users. The provider does not read owners back, so they are null on an imported asset until the next apply. Removing the attribute leaves the owners recorded in Matia unchanged; set [] to clear them.
- `reverse_etl` (Attributes) Credentials Matia uses to read data out of Snowflake for reverse ETL. Required unless credentials is set. Changing or removing this block after creation forces resource replacement. (see [below for nested schema](#nestedatt--reverse_etl))
- `tags` (List of String) Tag IDs to associate with the asset. Changing this forces resource replacement. The provider does not read tags back, so they are null on an imported asset and a configuration that sets them plans a replacement after import.

### Read-Only

- `connection_type` (String) Connection type reported by Matia; multi_purpose for assets this resource creates.
- `default_database` (String) Database of the ETL credentials, as reported by Matia.
- `default_warehouse` (String) Warehouse of the ETL credentials, as reported by Matia.
- `id` (String) The asset ID assigned by Matia.

<a id="nestedatt--catalog"></a>
### Nested Schema for `catalog`

Required:

- `account` (String) Snowflake account identifier (e.g. myorg-myaccount).
- `username` (String) Snowflake user for this purpose.
- `warehouse` (String) Warehouse the user runs on.

Optional:

- `database` (String) Default database. Required everywhere except on catalog, and on reverse_etl when database_schemas is set instead.
- `database_schemas` (Attributes List) Databases this purpose works in, each paired with a schema. Matia stores reverse-ETL and catalog databases this way, and its Manage tab reads them from here. On reverse_etl it replaces database. Elsewhere it is recorded alongside database, which etl and etl_source still need: the destination resolver and the ETL source read that single field and never this list. (see [below for nested schema](#nestedatt--catalog--database_schemas))
- `password` (String, Sensitive) Password for password authentication. Set this or private_key.
- `private_key` (String, Sensitive) PKCS#8 PEM private key for key-pair authentication, newlines included. Set this or password.
- `private_key_passphrase` (String, Sensitive) Passphrase of an encrypted private_key.
- `public_key` (String) Base64 body of the public key, without the BEGIN and END lines, as Snowflake's RSA_PUBLIC_KEY expects. Matia never authenticates with it: the dashboard shows it in the edit wizard and puts it in the Snowflake setup script. Adding it later replaces the asset.

<a id="nestedatt--catalog--database_schemas"></a>
### Nested Schema for `catalog.database_schemas`

Required:

- `database` (String) Name of an existing database.
- `schema` (String) Schema within the database. Matia requires one on every row.



<a id="nestedatt--credentials"></a>
### Nested Schema for `credentials`

Required:

- `account` (String) Snowflake account identifier (e.g. myorg-myaccount).
- `username` (String) Snowflake user for this purpose.
- `warehouse` (String) Warehouse the user runs on.

Optional:

- `database` (String) Default database. Required everywhere except on catalog, and on reverse_etl when database_schemas is set instead.
- `database_schemas` (Attributes List) Databases this purpose works in, each paired with a schema. Matia stores reverse-ETL and catalog databases this way, and its Manage tab reads them from here. On reverse_etl it replaces database. Elsewhere it is recorded alongside database, which etl and etl_source still need: the destination resolver and the ETL source read that single field and never this list. (see [below for nested schema](#nestedatt--credentials--database_schemas))
- `password` (String, Sensitive) Password for password authentication. Set this or private_key.
- `private_key` (String, Sensitive) PKCS#8 PEM private key for key-pair authentication, newlines included. Set this or password.
- `private_key_passphrase` (String, Sensitive) Passphrase of an encrypted private_key.
- `public_key` (String) Base64 body of the public key, without the BEGIN and END lines, as Snowflake's RSA_PUBLIC_KEY expects. Matia never authenticates with it: the dashboard shows it in the edit wizard and puts it in the Snowflake setup script. Adding it later replaces the asset.

<a id="nestedatt--credentials--database_schemas"></a>
### Nested Schema for `credentials.database_schemas`

Required:

- `database` (String) Name of an existing database.
- `schema` (String) Schema within the database. Matia requires one on every row.



<a id="nestedatt--etl"></a>
### Nested Schema for `etl`

Required:

- `account` (String) Snowflake account identifier (e.g. myorg-myaccount).
- `username` (String) Snowflake user for this purpose.
- `warehouse` (String) Warehouse the user runs on.

Optional:

- `database` (String) Default database. Required everywhere except on catalog, and on reverse_etl when database_schemas is set instead.
- `database_schemas` (Attributes List) Databases this purpose works in, each paired with a schema. Matia stores reverse-ETL and catalog databases this way, and its Manage tab reads them from here. On reverse_etl it replaces database. Elsewhere it is recorded alongside database, which etl and etl_source still need: the destination resolver and the ETL source read that single field and never this list. (see [below for nested schema](#nestedatt--etl--database_schemas))
- `password` (String, Sensitive) Password for password authentication. Set this or private_key.
- `private_key` (String, Sensitive) PKCS#8 PEM private key for key-pair authentication, newlines included. Set this or password.
- `private_key_passphrase` (String, Sensitive) Passphrase of an encrypted private_key.
- `public_key` (String) Base64 body of the public key, without the BEGIN and END lines, as Snowflake's RSA_PUBLIC_KEY expects. Matia never authenticates with it: the dashboard shows it in the edit wizard and puts it in the Snowflake setup script. Adding it later replaces the asset.

<a id="nestedatt--etl--database_schemas"></a>
### Nested Schema for `etl.database_schemas`

Required:

- `database` (String) Name of an existing database.
- `schema` (String) Schema within the database. Matia requires one on every row.



<a id="nestedatt--etl_source"></a>
### Nested Schema for `etl_source`

Required:

- `account` (String) Snowflake account identifier (e.g. myorg-myaccount).
- `username` (String) Snowflake user for this purpose.
- `warehouse` (String) Warehouse the user runs on.

Optional:

- `database` (String) Default database. Required everywhere except on catalog, and on reverse_etl when database_schemas is set instead.
- `database_schemas` (Attributes List) Databases this purpose works in, each paired with a schema. Matia stores reverse-ETL and catalog databases this way, and its Manage tab reads them from here. On reverse_etl it replaces database. Elsewhere it is recorded alongside database, which etl and etl_source still need: the destination resolver and the ETL source read that single field and never this list. (see [below for nested schema](#nestedatt--etl_source--database_schemas))
- `password` (String, Sensitive) Password for password authentication. Set this or private_key.
- `private_key` (String, Sensitive) PKCS#8 PEM private key for key-pair authentication, newlines included. Set this or password.
- `private_key_passphrase` (String, Sensitive) Passphrase of an encrypted private_key.
- `public_key` (String) Base64 body of the public key, without the BEGIN and END lines, as Snowflake's RSA_PUBLIC_KEY expects. Matia never authenticates with it: the dashboard shows it in the edit wizard and puts it in the Snowflake setup script. Adding it later replaces the asset.

<a id="nestedatt--etl_source--database_schemas"></a>
### Nested Schema for `etl_source.database_schemas`

Required:

- `database` (String) Name of an existing database.
- `schema` (String) Schema within the database. Matia requires one on every row.



<a id="nestedatt--reverse_etl"></a>
### Nested Schema for `reverse_etl`

Required:

- `account` (String) Snowflake account identifier (e.g. myorg-myaccount).
- `username` (String) Snowflake user for this purpose.
- `warehouse` (String) Warehouse the user runs on.

Optional:

- `database` (String) Default database. Required everywhere except on catalog, and on reverse_etl when database_schemas is set instead.
- `database_schemas` (Attributes List) Databases this purpose works in, each paired with a schema. Matia stores reverse-ETL and catalog databases this way, and its Manage tab reads them from here. On reverse_etl it replaces database. Elsewhere it is recorded alongside database, which etl and etl_source still need: the destination resolver and the ETL source read that single field and never this list. (see [below for nested schema](#nestedatt--reverse_etl--database_schemas))
- `password` (String, Sensitive) Password for password authentication. Set this or private_key.
- `private_key` (String, Sensitive) PKCS#8 PEM private key for key-pair authentication, newlines included. Set this or password.
- `private_key_passphrase` (String, Sensitive) Passphrase of an encrypted private_key.
- `public_key` (String) Base64 body of the public key, without the BEGIN and END lines, as Snowflake's RSA_PUBLIC_KEY expects. Matia never authenticates with it: the dashboard shows it in the edit wizard and puts it in the Snowflake setup script. Adding it later replaces the asset.

<a id="nestedatt--reverse_etl--database_schemas"></a>
### Nested Schema for `reverse_etl.database_schemas`

Required:

- `database` (String) Name of an existing database.
- `schema` (String) Schema within the database. Matia requires one on every row.

## Credentials, state and import

- Credentials are stored in Terraform state, marked sensitive. Matia never returns them, so state holds exactly what the configuration last applied.
- Matia cannot change the credentials of an existing multipurpose asset. Any change to `credentials`, `etl`, `reverse_etl`, `catalog` or `etl_source`, including adding or removing a block, plans a replacement. Integrations that reference the asset are replaced with it.
- After `terraform import`, the credentials blocks are null in state. The next apply records the configured blocks as-is, with a warning, and does not compare them with what Matia stores; no credentials are sent to the API.
- `owners`, `tags` and `auth_method` are not read back by the provider, so they are null after import. The next apply re-sends configured `owners` and `auth_method`; a configuration that sets `tags` on an imported asset plans a replacement.
- On a backend that supports multipurpose assets, every new Snowflake asset is created as `multi_purpose`, including those created with `matia_destination` or `matia_source`. Those resources keep working for existing assets; use `matia_asset` for new Snowflake assets.

## Import

Import is supported using the following syntax:

In Terraform v1.5.0 and later, the [`import` block](https://developer.hashicorp.com/terraform/language/import) can be used with the `id` attribute, for example:

```terraform
import {
  to = matia_asset.example
  id = "6aa0382d49359e5d73e424ba"
}
```

The [`terraform import` command](https://developer.hashicorp.com/terraform/cli/commands/import) can be used, for example:

```shell
terraform import matia_asset.example "6aa0382d49359e5d73e424ba"
```
