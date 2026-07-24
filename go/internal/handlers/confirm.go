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
	clientIP := h.clientIP(r)
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

	// Le MAC vient de l'URL : sans ce controle, n'importe qui joignant le
	// listener public pilotait la state machine de n'importe quelle box.
	if !h.whitelist.Contains(mac) {
		h.logger.Warn("confirm denied - MAC not whitelisted", "mac", mac, "ip", clientIP)
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Un MAC whiteliste est devinable : on exige aussi la coherence ARP.
	if match, verified := h.macMatchesPeer(mac, clientIP); !match {
		h.logger.Warn("confirm denied - MAC does not match peer",
			"mac_claimed", mac, "mac_arp", clientMAC, "ip", clientIP)
		http.Error(w, "Not found", http.StatusNotFound)
		return
	} else if !verified {
		h.logger.Info("confirm accepted with unverified MAC (no ARP entry)",
			"mac", mac, "ip", clientIP)
	}

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
