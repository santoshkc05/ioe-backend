# ioe-backend

Go backend for the IOE learning management system.

## Quick start

```sh
cp .env.example .env
make keygen            # paste the key into .env
make compose-up        # PostgreSQL + OpenObserve
make migrate-up
make run
```

See `AGENTS.md` for architecture rules and required checks, and `api/openapi.yaml` for the HTTP contract.
