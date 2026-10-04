# ioe-backend

Backend for the IOE learning management system: single tenant, Google sign-in only,
roles `student`, `instructor`, `root_admin`.

## Architecture

Modular monolith, domain-driven design, hexagonal architecture.

```text
cmd/api/              composition root (the only package that wires everything)
internal/platform/    shared infrastructure; never imports a bounded context
internal/<context>/
  domain/             entities, value objects, events; stdlib + uuid + platform/auth only
  app/                use cases and the ports they own; imports only its domain and platform/{auth,clock}
  adapters/           implementations of app ports (postgres, http, external services)
migrations/           goose SQL; one PostgreSQL schema per context
api/openapi.yaml      public HTTP contract
```

Rules:

- A context never imports another context. When context B needs data from A synchronously,
  B defines a port in `B/app` and `cmd/api` wires an adapter backed by A's application service.
  Asynchronous notifications use domain events through `internal/platform/outbox`.
- A context reads and writes only its own schema.
- Write the outbox message in the same transaction as the state change.
- Adding a context: add `<ctx>-domain` and `<ctx>-app` depguard rules in `.golangci.yml`,
  and add the context's import path to `platform-independent-of-contexts` and to a
  deny rule for every other context.

## Required gates

```sh
make check            # tidy diff, golangci-lint, sqlc diff, go test -race, govulncheck, gitleaks
make test-integration # requires a running Docker daemon
docker compose config
make docker-build
git diff --check
```

Integration tests must not be reported as passing unless they actually ran.
Use Conventional Commits.
