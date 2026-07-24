package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresStorage : base partagee avec le middleware. Les tables boot_*
// appartiennent a ce serveur ; box est lue seule (voir whitelist/).
type PostgresStorage struct {
	db *sql.DB
}

// NewPostgres ouvre la connexion et verifie qu'elle repond.
func NewPostgres(dsn string) (*PostgresStorage, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	// Pool modeste : requetes courtes, base partagee avec le middleware.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return &PostgresStorage{db: db}, nil
}

// DB expose le pool pour la whitelist, qui lit la table box.
func (s *PostgresStorage) DB() *sql.DB { return s.db }

func (s *PostgresStorage) Close() error { return s.db.Close() }

// --- Devices ---

const deviceColumns = `mac, state, block_start, error_404_count, flash_complete,
	flash_img, last_error, last_error_time, last_update, created_at`

// normMAC aligne la casse sur celle de box (minuscules) : ARP renvoie des
// majuscules, et une jointure box <-> boot_* ne remonterait alors rien.
func normMAC(mac string) string {
	return strings.ToLower(strings.TrimSpace(mac))
}

func scanPgDevice(sc interface{ Scan(...any) error }) (*Device, error) {
	var (
		d          Device
		state      int
		blockStart sql.NullTime
		lastError  sql.NullString
		lastErrTS  sql.NullTime
	)
	err := sc.Scan(&d.MAC, &state, &blockStart, &d.Error404Count, &d.FlashComplete,
		&d.FlashImg, &lastError, &lastErrTS, &d.LastUpdate, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	d.State = DeviceState(state)
	if blockStart.Valid {
		d.BlockStart = &blockStart.Time
	}
	if lastError.Valid {
		d.LastError = &lastError.String
	}
	if lastErrTS.Valid {
		d.LastErrorTime = &lastErrTS.Time
	}
	return &d, nil
}

func (s *PostgresStorage) GetDevice(ctx context.Context, mac string) (*Device, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+deviceColumns+` FROM boot_devices WHERE mac = $1`, normMAC(mac))
	return scanPgDevice(row)
}

// GetOrCreateDevice insere si absent, de facon atomique (ON CONFLICT).
func (s *PostgresStorage) GetOrCreateDevice(ctx context.Context, mac string) (*Device, bool, error) {
	d := NewDevice(normMAC(mac))

	row := s.db.QueryRowContext(ctx, `
		INSERT INTO boot_devices (mac, state, error_404_count, flash_complete, flash_img,
		                          last_update, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (mac) DO NOTHING
		RETURNING `+deviceColumns,
		d.MAC, int(d.State), d.Error404Count, d.FlashComplete, d.FlashImg,
		d.LastUpdate, d.CreatedAt)

	created, err := scanPgDevice(row)
	if err == nil {
		return created, true, nil
	}
	if err != sql.ErrNoRows {
		return nil, false, err
	}

	// Deja present : ON CONFLICT DO NOTHING ne renvoie aucune ligne.
	existing, err := s.GetDevice(ctx, mac)
	if err != nil {
		return nil, false, err
	}
	return existing, false, nil
}

func (s *PostgresStorage) UpdateDevice(ctx context.Context, device *Device) error {
	device.LastUpdate = time.Now()
	_, err := s.db.ExecContext(ctx, `
		UPDATE boot_devices SET
			state = $1, block_start = $2, error_404_count = $3, flash_complete = $4,
			flash_img = $5, last_error = $6, last_error_time = $7, last_update = $8
		WHERE mac = $9`,
		int(device.State), device.BlockStart, device.Error404Count, device.FlashComplete,
		device.FlashImg, device.LastError, device.LastErrorTime, device.LastUpdate,
		normMAC(device.MAC))
	return err
}

func (s *PostgresStorage) ListDevices(ctx context.Context) ([]*Device, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+deviceColumns+` FROM boot_devices ORDER BY mac`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []*Device
	for rows.Next() {
		d, err := scanPgDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

// --- Evenements 404 (un 404 sur boot.sig = un reboot reseau) ---

func (s *PostgresStorage) Add404Event(ctx context.Context, mac string, ts time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO boot_404_events (mac, timestamp) VALUES ($1, $2)`, normMAC(mac), ts)
	return err
}

func (s *PostgresStorage) Count404InWindow(ctx context.Context, mac string, windowStart time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM boot_404_events WHERE mac = $1 AND timestamp >= $2`,
		normMAC(mac), windowStart).Scan(&count)
	return count, err
}

func (s *PostgresStorage) Get404Timestamps(ctx context.Context, mac string, windowStart time.Time) ([]time.Time, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT timestamp FROM boot_404_events
		 WHERE mac = $1 AND timestamp >= $2 ORDER BY timestamp`,
		normMAC(mac), windowStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []time.Time
	for rows.Next() {
		var ts time.Time
		if err := rows.Scan(&ts); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}

func (s *PostgresStorage) Cleanup404Events(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM boot_404_events WHERE timestamp < $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *PostgresStorage) Cleanup404EventsForMAC(ctx context.Context, mac string, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM boot_404_events WHERE mac = $1 AND timestamp < $2`, normMAC(mac), cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
