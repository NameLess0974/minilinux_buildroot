package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"

	"minilinux-server/internal/arp"
	"minilinux-server/internal/config"
	"minilinux-server/internal/handlers"
	"minilinux-server/internal/middleware"
	"minilinux-server/internal/storage"
	"minilinux-server/internal/whitelist"
)

// Server represents the HTTP boot server
type Server struct {
	cfg        *config.Config
	httpServer *http.Server
	store      storage.Storage
	whitelist  *whitelist.Whitelist
	arpCache   *arp.Cache
	handlers   *handlers.Handlers
	logger     *slog.Logger
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

	// Create HTTP server
	s.httpServer = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           s.routes(),
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		MaxHeaderBytes:    1 << 20, // 1MB
	}

	return s
}

// routes sets up the HTTP routes
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Confirmation endpoint
	mux.HandleFunc(s.cfg.ConfirmEndpoint, s.handlers.HandleConfirm)

	// Boot files
	mux.HandleFunc("/boot.img", s.handlers.HandleBoot)
	mux.HandleFunc("/boot.sig", s.handlers.HandleBoot)

	// Image files
	mux.HandleFunc("/images/", s.handlers.HandleImage)

	// Health check
	mux.HandleFunc("/health", s.handleHealth)

	// Catch-all for unknown paths
	mux.HandleFunc("/", s.handleNotFound)

	// Apply middleware
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

// handleNotFound handles requests for unknown paths
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		// Root path - return allowed files info
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "HTTP Boot Server")
		fmt.Fprintln(w, "Allowed files:")
		
		files := make([]string, 0, len(s.cfg.AllowedFiles))
		for f := range s.cfg.AllowedFiles {
			files = append(files, f)
		}
		sort.Strings(files)
		for _, f := range files {
			fmt.Fprintf(w, "  - %s\n", f)
		}
		return
	}

	s.logger.Info("404 not found",
		"path", r.URL.Path,
		"method", r.Method)
	http.Error(w, "Not found", http.StatusNotFound)
}

// Start starts the HTTP server
func (s *Server) Start() error {
	// Print initial state
	s.printInitialState()

	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
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

	fmt.Printf("\n[OK] Server ready on port %d\n", s.cfg.Port)
	fmt.Println("======================================================================")
}
