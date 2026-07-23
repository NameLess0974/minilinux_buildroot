package storage

import (
	"context"
	"time"
)

// TelemetryEvent is a single progress/error event reported by a client during install.
// The client clock is unreliable at early boot, so ReceivedAt (set server-side) is the
// source of truth for ordering; ClientTS is kept only for internal delta computation.
type TelemetryEvent struct {
	ID         int64
	BootID     string // MAC-<boot_timestamp>, groups one install session
	MAC        string // stable machine identity
	IP         string // best-effort, for human identification only
	ClientTS   int64  // unix ts from client (may be wrong)
	ReceivedAt time.Time
	Step       string
	Status     string // start / ok / error / retry
	Progress   int    // 0-100
	Attempt    int
	Message    string
	ErrorCode  string // set when Status == "error"
	Details    string // raw JSON blob, stored as-is
}

// TelemetrySession is an aggregated view of one boot_id, built from its events.
type TelemetrySession struct {
	BootID       string    `json:"boot_id"`
	MAC          string    `json:"mac"`
	IP           string    `json:"ip"`
	LastStep     string    `json:"last_step"`
	LastStatus   string    `json:"last_status"`
	Progress     int       `json:"progress"`
	Attempt      int       `json:"attempt"`
	LastMessage  string    `json:"last_message"`
	ErrorCode    string    `json:"error_code"`
	EventCount   int       `json:"event_count"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	HasLogs      bool      `json:"has_logs"`
}

// TelemetryStorage defines telemetry persistence, separate from the device state machine.
type TelemetryStorage interface {
	AddTelemetryEvent(ctx context.Context, e *TelemetryEvent) error
	ListSessions(ctx context.Context, limit int) ([]*TelemetrySession, error)
	GetSession(ctx context.Context, bootID string) (*TelemetrySession, error)
	ListEventsForBoot(ctx context.Context, bootID string) ([]*TelemetryEvent, error)

	SaveLogs(ctx context.Context, bootID, mac, ip, body string) error
	GetLogs(ctx context.Context, bootID string) (string, bool, error)

	// PurgeTelemetryBefore deletes events and log blobs older than cutoff.
	// Returns (eventsDeleted, logsDeleted, error).
	PurgeTelemetryBefore(ctx context.Context, cutoff time.Time) (int64, int64, error)
}
