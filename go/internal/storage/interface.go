package storage

import (
	"context"
	"time"
)

// DeviceState represents the state of a device in the state machine
type DeviceState int

const (
	// StateAllowed - Device can download boot files
	StateAllowed DeviceState = iota
	// StateBlockedMonitoring - Post-flash, monitoring for SD boot success/failure
	StateBlockedMonitoring
	// StateBlockedPermanent - SD boot confirmed successful, permanently blocked
	StateBlockedPermanent
)

// String returns a human-readable state name
func (s DeviceState) String() string {
	switch s {
	case StateAllowed:
		return "allowed"
	case StateBlockedMonitoring:
		return "blocked_monitoring"
	case StateBlockedPermanent:
		return "blocked_permanent"
	default:
		return "unknown"
	}
}

// IsBlocked returns true if the device should receive 404 for boot files
func (s DeviceState) IsBlocked() bool {
	return s == StateBlockedMonitoring || s == StateBlockedPermanent
}

// Device represents a device record in storage
type Device struct {
	MAC           string
	State         DeviceState
	BlockStart    *time.Time
	Error404Count int
	FlashComplete bool
	FlashImg      bool
	LastError     *string
	LastErrorTime *time.Time
	LastUpdate    time.Time
	CreatedAt     time.Time
}

// Storage defines the interface for device storage operations
type Storage interface {
	// Device operations
	GetDevice(ctx context.Context, mac string) (*Device, error)
	GetOrCreateDevice(ctx context.Context, mac string) (*Device, bool, error) // returns (device, created, error)
	UpdateDevice(ctx context.Context, device *Device) error
	ListDevices(ctx context.Context) ([]*Device, error)

	// 404 Event operations
	Add404Event(ctx context.Context, mac string, ts time.Time) error
	Count404InWindow(ctx context.Context, mac string, windowStart time.Time) (int, error)
	Get404Timestamps(ctx context.Context, mac string, windowStart time.Time) ([]time.Time, error)
	Cleanup404Events(ctx context.Context, before time.Time) (int64, error)
	Cleanup404EventsForMAC(ctx context.Context, mac string, cutoff time.Time) (int64, error)

	// Lifecycle
	Close() error
}

// NewDevice creates a new device with default values
func NewDevice(mac string) *Device {
	now := time.Now()
	return &Device{
		MAC:           mac,
		State:         StateAllowed,
		BlockStart:    nil,
		Error404Count: 0,
		FlashComplete: false,
		FlashImg:      false,
		LastError:     nil,
		LastErrorTime: nil,
		LastUpdate:    now,
		CreatedAt:     now,
	}
}
