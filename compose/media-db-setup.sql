-- Local development only. Creates the media service's login role in the shared ioe database.
-- The service creates and owns the `media_service` schema when it migrates (AUTO_MIGRATE).
-- Safe to run on every `docker compose up`.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'media_service') THEN
        CREATE ROLE media_service LOGIN PASSWORD 'media-local-dev';
    END IF;
END
$$;

GRANT CONNECT, CREATE ON DATABASE ioe TO media_service;
