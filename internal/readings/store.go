package readings

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schema = `
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
)`

// Kind is the quantity a reading measures; it must match the
// reading_kind enum in the schema.
type Kind string

const (
	Temperature Kind = "temperature"
	Humidity    Kind = "humidity"
)

type Reading struct {
	DeviceID   string
	Kind       Kind
	RecordedAt time.Time
	Value      float64
}

// Store persists sensor readings to postgres.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Insert stores a reading. Devices occasionally resend the same
// measurement, so duplicates on (device, kind, time) are ignored.
func (s *Store) Insert(ctx context.Context, r Reading) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO readings (device_id, kind, recorded_at, value)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		r.DeviceID, r.Kind, r.RecordedAt, r.Value)
	return err
}

// Device is a registered eWeLink device.
type Device struct {
	DeviceID     string
	Name         string
	ProductModel string
	Details      []byte
}

// UpsertDevices inserts new devices and refreshes the details of
// existing ones.
func (s *Store) UpsertDevices(ctx context.Context, devices []Device) error {
	batch := &pgx.Batch{}
	for _, d := range devices {
		batch.Queue(
			`INSERT INTO devices (device_id, name, product_model, details, updated_at)
			 VALUES ($1, $2, $3, $4, now())
			 ON CONFLICT (device_id) DO UPDATE
			 SET name = EXCLUDED.name,
			     product_model = EXCLUDED.product_model,
			     details = EXCLUDED.details,
			     updated_at = now()`,
			d.DeviceID, d.Name, d.ProductModel, d.Details)
	}
	return s.pool.SendBatch(ctx, batch).Close()
}

func (s *Store) Close() {
	s.pool.Close()
}
