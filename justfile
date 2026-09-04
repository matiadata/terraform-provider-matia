default: fmt lint install generate

build:
    go build -v ./...

install: build
    go install -v ./...

# Build + install the provider, then write a gitignored terraform.rc so Terraform uses your local build.
dev-override: install
    #!/usr/bin/env bash
    set -euo pipefail
    bindir="$(go env GOBIN)"
    # go env GOPATH can be a colon-separated list; go install uses the first entry.
    [ -n "$bindir" ] || bindir="$(go env GOPATH | cut -d: -f1)/bin"
    printf 'provider_installation {\n  dev_overrides {\n    "registry.terraform.io/matiadata/matia" = "%s"\n  }\n\n  direct {}\n}\n' "$bindir" > terraform.rc
    echo "Wrote $(pwd)/terraform.rc (dev_overrides -> $bindir)"
    echo "Activate it for this shell, then run terraform plan/apply (never terraform init):"
    echo "  export TF_CLI_CONFIG_FILE=\"$(pwd)/terraform.rc\""

lint:
    go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run

generate:
    cd tools && go generate ./...

generate-check: generate
    git add --intent-to-add -- docs/ examples/
    git diff --compact-summary --exit-code -- docs/ examples/

fmt:
    gofmt -s -w -e .

test:
    go test -v -cover -timeout=120s -parallel=10 ./...

# Preview the changelog the next release PR would generate (requires git-cliff:
# `brew install git-cliff`). Defaults to unreleased commits since the last tag;
# pass extra args, e.g. `just changelog --tag v0.2.0` or `just changelog v0.1.0..HEAD`.
changelog *ARGS="--unreleased":
    git-cliff {{ ARGS }}

testacc:
    TF_ACC=1 go test -v -cover -timeout 120m ./...

# goreleaser v2.17.0 needs Go >= 1.26.4 while go.mod targets 1.25.8; pin the
# toolchain so these recipes also work where GOTOOLCHAIN=local disables the
# auto-download.
release-check:
    GOTOOLCHAIN=go1.26.5 go run github.com/goreleaser/goreleaser/v2@v2.17.0 check

release-snapshot:
    GOTOOLCHAIN=go1.26.5 go run github.com/goreleaser/goreleaser/v2@v2.17.0 release --snapshot --clean --skip=sign
