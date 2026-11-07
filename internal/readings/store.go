package readings

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a requested record doesn't exist.
var ErrNotFound = errors.New("not found")

// Kind is the quantity a reading measures; it must match the
// reading_kind enum in the migrations.
type Kind string

const (
	Temperature       Kind = "temperature"
	Humidity          Kind = "humidity"
	TargetTemperature Kind = "target_temperature" // a thermostat's set point
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

	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func NewStore(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Insert stores a reading. Devices occasionally resend the same
// measurement, so duplicates on (device, kind, time) are ignored.
func (s *Store) Insert(ctx context.Context, r Reading) error {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO readings (device_id, kind, recorded_at, value)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		r.DeviceID, r.Kind, r.RecordedAt, r.Value)
	if err == nil && tag.RowsAffected() > 0 {
		s.notify()
	}
	return err
}

// Subscribe returns a channel that receives a signal whenever a new
// reading is stored, and a function to cancel the subscription. Signals
// are coalesced: a slow receiver sees at most one pending signal.
func (s *Store) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	if s.subs == nil {
		s.subs = make(map[chan struct{}]struct{})
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

func (s *Store) notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
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
