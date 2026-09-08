# Terraform Provider for Matia

The official Terraform provider for [Matia](https://www.matia.io) - manage sources, destinations, integrations, replication schedules, and schema configuration as code through the Matia API.

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.0 (tested against 1.5, 1.13, and 1.14)

## Usage

```hcl
terraform {
  required_providers {
    matia = {
      source  = "matiadata/matia"
      version = "~> 0.1"
    }
  }
}

provider "matia" {}
```

Provide your API key via the `MATIA_API_TOKEN` environment variable:

```shell
export MATIA_API_TOKEN="your-api-key"
terraform plan
```

The environment variable is the preferred way to supply the token: your configuration stays free of secrets and works unchanged across workspaces and CI. Setting `api_token` in the `provider` block also works and takes precedence, reserve it for when the token comes from a secrets manager or another Terraform data source. An empty `api_token` value is treated as unset, so the environment variable still applies.

### Authentication

The provider authenticates every request with a Matia API key (sent as the `X-Api-Key` header). Generate a key in your Matia workspace, or contact your Matia representative.

| Attribute   | Environment variable | Required | Description                                                                                  |
| ----------- | -------------------- | -------- | -------------------------------------------------------------------------------------------- |
| `api_token` | `MATIA_API_TOKEN`    | yes      | Matia API key. Provide via the environment variable or the provider block. Marked sensitive. |
| `api_url`   | `MATIA_API_URL`      | no       | Base URL for the Matia v1 API (must end with `/v1`). Defaults to `https://api.matia.io/v1`.  |

## Resources

| Resource                        | Description                                                  |
| ------------------------------- | ------------------------------------------------------------ |
| `matia_source`                  | Manage a Matia source asset                                  |
| `matia_destination`             | Manage a Matia destination asset                             |
| `matia_asset`                   | Manage a multipurpose Snowflake asset                        |
| `matia_integration`             | Manage a data integration between a source and a destination |
| `matia_integration_schedule`    | Manage the replication schedule for an integration           |
| `matia_integration_schema`      | Manage schema configuration for an integration               |
| `matia_hybrid_deployment_agent` | Manage a hybrid deployment agent                             |

Full documentation for each resource is published on the [Terraform Registry](https://registry.terraform.io/providers/matiadata/matia/latest/docs) and mirrored in [docs/](docs/).

## Examples

| Example                                                                                                              | Description                                                             |
| -------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| [`examples/getting-started/`](examples/getting-started/)                                                             | Minimal configuration to verify provider setup                          |
| [`examples/integration-postgres-snowflake/`](examples/integration-postgres-snowflake/)                               | Postgres -> Snowflake integration using cloud execution                 |
| [`examples/integration-postgres-snowflake-existing-agent/`](examples/integration-postgres-snowflake-existing-agent/) | Postgres -> Snowflake hybrid integration using an existing agent        |
| [`examples/integration-postgres-snowflake-create-agent/`](examples/integration-postgres-snowflake-create-agent/)     | Postgres -> Snowflake hybrid integration with a Terraform-created agent |
| [`examples/integration-mssql-snowflake/`](examples/integration-mssql-snowflake/)                                     | SQL Server -> Snowflake integration using cloud execution               |
| [`examples/integration-mysql-bigquery/`](examples/integration-mysql-bigquery/)                                       | MySQL -> BigQuery integration using cloud execution                     |
| [`examples/integration-mysql-bigquery-existing-agent/`](examples/integration-mysql-bigquery-existing-agent/)         | MySQL -> BigQuery hybrid integration using an existing agent            |
| [`examples/integration-mysql-bigquery-create-agent/`](examples/integration-mysql-bigquery-create-agent/)             | MySQL -> BigQuery hybrid integration with a Terraform-created agent     |
| [`examples/integration/`](examples/integration/)                                                                     | Full Postgres -> Snowflake integration stack                            |
| [`examples/integration/existing-assets/`](examples/integration/existing-assets/)                                     | Integration stack using existing source/destination IDs                 |
| [`examples/resources/`](examples/resources/)                                                                         | Per-resource configuration snippets                                     |

## Support and contributing

- **Bug reports and feature requests**: please open a [GitHub issue](../../issues) using the provided templates.
- **Security vulnerabilities**: please report privately - see [SECURITY.md](SECURITY.md). Do not open a public issue.
- **Pull requests**: the public repository is a read-only mirror published from each release of Matia's internal repository, so we cannot merge pull requests directly. If you have a patch, open an issue describing it and we'll work with you to land it.

Development setup and project conventions are documented in [CONTRIBUTING.md](CONTRIBUTING.md).

## License

This project is licensed under the [Mozilla Public License 2.0](LICENSE).

The Matia name and logo are trademarks of Matia and are not covered by the MPL-2.0 license grant.
