package state

import (
	"context"
	"log/slog"
	"time"

	"minilinux-server/internal/storage"
)

// Machine handles device state transitions
type Machine struct {
	store            storage.Storage
	monitoringWindow time.Duration
	failureThreshold int
	logger           *slog.Logger
}

// NewMachine creates a new state machine
func NewMachine(store storage.Storage, monitoringWindow time.Duration, failureThreshold int, logger *slog.Logger) *Machine {
	return &Machine{
		store:            store,
		monitoringWindow: monitoringWindow,
		failureThreshold: failureThreshold,
		logger:           logger,
	}
}

// TransitionResult holds the result of a state transition
type TransitionResult struct {
	OldState     DeviceState
	NewState     DeviceState
	Transitioned bool
	Message      string
}

// Handle404Request processes a 404 request and updates state machine
// Returns true if the state changed
func (m *Machine) Handle404Request(ctx context.Context, mac string) (*TransitionResult, error) {
	device, _, err := m.store.GetOrCreateDevice(ctx, mac)
	if err != nil {
		return nil, err
	}

	result := &TransitionResult{
		OldState: device.State,
		NewState: device.State,
	}

	currentTime := time.Now()

	switch device.State {
	case Allowed:
		// Unexpected 404 in ALLOWED state
		m.logger.Warn("404 request in ALLOWED state (unexpected)",
			"mac", mac,
			"state", device.State.String())
		result.Message = "Unexpected 404 in ALLOWED state"
		return result, nil

	case BlockedMonitoring:
		return m.handleBlockedMonitoring(ctx, device, currentTime, result)

	case BlockedPermanent:
		return m.handleBlockedPermanent(ctx, device, currentTime, result)
	}

	return result, nil
}

// handleBlockedMonitoring handles 404 in monitoring state
func (m *Machine) handleBlockedMonitoring(ctx context.Context, device *storage.Device, currentTime time.Time, result *TransitionResult) (*TransitionResult, error) {
	mac := device.MAC

	// Check if monitoring window expired (>5min since block_start)
	// If so, reset the window - this is a normal SD boot requesting boot.sig
	if device.BlockStart != nil {
		elapsed := currentTime.Sub(*device.BlockStart)
		if elapsed >= m.monitoringWindow {
			m.logger.Info("monitoring window expired - resetting",
				"mac", mac,
				"elapsed", elapsed,
				"window", m.monitoringWindow)

			// Cleanup old 404 events for this MAC (on-request cleanup)
			windowStart := currentTime.Add(-m.monitoringWindow)
			m.store.Cleanup404EventsForMAC(ctx, mac, windowStart)

			// Reset the monitoring window
			device.BlockStart = &currentTime
			device.Error404Count = 0
		}
	}

	// Add 404 event
	if err := m.store.Add404Event(ctx, mac, currentTime); err != nil {
		return nil, err
	}

	// Count 404s in monitoring window
	windowStart := currentTime.Add(-m.monitoringWindow)
	count, err := m.store.Count404InWindow(ctx, mac, windowStart)
	if err != nil {
		return nil, err
	}

	device.Error404Count = count

	m.logger.Info("404 event recorded",
		"mac", mac,
		"count", count,
		"threshold", m.failureThreshold,
		"state", device.State.String())

	// Check if failure threshold reached (3x 404 in <5min = SD failure)
	if count >= m.failureThreshold {
		m.logSeparator()
		m.logger.Error("SD boot failure detected (watchdog loop)",
			"mac", mac,
			"count", count,
			"window", m.monitoringWindow)
		m.logger.Info("Transition: BLOCKED_MONITORING -> ALLOWED",
			"mac", mac,
			"reason", "will reflash on next boot")
		m.logSeparator()

		// Transition to ALLOWED for re-flash
		device.State = Allowed
		device.BlockStart = nil
		device.Error404Count = 0
		device.FlashComplete = false

		// Cleanup 404 events for this MAC
		m.store.Cleanup404EventsForMAC(ctx, mac, currentTime)

		if err := m.store.UpdateDevice(ctx, device); err != nil {
			return nil, err
		}

		result.NewState = Allowed
		result.Transitioned = true
		result.Message = "SD boot failure detected - will reflash"
		return result, nil
	}

	// Just update the device
	if err := m.store.UpdateDevice(ctx, device); err != nil {
		return nil, err
	}

	result.Message = "Monitoring 404 recorded"
	return result, nil
}

// handleBlockedPermanent handles 404 in permanent blocked state
func (m *Machine) handleBlockedPermanent(ctx context.Context, device *storage.Device, currentTime time.Time, result *TransitionResult) (*TransitionResult, error) {
	mac := device.MAC

	m.logSeparator()
	m.logger.Warn("New 404 after stable period - possible SD failure",
		"mac", mac,
		"state", device.State.String())
	m.logger.Info("Transition: BLOCKED_PERMANENT -> BLOCKED_MONITORING",
		"mac", mac,
		"reason", "restarting monitoring window")
	m.logSeparator()

	// Add 404 event
	if err := m.store.Add404Event(ctx, mac, currentTime); err != nil {
		return nil, err
	}

	// Transition to BLOCKED_MONITORING
	device.State = BlockedMonitoring
	device.BlockStart = &currentTime
	device.Error404Count = 1

	if err := m.store.UpdateDevice(ctx, device); err != nil {
		return nil, err
	}

	result.NewState = BlockedMonitoring
	result.Transitioned = true
	result.Message = "Possible SD failure - monitoring restarted"
	return result, nil
}

// ConfirmSuccess handles successful installation confirmation
func (m *Machine) ConfirmSuccess(ctx context.Context, mac string) error {
	device, _, err := m.store.GetOrCreateDevice(ctx, mac)
	if err != nil {
		return err
	}

	m.logSeparator()
	m.logger.Info("Installation SUCCESS - signature verified",
		"mac", mac,
		"event", "CONFIRM")
	m.logger.Info("Transition: ALLOWED -> BLOCKED_MONITORING",
		"mac", mac,
		"window", m.monitoringWindow)
	m.logSeparator()

	now := time.Now()
	device.State = BlockedMonitoring
	device.BlockStart = &now
	device.Error404Count = 0
	device.FlashComplete = true
	device.FlashImg = false

	return m.store.UpdateDevice(ctx, device)
}

// ConfirmError handles failed installation confirmation
func (m *Machine) ConfirmError(ctx context.Context, mac string, errorCode ErrorCode) error {
	device, _, err := m.store.GetOrCreateDevice(ctx, mac)
	if err != nil {
		return err
	}

	m.logSeparator()
	m.logger.Error("Installation FAILED",
		"mac", mac,
		"event", "CONFIRM",
		"code", string(errorCode),
		"description", errorCode.Description())
	m.logger.Info("Device stays in ALLOWED state for retry",
		"mac", mac)
	m.logSeparator()

	now := time.Now()
	errStr := string(errorCode)
	device.LastError = &errStr
	device.LastErrorTime = &now
	device.FlashComplete = false

	return m.store.UpdateDevice(ctx, device)
}

// ShouldServeBootFile determines if boot files should be served
// Returns (shouldServe, reason)
func (m *Machine) ShouldServeBootFile(ctx context.Context, mac string) (bool, string, error) {
	device, created, err := m.store.GetOrCreateDevice(ctx, mac)
	if err != nil {
		return false, "", err
	}

	if created {
		m.logger.Info("New device registered",
			"mac", mac,
			"state", "allowed",
			"event", "NEW")
	}

	// flash_img flag overrides blocking
	if device.FlashImg {
		return true, "flash_img override", nil
	}

	// Check if blocked
	if device.State.IsBlocked() {
		return false, device.State.String(), nil
	}

	return true, "allowed", nil
}

// MonitoringWindow returns the configured monitoring window duration.
func (m *Machine) MonitoringWindow() time.Duration { return m.monitoringWindow }

// FailureThreshold returns the number of 404s (reboots) that triggers a reflash.
func (m *Machine) FailureThreshold() int { return m.failureThreshold }

// ForceReflash sets the flash_img override so the next boot serves boot files
// again regardless of the current blocked state (manual "reflash" action).
func (m *Machine) ForceReflash(ctx context.Context, mac string) error {
	device, _, err := m.store.GetOrCreateDevice(ctx, mac)
	if err != nil {
		return err
	}
	m.logger.Warn("MANUAL action: force reflash", "mac", mac, "event", "MANUAL_REFLASH")
	device.State = Allowed
	device.FlashImg = true
	device.FlashComplete = false
	device.BlockStart = nil
	device.Error404Count = 0
	if _, err := m.store.Cleanup404EventsForMAC(ctx, mac, time.Now().Add(time.Hour)); err != nil {
		m.logger.Error("force reflash: cleanup 404 failed", "mac", mac, "error", err)
	}
	return m.store.UpdateDevice(ctx, device)
}

// ForceBlock moves a device to BLOCKED_MONITORING immediately (manual "passer en
// box" action: stop serving boot files, let it run from SD).
func (m *Machine) ForceBlock(ctx context.Context, mac string) error {
	device, _, err := m.store.GetOrCreateDevice(ctx, mac)
	if err != nil {
		return err
	}
	m.logger.Warn("MANUAL action: force block (box mode)", "mac", mac, "event", "MANUAL_BLOCK")
	now := time.Now()
	device.State = BlockedMonitoring
	device.FlashImg = false
	device.BlockStart = &now
	device.Error404Count = 0
	return m.store.UpdateDevice(ctx, device)
}

// Reset clears a device back to a clean ALLOWED state (manual reset).
func (m *Machine) Reset(ctx context.Context, mac string) error {
	device, _, err := m.store.GetOrCreateDevice(ctx, mac)
	if err != nil {
		return err
	}
	m.logger.Warn("MANUAL action: reset device", "mac", mac, "event", "MANUAL_RESET")
	device.State = Allowed
	device.FlashImg = false
	device.FlashComplete = false
	device.BlockStart = nil
	device.Error404Count = 0
	device.LastError = nil
	device.LastErrorTime = nil
	return m.store.UpdateDevice(ctx, device)
}

// logSeparator prints a visual separator
func (m *Machine) logSeparator() {
	m.logger.Info("----------------------------------------------------------------------")
}
