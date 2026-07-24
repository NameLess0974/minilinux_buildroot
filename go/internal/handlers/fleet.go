package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// AdminAuthorized : seul client admin = le backend middleware, qui a deja
// verifie le JWT et le role sabsystem. Ce token prouve juste "c'est le backend".
// SERVICE_TOKEN vide = acces ouvert (deploiement interne). Comparaison en temps
// constant.
func (h *Handlers) AdminAuthorized(r *http.Request) bool {
	if h.cfg.ServiceToken == "" {
		return true
	}
	got := r.Header.Get("X-Service-Token")
	if got == "" {
		// Accepte aussi Authorization: Bearer <token>.
		if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			got = after
		}
	}
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.cfg.ServiceToken)) == 1
}

// RequireAdmin ecrit 401 et renvoie false si l'appelant n'est pas le backend.
// Pas de WWW-Authenticate : API service-a-service, jamais un navigateur.
func (h *Handlers) RequireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if h.AdminAuthorized(r) {
		return true
	}
	h.logger.Warn("admin request rejected: bad or missing service token",
		"path", r.URL.Path, "ip", h.clientIP(r))
	writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
	return false
}

// deviceInfo is the per-box view shown in the fleet dashboard: state-machine data
// (reboot/404 counts, timings) enriched with the latest telemetry session.
type deviceInfo struct {
	MAC             string  `json:"mac"`
	IP              string  `json:"ip"`
	State           string  `json:"state"`
	Whitelisted     bool    `json:"whitelisted"`
	FlashImg        bool    `json:"flash_img"` // reflash forced/pending
	FlashComplete   bool    `json:"flash_complete"`
	RebootCount     int     `json:"reboot_count"`     // 404s in current window = reboots before reflash
	RebootThreshold int     `json:"reboot_threshold"` // reflash triggers at this many
	SecSinceReboot  *int64  `json:"sec_since_reboot"` // since last 404/reboot, nil if none
	AvgRebootGap    *int64  `json:"avg_reboot_gap"`   // avg seconds between reboots in window
	RebootGaps      []int64 `json:"reboot_gaps"`      // seconds between consecutive reboots
	LastError       string  `json:"last_error"`
	SecSinceUpdate  int64   `json:"sec_since_update"`

	// Latest telemetry (may be empty if the box never reported install progress)
	InstallStep     string `json:"install_step"`
	InstallStatus   string `json:"install_status"`
	InstallProgress int    `json:"install_progress"`
	InstallAttempt  int    `json:"install_attempt"`
	InstallMessage  string `json:"install_message"`
	InstallError    string `json:"install_error"`
	BootID          string `json:"boot_id"`
	HasLogs         bool   `json:"has_logs"`
}

// HandleAPIFleet returns the full fleet view: one row per known device, merging
// state-machine data with the most recent telemetry session for that MAC.
func (h *Handlers) HandleAPIFleet(w http.ResponseWriter, r *http.Request) {
	if !h.RequireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	now := time.Now()

	devices, err := h.store.ListDevices(ctx)
	if err != nil {
		h.logger.Error("fleet: list devices failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal error"})
		return
	}

	// Index latest telemetry session per MAC.
	sessions, _ := h.store.ListSessions(ctx, 1000)
	latestByMAC := map[string]int{} // mac -> index into sessions (first = newest, ListSessions is DESC)
	for i, s := range sessions {
		if _, seen := latestByMAC[s.MAC]; !seen {
			latestByMAC[s.MAC] = i
		}
	}

	window := h.machine.MonitoringWindow()
	threshold := h.machine.FailureThreshold()
	windowStart := now.Add(-window)

	out := make([]deviceInfo, 0, len(devices))
	seenMAC := map[string]bool{}
	for _, d := range devices {
		seenMAC[d.MAC] = true
		di := deviceInfo{
			MAC:             d.MAC,
			State:           d.State.String(),
			Whitelisted:     h.whitelist.Contains(d.MAC),
			FlashImg:        d.FlashImg,
			FlashComplete:   d.FlashComplete,
			RebootThreshold: threshold,
			SecSinceUpdate:  int64(now.Sub(d.LastUpdate).Seconds()),
		}
		if d.LastError != nil {
			di.LastError = *d.LastError
		}

		// Reboot timing from 404 events (each network re-boot hits boot.sig -> 404 when blocked).
		ts, err := h.store.Get404Timestamps(ctx, d.MAC, windowStart)
		if err == nil {
			di.RebootCount = len(ts)
			if len(ts) > 0 {
				last := ts[len(ts)-1]
				s := int64(now.Sub(last).Seconds())
				di.SecSinceReboot = &s
			}
			gaps := make([]int64, 0, len(ts))
			for i := 1; i < len(ts); i++ {
				gaps = append(gaps, int64(ts[i].Sub(ts[i-1]).Seconds()))
			}
			di.RebootGaps = gaps
			if len(gaps) > 0 {
				var sum int64
				for _, g := range gaps {
					sum += g
				}
				avg := sum / int64(len(gaps))
				di.AvgRebootGap = &avg
			}
		}

		// IP + latest telemetry enrichment.
		if idx, ok := latestByMAC[d.MAC]; ok {
			s := sessions[idx]
			di.IP = s.IP
			di.InstallStep = s.LastStep
			di.InstallStatus = s.LastStatus
			di.InstallProgress = s.Progress
			di.InstallAttempt = s.Attempt
			di.InstallMessage = s.LastMessage
			di.InstallError = s.ErrorCode
			di.BootID = s.BootID
			di.HasLogs = s.HasLogs
		}
		if di.IP == "" {
			di.IP = h.arpCache.Lookup(d.MAC) // best-effort reverse (usually UNKNOWN)
		}

		out = append(out, di)
	}

	// Include boxes seen only via telemetry (reported install progress but never
	// hit the boot state machine yet, e.g. mid first-flash). Without this they'd be
	// invisible in the fleet view until their next network boot.
	for i, s := range sessions {
		if latestByMAC[s.MAC] != i || seenMAC[s.MAC] {
			continue // not the newest session for this MAC, or already listed as a device
		}
		out = append(out, deviceInfo{
			MAC:             s.MAC,
			IP:              s.IP,
			State:           "unknown", // no device record yet
			Whitelisted:     h.whitelist.Contains(s.MAC),
			RebootThreshold: threshold,
			SecSinceUpdate:  int64(now.Sub(s.LastSeen).Seconds()),
			InstallStep:     s.LastStep,
			InstallStatus:   s.LastStatus,
			InstallProgress: s.Progress,
			InstallAttempt:  s.Attempt,
			InstallMessage:  s.LastMessage,
			InstallError:    s.ErrorCode,
			BootID:          s.BootID,
			HasLogs:         s.HasLogs,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"server_now":      now.Unix(),
		"monitoring_secs": int64(window.Seconds()),
		"threshold":       threshold,
		"devices":         out,
	})
}

// HandleAPIAction performs a manual action on a device.
// POST /api/v1/action  body: {"mac":"AA:..","action":"reflash|block|reset"}
func (h *Handlers) HandleAPIAction(w http.ResponseWriter, r *http.Request) {
	if !h.RequireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "POST only"})
		return
	}
	var body struct {
		MAC    string `json:"mac"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
		return
	}
	mac := strings.ToUpper(strings.TrimSpace(body.MAC))
	if mac == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "mac required"})
		return
	}

	var err error
	switch body.Action {
	case "reflash":
		err = h.machine.ForceReflash(r.Context(), mac)
	case "block":
		err = h.machine.ForceBlock(r.Context(), mac)
	case "reset":
		err = h.machine.Reset(r.Context(), mac)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown action"})
		return
	}
	if err != nil {
		h.logger.Error("fleet action failed", "mac", mac, "action", body.Action, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "action failed"})
		return
	}
	h.logger.Info("fleet action applied", "mac", mac, "action", body.Action)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mac": mac, "action": body.Action})
}
