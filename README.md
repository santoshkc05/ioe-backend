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

## Notifications

Welcome emails are sent through the standalone notification service in `../notification`.
`docker compose --profile app up --build` creates its database role, runs its migrations into
the `notification` schema of the `ioe` database, and starts it with Mailpit
(http://localhost:8025). To use it with `make run`, start only the service and point `.env` at it:

```sh
docker compose --profile app up -d --wait notification
# .env: NOTIFICATION_SERVICE_BASE_URL=http://localhost:8081 and the local key from .env.example
```

In production the notification service shares the application database but owns only the
`notification` schema. Before its first `notification-service migrate`, a database
administrator creates a dedicated login role for it and runs
`GRANT CONNECT, CREATE ON DATABASE <database> TO <role>;`. Never grant the application role
access to that schema, and never manage it from `ioe-backend` migrations. Deploy
`notification-service migrate` as its own job before `notification-service serve`.
