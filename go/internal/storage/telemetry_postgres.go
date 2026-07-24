package storage

import (
	"context"
	"database/sql"
	"time"
)

func (s *PostgresStorage) AddTelemetryEvent(ctx context.Context, e *TelemetryEvent) error {
	// details est du jsonb : une chaine vide n'est pas du JSON valide.
	var details any
	if e.Details != "" {
		details = e.Details
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO boot_telemetry_events
			(boot_id, mac, ip, client_ts, received_at, step, status, progress,
			 attempt, message, error_code, details)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		e.BootID, normMAC(e.MAC), e.IP, e.ClientTS, e.ReceivedAt,
		e.Step, e.Status, e.Progress, e.Attempt, e.Message, e.ErrorCode, details)
	return err
}

// ListSessions agrege une ligne par boot_id, activite recente d'abord.
// DISTINCT ON donne le dernier evenement de chaque session.
func (s *PostgresStorage) ListSessions(ctx context.Context, limit int) ([]*TelemetrySession, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.db.QueryContext(ctx, `
		WITH last AS (
			SELECT DISTINCT ON (boot_id)
			       boot_id, step, status, attempt, message, error_code, ip
			FROM boot_telemetry_events
			ORDER BY boot_id, received_at DESC, id DESC
		),
		agg AS (
			SELECT boot_id,
			       MAX(mac) AS mac,
			       MAX(progress) AS progress,
			       COUNT(*) AS event_count,
			       MIN(received_at) AS first_seen,
			       MAX(received_at) AS last_seen
			FROM boot_telemetry_events
			GROUP BY boot_id
		)
		SELECT a.boot_id, a.mac,
		       COALESCE(NULLIF(l.ip, ''), '') AS ip,
		       l.step, l.status, a.progress, l.attempt, l.message, l.error_code,
		       a.event_count, a.first_seen, a.last_seen,
		       EXISTS (SELECT 1 FROM boot_telemetry_logs g WHERE g.boot_id = a.boot_id) AS has_logs
		FROM agg a
		JOIN last l ON l.boot_id = a.boot_id
		ORDER BY a.last_seen DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*TelemetrySession
	for rows.Next() {
		var (
			sess             TelemetrySession
			ip, step, status sql.NullString
			msg, ec          sql.NullString
		)
		if err := rows.Scan(
			&sess.BootID, &sess.MAC, &ip, &step, &status, &sess.Progress, &sess.Attempt,
			&msg, &ec, &sess.EventCount, &sess.FirstSeen, &sess.LastSeen, &sess.HasLogs,
		); err != nil {
			return nil, err
		}
		sess.IP = ip.String
		sess.LastStep = step.String
		sess.LastStatus = status.String
		sess.LastMessage = msg.String
		sess.ErrorCode = ec.String
		sessions = append(sessions, &sess)
	}
	return sessions, rows.Err()
}

func (s *PostgresStorage) GetSession(ctx context.Context, bootID string) (*TelemetrySession, error) {
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

func (s *PostgresStorage) ListEventsForBoot(ctx context.Context, bootID string) ([]*TelemetryEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, boot_id, mac, ip, client_ts, received_at, step, status,
		       progress, attempt, message, error_code, details
		FROM boot_telemetry_events
		WHERE boot_id = $1
		ORDER BY received_at ASC, id ASC`, bootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*TelemetryEvent
	for rows.Next() {
		var (
			e                TelemetryEvent
			ip, step, status sql.NullString
			msg, ec, details sql.NullString
			clientTS         sql.NullInt64
		)
		if err := rows.Scan(&e.ID, &e.BootID, &e.MAC, &ip, &clientTS, &e.ReceivedAt,
			&step, &status, &e.Progress, &e.Attempt, &msg, &ec, &details); err != nil {
			return nil, err
		}
		e.IP = ip.String
		e.ClientTS = clientTS.Int64
		e.Step = step.String
		e.Status = status.String
		e.Message = msg.String
		e.ErrorCode = ec.String
		e.Details = details.String
		events = append(events, &e)
	}
	return events, rows.Err()
}

// SaveLogs ne garde qu'un blob par MAC, sinon ils s'accumulent a chaque reboot.
func (s *PostgresStorage) SaveLogs(ctx context.Context, bootID, mac, ip, body string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO boot_telemetry_logs (boot_id, mac, ip, received_at, body)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (boot_id) DO UPDATE SET
			mac = EXCLUDED.mac, ip = EXCLUDED.ip,
			received_at = EXCLUDED.received_at, body = EXCLUDED.body`,
		bootID, normMAC(mac), ip, time.Now(), body); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM boot_telemetry_logs WHERE mac = $1 AND boot_id != $2`,
		normMAC(mac), bootID); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *PostgresStorage) GetLogs(ctx context.Context, bootID string) (string, bool, error) {
	var body string
	err := s.db.QueryRowContext(ctx,
		`SELECT body FROM boot_telemetry_logs WHERE boot_id = $1`, bootID).Scan(&body)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return body, true, nil
}

func (s *PostgresStorage) PurgeTelemetryBefore(ctx context.Context, cutoff time.Time) (int64, int64, error) {
	ev, err := s.db.ExecContext(ctx,
		`DELETE FROM boot_telemetry_events WHERE received_at < $1`, cutoff)
	if err != nil {
		return 0, 0, err
	}
	evN, _ := ev.RowsAffected()

	lg, err := s.db.ExecContext(ctx,
		`DELETE FROM boot_telemetry_logs WHERE received_at < $1`, cutoff)
	if err != nil {
		return evN, 0, err
	}
	lgN, _ := lg.RowsAffected()

	return evN, lgN, nil
}
