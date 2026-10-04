GITLEAKS_VERSION    := v8.30.1
GOVULNCHECK_VERSION := v1.8.0
LEFTHOOK_VERSION    := v2.1.16

.PHONY: run build test test-integration lint fmt vuln secrets tidy-check check hooks migrate-up migrate-down migrate-status keygen sqlc sqlc-check docker-build compose-up compose-down

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

migrate-up:
	go run ./cmd/api migrate up

migrate-down:
	go run ./cmd/api migrate down

migrate-status:
	go run ./cmd/api migrate status

check: tidy-check lint sqlc-check test vuln secrets

keygen:
	go run ./cmd/keygen

sqlc:
	sqlc generate

sqlc-check:
	sqlc diff

docker-build:
	docker build --build-arg VERSION=$$(git describe --tags --always --dirty) -t ioe-backend:local .

compose-up:
	docker compose up -d --wait postgres openobserve

compose-down:
	docker compose down
