-- Local development only. Creates the notification service's login role in the shared
-- ioe database. The service creates and owns the `notification` schema when it migrates.
-- Safe to run on every `docker compose up`.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'notification') THEN
        CREATE ROLE notification LOGIN PASSWORD 'notification-local-dev';
    END IF;
END
$$;

GRANT CONNECT, CREATE ON DATABASE ioe TO notification;
