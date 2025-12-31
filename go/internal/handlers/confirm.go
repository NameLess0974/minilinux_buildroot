package handlers

import (
	"net/http"
	"net/url"
	"strings"

	"minilinux-server/internal/state"
)

// HandleConfirm handles installation confirmation from clients
// URL format: /confirm/<MAC>?status=success or /confirm/<MAC>?status=error&code=signature_invalid
func (h *Handlers) HandleConfirm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clientIP := getClientIP(r)
	clientMAC := h.arpCache.Lookup(clientIP)

	// Parse URL path to get MAC
	path := strings.TrimPrefix(r.URL.Path, h.cfg.ConfirmEndpoint)
	macFromURL := strings.ToUpper(strings.Trim(path, "/"))

	// Parse query parameters
	query := r.URL.Query()
	status := query.Get("status")
	errorCode := query.Get("code")

	// Use MAC from URL if provided, otherwise use ARP lookup
	mac := macFromURL
	if mac == "" {
		mac = clientMAC
	}

	h.logger.Info("confirm request",
		"ip", clientIP,
		"mac_url", macFromURL,
		"mac_arp", clientMAC,
		"status", status,
		"code", errorCode)

	// Validate MAC
	if mac == "" || mac == "UNKNOWN" {
		h.logger.Error("confirm error - unknown MAC",
			"ip", clientIP)
		http.Error(w, "MAC address required", http.StatusBadRequest)
		return
	}

	// Normalize MAC
	mac = strings.ToUpper(mac)

	// Process based on status
	switch status {
	case "success":
		// Send response FIRST (important for client timeout)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK\n"))

		// Then update state (after HTTP response sent)
		if err := h.machine.ConfirmSuccess(ctx, mac); err != nil {
			h.logger.Error("failed to confirm success",
				"mac", mac,
				"error", err)
		}

	case "error":
		// Send response FIRST
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK\n"))

		// Then update state
		code := state.ErrorCode(errorCode)
		if errorCode == "" {
			code = state.ErrorUnknown
		}

		if err := h.machine.ConfirmError(ctx, mac, code); err != nil {
			h.logger.Error("failed to confirm error",
				"mac", mac,
				"code", errorCode,
				"error", err)
		}

	default:
		h.logger.Error("confirm error - invalid status",
			"ip", clientIP,
			"mac", mac,
			"status", status)
		http.Error(w, "Invalid status: "+url.QueryEscape(status), http.StatusBadRequest)
	}
}
