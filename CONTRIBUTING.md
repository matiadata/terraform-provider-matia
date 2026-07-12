# Contributing

Thank you for your interest in improving the Matia Terraform provider.

## How this repository works

The public repository is a **read-only mirror**, published from each release of Matia's internal repository. Development happens internally, so pull requests opened against the mirror cannot be merged directly.

That said, contributions are welcome — they just flow through issues:

- **Bug reports**: open a [GitHub issue](../../issues) using the bug report template. Include provider and Terraform versions, a minimal configuration, and the error output (with secrets redacted).
- **Feature requests**: open an issue using the feature request template — describing the use case helps us design the right thing.
- **Patches**: open an issue describing the change you have in mind. We'll work with you to land it and credit you in the changelog.
- **Security vulnerabilities**: never via a public issue — see [SECURITY.md](SECURITY.md) for private reporting.

## Building from source

```shell
go build ./...
go test ./...
```

The provider is built with the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework). To run a locally built binary against your own configuration, use Terraform's [`dev_overrides`](https://developer.hashicorp.com/terraform/cli/config/config-file#development-overrides-for-provider-developers).

---

*Matia maintainers: the full internal development guide lives at `internal-docs/CONTRIBUTING.md` (internal repository only).*
