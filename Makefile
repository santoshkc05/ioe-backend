GITLEAKS_VERSION    := v8.30.1
GOVULNCHECK_VERSION := v1.8.0
LEFTHOOK_VERSION    := v2.1.16

.PHONY: run build test test-integration lint fmt vuln secrets tidy-check check hooks

run:
	go run ./cmd/api

build:
	CGO_ENABLED=0 go build -trimpath -o bin/api ./cmd/api

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./...

lint:
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

secrets:
	go run github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION) git --redact --no-banner .

tidy-check:
	go mod tidy -diff

hooks:
	go run github.com/evilmartians/lefthook/v2@$(LEFTHOOK_VERSION) install

check: tidy-check lint test vuln secrets
