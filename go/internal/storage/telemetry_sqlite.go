package storage

import (
	"context"
	"database/sql"
	"time"
)

// AddTelemetryEvent inserts a single install event. ReceivedAt is expected to be
// set by the caller (server time) since the client clock is unreliable at boot.
func (s *SQLiteStorage) AddTelemetryEvent(ctx context.Context, e *TelemetryEvent) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telemetry_events
			(boot_id, mac, ip, client_ts, received_at, step, status, progress, attempt, message, error_code, details)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		e.BootID, e.MAC, e.IP, e.ClientTS, e.ReceivedAt.Unix(),
		e.Step, e.Status, e.Progress, e.Attempt, e.Message, e.ErrorCode, e.Details,
	)
	return err
}

// ListSessions returns one aggregated row per boot_id, newest activity first.
// The "latest" per-session fields (step, status, progress...) come from the most
// recently received event, ordered by received_at then id to break ties.
func (s *SQLiteStorage) ListSessions(ctx context.Context, limit int) ([]*TelemetrySession, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			t.boot_id,
			t.mac,
			(SELECT ip FROM telemetry_events e WHERE e.boot_id = t.boot_id AND e.ip != '' ORDER BY e.received_at DESC, e.id DESC LIMIT 1) AS ip,
			last.step, last.status,
			MAX(t.progress) AS progress,
			last.attempt, last.message, last.error_code,
			COUNT(*) AS event_count,
			MIN(t.received_at) AS first_seen,
			MAX(t.received_at) AS last_seen,
			(SELECT COUNT(*) FROM telemetry_logs l WHERE l.boot_id = t.boot_id) AS has_logs
		FROM telemetry_events t
		JOIN (
			SELECT e.boot_id, e.step, e.status, e.progress, e.attempt, e.message, e.error_code
			FROM telemetry_events e
			JOIN (
				SELECT boot_id, MAX(received_at * 1000000000 + id) AS rk
				FROM telemetry_events GROUP BY boot_id
			) m ON e.boot_id = m.boot_id AND (e.received_at * 1000000000 + e.id) = m.rk
		) last ON last.boot_id = t.boot_id
		GROUP BY t.boot_id
		ORDER BY last_seen DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*TelemetrySession
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// GetSession returns the aggregated view for a single boot_id.
func (s *SQLiteStorage) GetSession(ctx context.Context, bootID string) (*TelemetrySession, error) {
	sessions, err := s.ListSessions(ctx, 0)
	if err != nil {
		return nil, err
	}
	for _, sess := range sessions {
		if sess.BootID == bootID {
			return sess, nil
		}
	}
	return nil, sql.ErrNoRows
}

// ListEventsForBoot returns every event for a session, oldest first (server order).
func (s *SQLiteStorage) ListEventsForBoot(ctx context.Context, bootID string) ([]*TelemetryEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, boot_id, mac, ip, client_ts, received_at, step, status,
		       progress, attempt, message, error_code, details
		FROM telemetry_events
		WHERE boot_id = ?
		ORDER BY received_at ASC, id ASC
	`, bootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*TelemetryEvent
	for rows.Next() {
		var (
			e          TelemetryEvent
			ip, step   sql.NullString
			status     sql.NullString
			msg, ec    sql.NullString
			details    sql.NullString
			receivedAt int64
			clientTS   sql.NullInt64
		)
		if err := rows.Scan(&e.ID, &e.BootID, &e.MAC, &ip, &clientTS, &receivedAt,
			&step, &status, &e.Progress, &e.Attempt, &msg, &ec, &details); err != nil {
			return nil, err
		}
		e.IP = ip.String
		e.ClientTS = clientTS.Int64
		e.ReceivedAt = time.Unix(receivedAt, 0)
		e.Step = step.String
		e.Status = status.String
		e.Message = msg.String
		e.ErrorCode = ec.String
		e.Details = details.String
		events = append(events, &e)
	}
	return events, rows.Err()
}

// SaveLogs upserts the log blob for a boot_id, then keeps only this newest blob
// per MAC (older sessions' logs for the same box are dropped) to avoid unbounded
// accumulation across reboots.
func (s *SQLiteStorage) SaveLogs(ctx context.Context, bootID, mac, ip, body string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO telemetry_logs (boot_id, mac, ip, received_at, body)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(boot_id) DO UPDATE SET
			mac = excluded.mac,
			ip = excluded.ip,
			received_at = excluded.received_at,
			body = excluded.body
	`, bootID, mac, ip, time.Now().Unix(), body); err != nil {
		return err
	}

	// Drop any other log blobs for this MAC (older boot_ids): keep one per box.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM telemetry_logs WHERE mac = ? AND boot_id != ?
	`, mac, bootID); err != nil {
		return err
	}

	return tx.Commit()
}

// GetLogs returns the stored log blob for a boot_id, and whether it exists.
func (s *SQLiteStorage) GetLogs(ctx context.Context, bootID string) (string, bool, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `
		SELECT body FROM telemetry_logs WHERE boot_id = ?
	`, bootID).Scan(&body)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return body, true, nil
}

// PurgeTelemetryBefore removes telemetry events and log blobs received before cutoff.
func (s *SQLiteStorage) PurgeTelemetryBefore(ctx context.Context, cutoff time.Time) (int64, int64, error) {
	cut := cutoff.Unix()

	ev, err := s.db.ExecContext(ctx, `DELETE FROM telemetry_events WHERE received_at < ?`, cut)
	if err != nil {
		return 0, 0, err
	}
	evN, _ := ev.RowsAffected()

	lg, err := s.db.ExecContext(ctx, `DELETE FROM telemetry_logs WHERE received_at < ?`, cut)
	if err != nil {
		return evN, 0, err
	}
	lgN, _ := lg.RowsAffected()

	return evN, lgN, nil
}

func scanSession(rows *sql.Rows) (*TelemetrySession, error) {
	var (
		sess                  TelemetrySession
		ip, step, status      sql.NullString
		msg, ec               sql.NullString
		firstSeen, lastSeen   int64
		hasLogs               int
	)
	if err := rows.Scan(
		&sess.BootID, &sess.MAC, &ip, &step, &status, &sess.Progress, &sess.Attempt,
		&msg, &ec, &sess.EventCount, &firstSeen, &lastSeen, &hasLogs,
	); err != nil {
		return nil, err
	}
	sess.IP = ip.String
	sess.LastStep = step.String
	sess.LastStatus = status.String
	sess.LastMessage = msg.String
	sess.ErrorCode = ec.String
	sess.FirstSeen = time.Unix(firstSeen, 0)
	sess.LastSeen = time.Unix(lastSeen, 0)
	sess.HasLogs = hasLogs > 0
	return &sess, nil
}
