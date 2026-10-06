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

## Configuration

- `SNOWFLAKE_NODE_ID` (default `0`): Snowflake node ID (0-1023). Every running replica must use a different value.

## Roles

New users are students. Accounts whose email is in `BOOTSTRAP_ROOT_ADMIN_EMAILS` become root
admins at sign-in. A root admin finds a user with `GET /v1/admin/users?email=<prefix>` and
makes them an instructor (or a student again) with `PUT /v1/admin/users/{id}/role`. The user
gets the new role at their next token refresh, within 15 minutes; a demoted instructor keeps
managing the courses they own.

## Catalog

Anyone, signed in or not, lists published courses newest first with
`GET /v1/courses?level=&price=free|paid&limit=&cursor=` and reads a published course's outline
with `GET /v1/courses/{courseID}`. Pass a page's `next_cursor` as `cursor` to get the next one.
Both routes accept an optional bearer token and are rate-limited per client IP. Lecture content
still needs an enrollment or a free preview.

## Enrollment

Students enroll themselves in published free courses with
`POST /v1/courses/{courseID}/enrollments/{theirUserID}`; paid courses answer
`402 payment_required` until payment exists. The course owner or a root admin can enroll anyone
in a published course, free or paid, and list a course's roster with
`GET /v1/courses/{courseID}/enrollments`. Enrolled students read every lecture of a published
course. A student, the owner, or a root admin cancels with `DELETE` on the same path.

## Progress

Enrolled students record per-lecture progress with
`PUT /v1/courses/{courseID}/lectures/{lectureID}/progress/{theirUserID}` and
`{"state": "in_progress" | "completed", "position_ms": n}`. A completed lecture stays completed.
Without an active enrollment the write answers `409 enrollment_required`. Students read
`GET /v1/courses/{courseID}/progress/{theirUserID}` and `GET /v1/users/{theirUserID}/progress`
(all courses plus daily activity); the course owner or a root admin can read a student's
progress in that course. Canceling an enrollment keeps progress.

## Media

Course owners and root admins upload lecture videos and images through the standalone media
service in `../hitox-media-service`. `POST /v1/courses/{courseID}/media/uploads` with
`{kind, content_type, filename, size_bytes}` returns presigned URLs; the browser uploads the
bytes directly to object storage, then calls `POST /v1/media/uploads/{assetID}/complete` and
polls `GET /v1/media/assets/{assetID}` until `status` is `ready`. Put the asset ID in a video
block's, image block's, or flashcard card's `media_asset_id`; content writes reject assets of
another course or the wrong kind with `400 invalid_media_reference`. Anyone who may read a
lecture gets short-lived URLs with `GET /v1/courses/{courseID}/lectures/{lectureID}/media/{assetID}`.

Configure `MEDIA_SERVICE_BASE_URL` (what the backend calls), `MEDIA_SERVICE_PUBLIC_URL` (what
browsers use for the service's `/v1/delivery` paths), and `MEDIA_SERVICE_API_KEY`, or none of
them to disable uploads. `docker compose --profile app up --build` runs the service with MinIO.

In production the media service shares the application database but owns only the
`media_service` schema (`ioe-backend` owns `media`). Before the media service first starts, a
database administrator creates a dedicated login role for it and runs
`GRANT CONNECT, CREATE ON DATABASE <database> TO <role>;`, and the service runs with
`DATABASE_SCHEMA=media_service`. Never grant the application role access to that schema.

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
