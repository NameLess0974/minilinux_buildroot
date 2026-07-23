package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"minilinux-server/internal/storage"
)

// maxLogBytes caps the size of a log blob we accept (protects the DB / memory).
const maxLogBytes = 4 << 20 // 4 MB

// eventPayload mirrors the JSON body sent to POST /api/v1/events.
type eventPayload struct {
	MAC      string          `json:"mac"`
	BootID   string          `json:"boot_id"`
	TS       int64           `json:"ts"`
	Step     string          `json:"step"`
	Status   string          `json:"status"`
	Progress int             `json:"progress"`
	Attempt  int             `json:"attempt"`
	Message  string          `json:"message"`
	Details  json.RawMessage `json:"details"`
}

// HandleEvent ingests a single progress/error event. Best-effort by contract:
// the client ignores the body, so on any client-side problem we still return 200
// with {"ok":true} to avoid perturbing the install, and only 4xx on truly
// unusable input (missing identity / malformed JSON).
func (h *Handlers) HandleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "POST only"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "read error"})
		return
	}

	var p eventPayload
	if err := json.Unmarshal(body, &p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
		return
	}

	mac := strings.ToUpper(strings.TrimSpace(p.MAC))
	clientIP := getClientIP(r)

	// Fall back to ARP if the client omitted its MAC.
	if mac == "" {
		mac = h.arpCache.Lookup(clientIP)
	}
	if mac == "" || mac == "UNKNOWN" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "mac required"})
		return
	}

	// Security: only whitelisted machines may write telemetry (avoids DB spam from
	// any host on the network). Rejected quietly with 403.
	if !h.whitelist.Contains(mac) {
		h.logger.Warn("telemetry: MAC not in whitelist", "mac", mac, "ip", clientIP)
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not authorized"})
		return
	}

	bootID := strings.TrimSpace(p.BootID)
	if bootID == "" {
		bootID = mac
	}

	// error_code is nested in details; hoist it into its own column for indexing.
	errorCode := extractErrorCode(p.Details)

	details := "{}"
	if len(p.Details) > 0 {
		details = string(p.Details)
	}

	e := &storage.TelemetryEvent{
		BootID:     bootID,
		MAC:        mac,
		IP:         clientIP,
		ClientTS:   p.TS,
		ReceivedAt: time.Now(), // server clock: the Pi clock is wrong at early boot
		Step:       p.Step,
		Status:     p.Status,
		Progress:   clampProgress(p.Progress),
		Attempt:    p.Attempt,
		Message:    p.Message,
		ErrorCode:  errorCode,
		Details:    details,
	}

	if err := h.store.AddTelemetryEvent(r.Context(), e); err != nil {
		h.logger.Error("telemetry: failed to store event", "boot_id", bootID, "mac", mac, "error", err)
		// Return 200 anyway: telemetry is best-effort and must never stall the install.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	h.logger.Info("telemetry event",
		"mac", mac, "ip", clientIP, "boot_id", bootID,
		"step", p.Step, "status", p.Status, "progress", e.Progress,
		"attempt", p.Attempt, "error_code", errorCode)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleLogs ingests the full log blob (text/plain) sent once near end of run.
// Identity comes from X-Machine-MAC / X-Boot-Id headers (per spec).
func (h *Handlers) HandleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "POST only"})
		return
	}

	clientIP := getClientIP(r)
	mac := strings.ToUpper(strings.TrimSpace(r.Header.Get("X-Machine-MAC")))
	if mac == "" {
		mac = h.arpCache.Lookup(clientIP)
	}
	if mac == "" || mac == "UNKNOWN" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "mac required"})
		return
	}
	if !h.whitelist.Contains(mac) {
		h.logger.Warn("telemetry logs: MAC not in whitelist", "mac", mac, "ip", clientIP)
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not authorized"})
		return
	}

	bootID := strings.TrimSpace(r.Header.Get("X-Boot-Id"))
	if bootID == "" {
		bootID = mac
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxLogBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "read error"})
		return
	}

	if err := h.store.SaveLogs(r.Context(), bootID, mac, clientIP, string(body)); err != nil {
		h.logger.Error("telemetry: failed to store logs", "boot_id", bootID, "mac", mac, "error", err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	h.logger.Info("telemetry logs stored", "mac", mac, "ip", clientIP, "boot_id", bootID, "bytes", len(body))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- Read API for the dashboard ---

// HandleAPISessions returns the aggregated list of install sessions (one per boot_id).
func (h *Handlers) HandleAPISessions(w http.ResponseWriter, r *http.Request) {
	if !h.RequireAdmin(w, r) {
		return
	}
	sessions, err := h.store.ListSessions(r.Context(), 500)
	if err != nil {
		h.logger.Error("telemetry: list sessions failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal error"})
		return
	}
	if sessions == nil {
		sessions = []*storage.TelemetrySession{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"server_now": time.Now().Unix(),
		"sessions":   sessions,
	})
}

// HandleAPISessionDetail returns the full event timeline for one boot_id.
// Path: /api/v1/sessions/{boot_id}
func (h *Handlers) HandleAPISessionDetail(w http.ResponseWriter, r *http.Request) {
	if !h.RequireAdmin(w, r) {
		return
	}
	bootID := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	bootID = strings.Trim(bootID, "/")
	if bootID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "boot_id required"})
		return
	}

	events, err := h.store.ListEventsForBoot(r.Context(), bootID)
	if err != nil {
		h.logger.Error("telemetry: list events failed", "boot_id", bootID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal error"})
		return
	}

	// Shape events for JSON (details is raw JSON, embed it as-is).
	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		out = append(out, map[string]any{
			"received_at": e.ReceivedAt.Unix(),
			"client_ts":   e.ClientTS,
			"step":        e.Step,
			"status":      e.Status,
			"progress":    e.Progress,
			"attempt":     e.Attempt,
			"message":     e.Message,
			"error_code":  e.ErrorCode,
			"details":     json.RawMessage(e.Details),
		})
	}

	_, hasLogs, _ := h.store.GetLogs(r.Context(), bootID)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"boot_id":  bootID,
		"has_logs": hasLogs,
		"events":   out,
	})
}

// HandleAPILogs returns the raw stored log blob for a boot_id.
// Path: /api/v1/logs/{boot_id}
func (h *Handlers) HandleAPILogs(w http.ResponseWriter, r *http.Request) {
	if !h.RequireAdmin(w, r) {
		return
	}
	bootID := strings.TrimPrefix(r.URL.Path, "/api/v1/logs/")
	bootID = strings.Trim(bootID, "/")
	if bootID == "" {
		http.Error(w, "boot_id required", http.StatusBadRequest)
		return
	}

	body, ok, err := h.store.GetLogs(r.Context(), bootID)
	if err != nil {
		h.logger.Error("telemetry: get logs failed", "boot_id", bootID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no logs for this boot_id", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, body)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func clampProgress(p int) int {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func extractErrorCode(details json.RawMessage) string {
	if len(details) == 0 {
		return ""
	}
	var d struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(details, &d); err != nil {
		return ""
	}
	return d.ErrorCode
}
