default: fmt lint install generate

build:
    go build -v ./...

install: build
    go install -v ./...

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

testacc:
    TF_ACC=1 go test -v -cover -timeout 120m ./...

# goreleaser v2.17.0 needs Go >= 1.26.4 while go.mod targets 1.25.8; pin the
# toolchain so these recipes also work where GOTOOLCHAIN=local disables the
# auto-download.
release-check:
    GOTOOLCHAIN=go1.26.5 go run github.com/goreleaser/goreleaser/v2@v2.17.0 check

release-snapshot:
    GOTOOLCHAIN=go1.26.5 go run github.com/goreleaser/goreleaser/v2@v2.17.0 release --snapshot --clean --skip=sign
