-- Idempotent so that databases created before migrations were introduced
-- adopt this as their baseline.
DO $$ BEGIN
	CREATE TYPE reading_kind AS ENUM ('temperature', 'humidity');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS readings (
	device_id   text         NOT NULL,
	kind        reading_kind NOT NULL,
	recorded_at timestamptz  NOT NULL,
	value       numeric(6,2) NOT NULL,
	PRIMARY KEY (device_id, kind, recorded_at)
);

CREATE TABLE IF NOT EXISTS devices (
	device_id     text        PRIMARY KEY,
	name          text        NOT NULL,
	product_model text        NOT NULL DEFAULT '',
	details       jsonb       NOT NULL,
	updated_at    timestamptz NOT NULL DEFAULT now()
);
