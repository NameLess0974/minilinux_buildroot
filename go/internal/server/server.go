package server

import (
	"context"
	"crypto/tls"
	"fmt"
	stdlog "log"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"minilinux-server/internal/arp"
	"minilinux-server/internal/config"
	"minilinux-server/internal/handlers"
	"minilinux-server/internal/middleware"
	"minilinux-server/internal/storage"
	"minilinux-server/internal/whitelist"
)

// tlsNoiseFilter downgrades the http.Server's connection-level noise instead of
// dropping it : un echec de cert pin cote EEPROM se presente exactement comme un
// "TLS handshake error", et le boot n'a plus de repli en clair pour le rattraper.
// Ces messages viennent du logger interne de net/http, pas de notre middleware.
type tlsNoiseFilter struct{ logger *slog.Logger }

func (f tlsNoiseFilter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	noisy := strings.Contains(msg, "bad record MAC") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "EOF")
	if noisy {
		f.logger.Debug("http server", "msg", msg)
	} else {
		f.logger.Warn("http server", "msg", msg)
	}
	return len(p), nil
}

// Server : deux listeners, separes par audience.
//   - httpsServer : public, TLS uniquement (boot compris, cert pin cote EEPROM)
//   - adminServer : INTERNE (loopback), API admin utilisee par le backend
//
// Il n'y a plus de listener HTTP clair : le boot passe exclusivement par TLS.
// L'API admin n'est pas bindee sur le listener public : injoignable depuis
// Internet meme si son controle de token etait contourne.
type Server struct {
	cfg         *config.Config
	httpsServer *http.Server
	adminServer *http.Server
	store       storage.Storage
	whitelist   *whitelist.Whitelist
	arpCache    *arp.Cache
	handlers    *handlers.Handlers
	logger      *slog.Logger
}

// New creates a new Server instance
func New(cfg *config.Config, store storage.Storage, wl *whitelist.Whitelist, arpCache *arp.Cache, logger *slog.Logger) *Server {
	s := &Server{
		cfg:       cfg,
		store:     store,
		whitelist: wl,
		arpCache:  arpCache,
		logger:    logger,
	}

	// Create handlers
	s.handlers = handlers.New(cfg, store, wl, arpCache, logger)

	if cfg.EnableHTTPS {
		s.httpsServer = &http.Server{
			Addr:              fmt.Sprintf(":%d", cfg.HTTPSPort),
			Handler:           s.secureRoutes(),
			ReadTimeout:       cfg.ReadTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			MaxHeaderBytes:    1 << 20,
			TLSConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
			// Silence benign per-connection TLS noise (see tlsNoiseFilter).
			ErrorLog: stdlog.New(tlsNoiseFilter{logger: logger}, "", 0),
		}
	}

	// Listener admin : interne uniquement, adresse explicite (loopback).
	// ReadTimeout a 0 : un upload d'image de plusieurs Go depasse toute limite
	// raisonnable. ReadHeaderTimeout protege toujours contre les slowloris.
	s.adminServer = &http.Server{
		Addr:              cfg.AdminAddr,
		Handler:           s.adminRoutes(),
		ReadTimeout:       0,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		MaxHeaderBytes:    1 << 20,
	}

	return s
}

// secureRoutes is the HTTPS listener: everything sensitive. Reached by the
// already-booted auto-installer (full Linux, curl+TLS) and by the middleware
// backend, which relays the console's admin requests with a service token.
func (s *Server) secureRoutes() http.Handler {
	mux := http.NewServeMux()

	// Fichiers de boot. Seul canal disponible : le bootloader BCM2712 (Pi 5) doit
	// avoir HTTP_CACERT_HASH dans son EEPROM, sinon il ne peut pas telecharger.
	mux.HandleFunc("/boot.img", s.handlers.HandleBoot)
	mux.HandleFunc("/boot.sig", s.handlers.HandleBoot)

	// Confirmation endpoint
	mux.HandleFunc(s.cfg.ConfirmEndpoint, s.handlers.HandleConfirm)

	// Image files
	mux.HandleFunc("/images/", s.handlers.HandleImage)

	// Ingestion telemetrie (ecriture seule). La lecture vit sur le listener
	// interne : un client public ne peut pas relire les donnees du parc.
	mux.HandleFunc("/api/v1/events", s.handlers.HandleEvent)
	mux.HandleFunc("/api/v1/logs", s.handlers.HandleLogs) // POST blob only

	// Health check
	mux.HandleFunc("/health", s.handleHealth)

	// Catch-all for unknown paths
	mux.HandleFunc("/", s.handleNotFound)

	return s.withMiddleware(mux)
}

// adminRoutes : listener INTERNE (fleet, images, sessions, logs, actions).
// Bind sur AdminAddr (loopback par defaut). HTTP clair : le trafic ne quitte
// jamais la machine.
func (s *Server) adminRoutes() http.Handler {
	mux := http.NewServeMux()

	// Telemetry read API
	mux.HandleFunc("/api/v1/sessions", s.handlers.HandleAPISessions)
	mux.HandleFunc("/api/v1/sessions/", s.handlers.HandleAPISessionDetail)
	mux.HandleFunc("/api/v1/logs/", s.handlers.HandleAPILogs)

	// Fleet API: per-box state + reboot timing, and manual actions
	mux.HandleFunc("/api/v1/fleet", s.handlers.HandleAPIFleet)
	mux.HandleFunc("/api/v1/action", s.handlers.HandleAPIAction)

	// Image API: identity + signature status of the image served to the Pi
	mux.HandleFunc("/api/v1/images", s.handlers.HandleAPIImages)
	mux.HandleFunc("/api/v1/images/upload", s.handlers.HandleAPIImageUpload)

	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/", s.handleNotFound)

	return s.withMiddleware(mux)
}

// withMiddleware wraps a mux with recovery + logging.
func (s *Server) withMiddleware(mux http.Handler) http.Handler {
	handler := middleware.Recovery(s.logger)(mux)
	handler = middleware.Logging(s.logger)(handler)
	return handler
}

// handleHealth returns server health status
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// handleNotFound returns an opaque 404 for any unknown path, including "/".
// No banner, no file listing, no protocol hints — anyone authorised already
// knows the exact paths to use.
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.logger.Info("404 not found", "path", r.URL.Path, "method", r.Method)
	http.Error(w, "Not found", http.StatusNotFound)
}

// Start starts both listeners. It blocks on the HTTPS listener; the internal
// admin listener runs in a goroutine.
func (s *Server) Start() error {
	s.printInitialState()

	if s.httpsServer == nil {
		return fmt.Errorf("TLS non configure (%s) : le boot exige HTTPS, arret",
			s.cfg.TLSCertFile)
	}

	go func() {
		s.logger.Info("admin (internal) listener starting", "addr", s.cfg.AdminAddr)
		if err := s.adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("admin listener error", "addr", s.cfg.AdminAddr, "error", err)
		}
	}()

	s.logger.Info("HTTPS listener starting",
		"port", s.cfg.HTTPSPort,
		"cert", s.cfg.TLSCertFile)
	if err := s.httpsServer.ListenAndServeTLS(s.cfg.TLSCertFile, s.cfg.TLSKeyFile); err != nil &&
		err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown gracefully shuts down every listener, returning the first error.
func (s *Server) Shutdown(ctx context.Context) error {
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if s.httpsServer != nil {
		record(s.httpsServer.Shutdown(ctx))
	}
	if s.adminServer != nil {
		record(s.adminServer.Shutdown(ctx))
	}

	return firstErr
}

// printInitialState prints the whitelist and device states on startup
func (s *Server) printInitialState() {
	// Print whitelist
	macs := s.whitelist.List()
	fmt.Printf("\nWhitelist: %d MACs loaded\n", len(macs))
	sort.Strings(macs)
	for _, mac := range macs {
		fmt.Printf("  + %s\n", mac)
	}

	// Print device states
	ctx := context.Background()
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		s.logger.Error("failed to list devices", "error", err)
		return
	}

	if len(devices) > 0 {
		fmt.Printf("\nDevice states: %d devices\n", len(devices))
		for _, d := range devices {
			fmt.Printf("  - %s: %s (404_count: %d)\n", d.MAC, d.State.String(), d.Error404Count)
		}
	}

	fmt.Printf("\n[OK] Server ready on port %d (HTTPS)\n", s.cfg.HTTPSPort)
	fmt.Println("======================================================================")
}
