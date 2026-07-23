package handlers

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HandleBoot handles requests for boot.img and boot.sig
func (h *Handlers) HandleBoot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clientIP := getClientIP(r)
	clientMAC := h.arpCache.Lookup(clientIP)

	// Parse filename
	filename := strings.TrimPrefix(r.URL.Path, "/")

	// Log request
	h.logger.Info("boot request",
		"ip", clientIP,
		"mac", clientMAC,
		"file", filename)

	// Whitelist check. Note: an UNKNOWN (unresolved) MAC is allowed here on purpose —
	// a Pi doing its very first network boot may not be in the ARP cache yet, and boot
	// files are integrity-protected by the RSA signature regardless.
	if clientMAC != "UNKNOWN" && !h.whitelist.Contains(clientMAC) {
		h.logger.Warn("boot request denied - MAC not whitelisted",
			"mac", clientMAC,
			"ip", clientIP)
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// URL /boot.img maps to <ServeDirectory>/boot/boot.img. The client-facing URL
	// stays fixed (firmware EEPROM) while the file is stored under boot/.
	filePath, ok := h.safeResolve("boot/" + filename)
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	stat, err := os.Stat(filePath)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// State machine logic (only for known MACs)
	if clientMAC != "UNKNOWN" {
		shouldServe, reason, err := h.machine.ShouldServeBootFile(ctx, clientMAC)
		if err != nil {
			h.logger.Error("state machine error",
				"mac", clientMAC,
				"error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		h.logger.Info("state check",
			"mac", clientMAC,
			"file", filename,
			"serve", shouldServe,
			"reason", reason)

		if !shouldServe {
			// Handle 404 for boot.sig (triggers state transition)
			if filename == "boot.sig" {
				result, err := h.machine.Handle404Request(ctx, clientMAC)
				if err != nil {
					h.logger.Error("failed to handle 404",
						"mac", clientMAC,
						"error", err)
				} else if result.Transitioned {
					h.logger.Info("state transition",
						"mac", clientMAC,
						"from", result.OldState.String(),
						"to", result.NewState.String(),
						"message", result.Message)
				}
			}
			http.Error(w, "Boot files currently unavailable", http.StatusNotFound)
			return
		}
	}

	// Serve the file
	h.serveFile(w, r, filePath, filename, stat.Size())
}

// HandleImage handles requests for image files (no state machine)
func (h *Handlers) HandleImage(w http.ResponseWriter, r *http.Request) {
	clientIP := getClientIP(r)
	clientMAC := h.arpCache.Lookup(clientIP)

	// Parse filename (e.g., /images/final_image.img.xz)
	filename := strings.TrimPrefix(r.URL.Path, "/")

	h.logger.Info("image request",
		"ip", clientIP,
		"mac", clientMAC,
		"file", filename)

	// Whitelist: images are sensitive. Deny unless the MAC is known AND whitelisted.
	// An unresolvable MAC (UNKNOWN) is NOT a free pass — it is refused.
	if !h.whitelist.Contains(clientMAC) {
		h.logger.Warn("image request denied - MAC not whitelisted",
			"mac", clientMAC,
			"ip", clientIP)
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Resolve the file safely inside the images directory (prevents path traversal).
	filePath, ok := h.safeResolve(filename)
	if !ok {
		h.logger.Warn("image request rejected - unsafe path",
			"requested", filename,
			"ip", clientIP)
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	stat, err := os.Stat(filePath)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Serve the file (no state machine for images)
	h.serveFile(w, r, filePath, filepath.Base(filename), stat.Size())
}

// serveFile streams a file to the client
func (h *Handlers) serveFile(w http.ResponseWriter, r *http.Request, filePath, filename string, fileSize int64) {
	file, err := os.Open(filePath)
	if err != nil {
		h.logger.Error("failed to open file",
			"file", filePath,
			"error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// Set headers
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))

	// Stream file using io.Copy (uses sendfile on Linux for zero-copy)
	bytesSent, err := io.Copy(w, file)
	if err != nil {
		h.logger.Error("transfer error",
			"file", filename,
			"sent", bytesSent,
			"total", fileSize,
			"error", err)
		return
	}

	h.logger.Info("transfer complete",
		"file", filename,
		"bytes", bytesSent)
}

// safeResolve maps a request-relative filename to an absolute path guaranteed to be
// inside ServeDirectory. Returns (path, false) if the result would escape the dir
// (path traversal) — protecting bootkey-private.pem, devices.db, certs/, etc.
func (h *Handlers) safeResolve(filename string) (string, bool) {
	// Reject obvious traversal attempts up-front.
	if strings.Contains(filename, "..") {
		return "", false
	}
	// Clean and join, then verify containment with a path-boundary check.
	clean := filepath.Clean("/" + filename) // leading slash neutralises absolute paths
	full := filepath.Join(h.cfg.ServeDirectory, clean)

	base := filepath.Clean(h.cfg.ServeDirectory)
	if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", false
	}
	return full, true
}

// getClientIP extracts the client IP from the request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header first (for proxies)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	ip := r.RemoteAddr
	// Remove port if present
	if colonIdx := strings.LastIndex(ip, ":"); colonIdx != -1 {
		// Check if it's IPv6 (contains [ ])
		if strings.Contains(ip, "[") {
			// IPv6: [::1]:8080
			if bracketIdx := strings.Index(ip, "]"); bracketIdx != -1 {
				ip = ip[1:bracketIdx]
			}
		} else {
			// IPv4: 192.168.1.1:8080
			ip = ip[:colonIdx]
		}
	}

	return ip
}
