package readings

import (
	"context"
	"errors"
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

// ErrNotFound is returned when a requested record doesn't exist.
var ErrNotFound = errors.New("not found")

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

// DeviceRecord is a stored device with the details captured at its last refresh.
type DeviceRecord struct {
	DeviceID     string
	Name         string
	ProductModel string
	Details      []byte
	UpdatedAt    time.Time
}

// ListDevices returns all stored devices ordered by name. Details are not loaded.
func (s *Store) ListDevices(ctx context.Context) ([]DeviceRecord, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT device_id, name, product_model, updated_at FROM devices ORDER BY lower(name), device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var devices []DeviceRecord
	for rows.Next() {
		var d DeviceRecord
		if err := rows.Scan(&d.DeviceID, &d.Name, &d.ProductModel, &d.UpdatedAt); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

// GetDevice returns one stored device including its details. It returns
// ErrNotFound if there is no such device.
func (s *Store) GetDevice(ctx context.Context, deviceID string) (DeviceRecord, error) {
	var d DeviceRecord
	err := s.pool.QueryRow(ctx,
		`SELECT device_id, name, product_model, details, updated_at FROM devices WHERE device_id = $1`,
		deviceID).Scan(&d.DeviceID, &d.Name, &d.ProductModel, &d.Details, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// LatestReadings returns the most recent reading of each kind for a device.
func (s *Store) LatestReadings(ctx context.Context, deviceID string) ([]Reading, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT ON (kind) device_id, kind, recorded_at, value::float8
		 FROM readings
		 WHERE device_id = $1
		 ORDER BY kind, recorded_at DESC`,
		deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var latest []Reading
	for rows.Next() {
		var r Reading
		if err := rows.Scan(&r.DeviceID, &r.Kind, &r.RecordedAt, &r.Value); err != nil {
			return nil, err
		}
		latest = append(latest, r)
	}
	return latest, rows.Err()
}

// HistoryPoint is a reading together with its device's name, if known.
type HistoryPoint struct {
	DeviceID   string
	DeviceName string
	Kind       Kind
	RecordedAt time.Time
	Value      float64
}

// History returns all readings recorded since the given time, oldest first.
func (s *Store) History(ctx context.Context, since time.Time) ([]HistoryPoint, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT r.device_id, COALESCE(d.name, r.device_id), r.kind, r.recorded_at, r.value::float8
		 FROM readings r
		 LEFT JOIN devices d USING (device_id)
		 WHERE r.recorded_at >= $1
		 ORDER BY r.recorded_at`,
		since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []HistoryPoint
	for rows.Next() {
		var p HistoryPoint
		if err := rows.Scan(&p.DeviceID, &p.DeviceName, &p.Kind, &p.RecordedAt, &p.Value); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

func (s *Store) Close() {
	s.pool.Close()
}
