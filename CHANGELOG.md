# Changelog

All notable changes to this provider are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Initial release of the Matia Terraform provider.
- Resources: `matia_source`, `matia_destination`, `matia_integration`, `matia_integration_schedule`, `matia_integration_schema`, `matia_hybrid_deployment_agent`.
- Provider configuration via `api_token` and `api_url`, each settable in the provider block or via the `MATIA_API_TOKEN` / `MATIA_API_URL` environment variables (`api_url` defaults to `https://api.matia.io/v1`).
