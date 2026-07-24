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
	clientIP := h.clientIP(r)
	clientMAC := h.arpCache.Lookup(clientIP)

	// Parse filename
	filename := strings.TrimPrefix(r.URL.Path, "/")

	// Log request
	h.logger.Info("boot request",
		"ip", clientIP,
		"mac", clientMAC,
		"file", filename)

	// Le boitier doit etre declare dans la console : IP ET MAC doivent
	// correspondre. On part de l'IP, seule donnee certaine ici, ce qui ferme le
	// cas d'un MAC non resolu par ARP qui passait auparavant sans controle.
	if ok, name := h.whitelist.Authorized(clientMAC, clientIP); !ok {
		h.logger.Warn("boot refuse - boitier non declare ou MAC/IP incoherents",
			"ip", clientIP, "mac_arp", clientMAC,
			"mac_attendu", h.whitelist.ExpectedMAC(clientIP))
		http.Error(w, "Not found", http.StatusNotFound)
		return
	} else if name != "" {
		h.logger.Info("boot autorise", "box", name, "ip", clientIP)
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
	clientIP := h.clientIP(r)
	clientMAC := h.arpCache.Lookup(clientIP)

	// Parse filename (e.g., /images/final_image.img.xz)
	filename := strings.TrimPrefix(r.URL.Path, "/")

	h.logger.Info("image request",
		"ip", clientIP,
		"mac", clientMAC,
		"file", filename)

	// Meme regle stricte que pour le boot : IP declaree et MAC coherent.
	if ok, _ := h.whitelist.Authorized(clientMAC, clientIP); !ok {
		h.logger.Warn("image refusee - boitier non declare ou MAC/IP incoherents",
			"ip", clientIP, "mac_arp", clientMAC,
			"mac_attendu", h.whitelist.ExpectedMAC(clientIP))
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

// getClientIP renvoie l'adresse du pair, sans tenir compte des en-tetes de
// forwarding. Preferer h.clientIP quand un Handlers est disponible.
func getClientIP(r *http.Request) string {
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

// clientIP honore X-Forwarded-For / X-Real-IP UNIQUEMENT si la connexion vient
// d'un proxy liste dans TRUSTED_PROXIES. Le serveur est joignable depuis une IP
// publique : sinon n'importe qui maquillerait son origine dans les logs et
// fausserait la resolution ARP dont depend la whitelist.
func (h *Handlers) clientIP(r *http.Request) string {
	peer := getClientIP(r)

	if len(h.cfg.TrustedProxies) == 0 {
		return peer
	}
	trusted := false
	for _, p := range h.cfg.TrustedProxies {
		if p == peer {
			trusted = true
			break
		}
	}
	if !trusted {
		return peer
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Le premier element est le client d'origine.
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return xri
	}
	return peer
}
