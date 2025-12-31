package state

import "minilinux-server/internal/storage"

// Re-export storage types for convenience
type DeviceState = storage.DeviceState

const (
	Allowed           = storage.StateAllowed
	BlockedMonitoring = storage.StateBlockedMonitoring
	BlockedPermanent  = storage.StateBlockedPermanent
)

// CanTransitionTo validates if a state transition is allowed
func CanTransitionTo(from, to DeviceState) bool {
	switch from {
	case Allowed:
		// From Allowed, can only go to BlockedMonitoring (after successful flash)
		return to == BlockedMonitoring
	case BlockedMonitoring:
		// From BlockedMonitoring, can go to:
		// - BlockedPermanent (SD boot success confirmed)
		// - Allowed (SD boot failure detected)
		return to == BlockedPermanent || to == Allowed
	case BlockedPermanent:
		// From BlockedPermanent, can go to:
		// - BlockedMonitoring (new 404 detected, possible SD failure)
		return to == BlockedMonitoring
	default:
		return false
	}
}

// ErrorCode represents installation error codes from client
type ErrorCode string

const (
	ErrorSuccess          ErrorCode = "success"
	ErrorSignatureInvalid ErrorCode = "signature_invalid"
	ErrorDownloadFailed   ErrorCode = "download_failed"
	ErrorHashMismatch     ErrorCode = "hash_mismatch"
	ErrorDecompressFailed ErrorCode = "decompress_failed"
	ErrorWriteFailed      ErrorCode = "write_failed"
	ErrorNetworkError     ErrorCode = "network_error"
	ErrorUnknown          ErrorCode = "unknown"
)

// Description returns a human-readable description of the error
func (e ErrorCode) Description() string {
	switch e {
	case ErrorSuccess:
		return "Installation successful"
	case ErrorSignatureInvalid:
		return "RSA signature verification failed"
	case ErrorDownloadFailed:
		return "Image download failed"
	case ErrorHashMismatch:
		return "Hash calculation failed"
	case ErrorDecompressFailed:
		return "XZ decompression failed"
	case ErrorWriteFailed:
		return "DD write to device failed"
	case ErrorNetworkError:
		return "Network connectivity issue"
	case ErrorUnknown:
		return "Unknown error"
	default:
		return "Unknown error: " + string(e)
	}
}
