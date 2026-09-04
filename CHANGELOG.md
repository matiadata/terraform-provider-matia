# Changelog

All notable changes to this provider are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - 2026-09-04

### Added

- [provider] support terraform import for matia_integration_schedule (#73)
- [provider] detect integration_schema drift and expose effective_schema (#76)
- [provider] support terraform import for matia_integration (#79)
- **BREAKING** [provider] support terraform import for matia_hybrid_deployment_agent (#74)
- [provider] warn when a declared integration schema leaves the catalog (#78)
- [provider] disable tables removed from integration_schema config (#82)
- [examples] add MSSQL -> Snowflake integration example (#92)

### Fixed

- [provider] correct integration_schema Update no-op comparison (#68)
- [provider] mark integration_schedule cron_expression and base_time Computed (#77)
- [client] retry HTTP requests only when safe for the request method (#83)
- Write release notes outside the work tree (#93)

## [Unreleased]

## [0.1.0] - 2026-07-12

### Added

- Initial release of the Matia Terraform provider.
- Resources: `matia_source`, `matia_destination`, `matia_integration`, `matia_integration_schedule`, `matia_integration_schema`, `matia_hybrid_deployment_agent`.
- Provider configuration via `api_token` and `api_url`, each settable in the provider block or via the `MATIA_API_TOKEN` / `MATIA_API_URL` environment variables (`api_url` defaults to `https://api.matia.io/v1`).
