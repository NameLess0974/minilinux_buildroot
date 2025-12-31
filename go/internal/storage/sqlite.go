package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStorage implements Storage interface using SQLite
type SQLiteStorage struct {
	db *sql.DB
}

// NewSQLiteStorage creates a new SQLite storage instance
func NewSQLiteStorage(dbPath string) (*SQLiteStorage, error) {
	// Open database with WAL mode for better concurrency
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	// Initialize schema
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	storage := &SQLiteStorage{db: db}

	slog.Info("SQLite storage initialized", "path", dbPath)
	return storage, nil
}

// initSchema creates the database schema if not exists
func initSchema(db *sql.DB) error {
	schema := `
	-- Devices table
	CREATE TABLE IF NOT EXISTS devices (
		mac TEXT PRIMARY KEY,
		state INTEGER NOT NULL DEFAULT 0,
		block_start INTEGER,
		error_404_count INTEGER DEFAULT 0,
		flash_complete INTEGER DEFAULT 0,
		flash_img INTEGER DEFAULT 0,
		last_error TEXT,
		last_error_time INTEGER,
		last_update INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	);

	-- Index for state queries
	CREATE INDEX IF NOT EXISTS idx_devices_state ON devices(state);

	-- 404 Events table
	CREATE TABLE IF NOT EXISTS device_404_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		mac TEXT NOT NULL,
		timestamp INTEGER NOT NULL,
		FOREIGN KEY (mac) REFERENCES devices(mac) ON DELETE CASCADE
	);

	-- Index for 404 lookups
	CREATE INDEX IF NOT EXISTS idx_404_mac_ts ON device_404_events(mac, timestamp);

	-- Event log table (for audit)
	CREATE TABLE IF NOT EXISTS event_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		mac TEXT NOT NULL,
		event_type TEXT NOT NULL,
		message TEXT,
		timestamp INTEGER NOT NULL
	);

	-- Index for log queries
	CREATE INDEX IF NOT EXISTS idx_log_mac ON event_log(mac);
	CREATE INDEX IF NOT EXISTS idx_log_ts ON event_log(timestamp);
	`

	_, err := db.Exec(schema)
	return err
}

// Cleanup404EventsForMAC removes old 404 events for a specific MAC (called on-request)
func (s *SQLiteStorage) Cleanup404EventsForMAC(ctx context.Context, mac string, cutoff time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM device_404_events WHERE mac = ? AND timestamp < ?
	`, mac, cutoff.Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// GetDevice retrieves a device by MAC address
func (s *SQLiteStorage) GetDevice(ctx context.Context, mac string) (*Device, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT mac, state, block_start, error_404_count, flash_complete, flash_img,
		       last_error, last_error_time, last_update, created_at
		FROM devices WHERE mac = ?
	`, mac)

	return scanDevice(row)
}

// GetOrCreateDevice gets an existing device or creates a new one
func (s *SQLiteStorage) GetOrCreateDevice(ctx context.Context, mac string) (*Device, bool, error) {
	// Try to get existing device first
	device, err := s.GetDevice(ctx, mac)
	if err == nil {
		return device, false, nil
	}
	if err != sql.ErrNoRows {
		return nil, false, err
	}

	// Create new device
	device = NewDevice(mac)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO devices (mac, state, block_start, error_404_count, flash_complete, flash_img,
		                     last_error, last_error_time, last_update, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		device.MAC,
		int(device.State),
		timeToUnix(device.BlockStart),
		device.Error404Count,
		boolToInt(device.FlashComplete),
		boolToInt(device.FlashImg),
		device.LastError,
		timeToUnix(device.LastErrorTime),
		device.LastUpdate.Unix(),
		device.CreatedAt.Unix(),
	)

	if err != nil {
		// Handle race condition - another goroutine may have inserted
		device, err = s.GetDevice(ctx, mac)
		if err != nil {
			return nil, false, err
		}
		return device, false, nil
	}

	return device, true, nil
}

// UpdateDevice updates a device record
func (s *SQLiteStorage) UpdateDevice(ctx context.Context, device *Device) error {
	device.LastUpdate = time.Now()

	_, err := s.db.ExecContext(ctx, `
		UPDATE devices SET
			state = ?,
			block_start = ?,
			error_404_count = ?,
			flash_complete = ?,
			flash_img = ?,
			last_error = ?,
			last_error_time = ?,
			last_update = ?
		WHERE mac = ?
	`,
		int(device.State),
		timeToUnix(device.BlockStart),
		device.Error404Count,
		boolToInt(device.FlashComplete),
		boolToInt(device.FlashImg),
		device.LastError,
		timeToUnix(device.LastErrorTime),
		device.LastUpdate.Unix(),
		device.MAC,
	)
	return err
}

// ListDevices returns all devices
func (s *SQLiteStorage) ListDevices(ctx context.Context) ([]*Device, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mac, state, block_start, error_404_count, flash_complete, flash_img,
		       last_error, last_error_time, last_update, created_at
		FROM devices ORDER BY mac
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []*Device
	for rows.Next() {
		device, err := scanDeviceRows(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}

	return devices, rows.Err()
}

// Add404Event records a 404 event for a device
func (s *SQLiteStorage) Add404Event(ctx context.Context, mac string, ts time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO device_404_events (mac, timestamp) VALUES (?, ?)
	`, mac, ts.Unix())
	return err
}

// Count404InWindow counts 404 events within a time window
func (s *SQLiteStorage) Count404InWindow(ctx context.Context, mac string, windowStart time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM device_404_events
		WHERE mac = ? AND timestamp >= ?
	`, mac, windowStart.Unix()).Scan(&count)
	return count, err
}

// Get404Timestamps returns all 404 timestamps within window
func (s *SQLiteStorage) Get404Timestamps(ctx context.Context, mac string, windowStart time.Time) ([]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT timestamp FROM device_404_events
		WHERE mac = ? AND timestamp >= ?
		ORDER BY timestamp
	`, mac, windowStart.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var timestamps []time.Time
	for rows.Next() {
		var ts int64
		if err := rows.Scan(&ts); err != nil {
			return nil, err
		}
		timestamps = append(timestamps, time.Unix(ts, 0))
	}

	return timestamps, rows.Err()
}

// Cleanup404Events removes old 404 events
func (s *SQLiteStorage) Cleanup404Events(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM device_404_events WHERE timestamp < ?
	`, before.Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Close closes the database connection
func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

// Helper functions

func scanDevice(row *sql.Row) (*Device, error) {
	var (
		mac                             string
		stateInt                        int
		blockStartUnix                  sql.NullInt64
		error404Count                   int
		flashCompleteInt, flashImgInt   int
		lastError                       sql.NullString
		lastErrorTimeUnix               sql.NullInt64
		lastUpdateUnix, createdAtUnix   int64
	)

	err := row.Scan(
		&mac, &stateInt, &blockStartUnix, &error404Count,
		&flashCompleteInt, &flashImgInt, &lastError, &lastErrorTimeUnix,
		&lastUpdateUnix, &createdAtUnix,
	)
	if err != nil {
		return nil, err
	}

	return buildDevice(mac, stateInt, blockStartUnix, error404Count,
		flashCompleteInt, flashImgInt, lastError, lastErrorTimeUnix,
		lastUpdateUnix, createdAtUnix), nil
}

func scanDeviceRows(rows *sql.Rows) (*Device, error) {
	var (
		mac                             string
		stateInt                        int
		blockStartUnix                  sql.NullInt64
		error404Count                   int
		flashCompleteInt, flashImgInt   int
		lastError                       sql.NullString
		lastErrorTimeUnix               sql.NullInt64
		lastUpdateUnix, createdAtUnix   int64
	)

	err := rows.Scan(
		&mac, &stateInt, &blockStartUnix, &error404Count,
		&flashCompleteInt, &flashImgInt, &lastError, &lastErrorTimeUnix,
		&lastUpdateUnix, &createdAtUnix,
	)
	if err != nil {
		return nil, err
	}

	return buildDevice(mac, stateInt, blockStartUnix, error404Count,
		flashCompleteInt, flashImgInt, lastError, lastErrorTimeUnix,
		lastUpdateUnix, createdAtUnix), nil
}

func buildDevice(mac string, stateInt int, blockStartUnix sql.NullInt64, error404Count int,
	flashCompleteInt, flashImgInt int, lastError sql.NullString, lastErrorTimeUnix sql.NullInt64,
	lastUpdateUnix, createdAtUnix int64) *Device {

	device := &Device{
		MAC:           mac,
		State:         DeviceState(stateInt),
		Error404Count: error404Count,
		FlashComplete: flashCompleteInt == 1,
		FlashImg:      flashImgInt == 1,
		LastUpdate:    time.Unix(lastUpdateUnix, 0),
		CreatedAt:     time.Unix(createdAtUnix, 0),
	}

	if blockStartUnix.Valid {
		t := time.Unix(blockStartUnix.Int64, 0)
		device.BlockStart = &t
	}
	if lastError.Valid {
		device.LastError = &lastError.String
	}
	if lastErrorTimeUnix.Valid {
		t := time.Unix(lastErrorTimeUnix.Int64, 0)
		device.LastErrorTime = &t
	}

	return device
}

func timeToUnix(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Unix()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
