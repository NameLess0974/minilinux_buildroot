package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"minilinux-server/internal/arp"
	"minilinux-server/internal/config"
	"minilinux-server/internal/server"
	"minilinux-server/internal/storage"
	"minilinux-server/internal/whitelist"
)

func main() {
	// Parse command line flags
	configPath := flag.String("config", "", "Path to config file (optional)")
	flag.Parse()

	// Setup structured logging
	logHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	logger := slog.New(logHandler)
	slog.SetDefault(logger)

	// Load configuration
	cfg := config.Load(*configPath)

	// Un SERVICE_TOKEN vide ouvre l'API admin (fleet, actions, upload d'image
	// signee). Le fichier d'environnement etant optionnel cote systemd
	// (EnvironmentFile=-), une erreur de deploiement passerait sinon inapercue :
	// on refuse de demarrer plutot que de degrader silencieusement.
	if cfg.ServiceToken == "" && os.Getenv("ALLOW_NO_SERVICE_TOKEN") != "1" {
		slog.Error("SERVICE_TOKEN absent : l'API admin serait ouverte, arret. " +
			"Definir SERVICE_TOKEN, ou ALLOW_NO_SERVICE_TOKEN=1 en developpement.")
		os.Exit(1)
	}

	// Le boot passe exclusivement par TLS : sans cert il n'y a plus de listener
	// clair pour degrader silencieusement, aucune box ne booterait.
	if !cfg.EnableHTTPS {
		slog.Error("TLS absent : le boot exige HTTPS, arret. Lancer scripts/gen-server-cert.sh",
			"cert", cfg.TLSCertFile, "key", cfg.TLSKeyFile)
		os.Exit(1)
	}

	// Print banner
	printBanner(cfg)

	// Storage : base PostgreSQL partagee avec le middleware. Les tables boot_*
	// appartiennent a ce serveur ; la table box est lue seule (liste blanche).
	store, err := storage.NewPostgres(cfg.DSN())
	if err != nil {
		slog.Error("connexion PostgreSQL impossible",
			"host", cfg.DBHost, "port", cfg.DBPort, "db", cfg.DBName, "error", err)
		os.Exit(1)
	}
	defer store.Close()

	// Liste blanche : les boitiers declares dans la console (table box).
	wl := whitelist.New(store.DB(), cfg.WhitelistReloadInterval)
	wl.Start()
	defer wl.Stop()

	// Initialize ARP cache
	arpCache := arp.NewCache(cfg.ARPCacheTTL)

	// Create HTTP server
	srv := server.New(cfg, store, wl, arpCache, logger)

	// Setup graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Background telemetry purge (retention).
	go runTelemetryPurge(ctx, store, cfg.TelemetryRetention, cfg.TelemetryPurgeEvery)

	// Start server in goroutine
	go func() {
		slog.Info("server starting", "port", cfg.HTTPSPort)
		if err := srv.Start(); err != nil {
			slog.Error("server error", "error", err)
			cancel()
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	slog.Info("shutdown signal received")

	// Graceful shutdown with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}

	slog.Info("server stopped gracefully")
}

// runTelemetryPurge periodically deletes telemetry older than retention, until ctx is done.
func runTelemetryPurge(ctx context.Context, store storage.Storage, retention, every time.Duration) {
	if retention <= 0 || every <= 0 {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	purge := func() {
		cutoff := time.Now().Add(-retention)
		ev, lg, err := store.PurgeTelemetryBefore(ctx, cutoff)
		if err != nil {
			slog.Error("telemetry purge failed", "error", err)
			return
		}
		if ev > 0 || lg > 0 {
			slog.Info("telemetry purged", "events", ev, "logs", lg, "older_than", retention)
		}
	}

	purge() // run once at startup
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			purge()
		}
	}
}

func printBanner(cfg *config.Config) {
	fmt.Println("======================================================================")
	fmt.Println("  HTTPS Boot Server with SD Fallback Detection (Go)")
	fmt.Println("  Optimized for high-traffic (200-500 devices)")
	fmt.Println("======================================================================")
	if cfg.EnableHTTPS {
		fmt.Printf("  HTTPS port:        %d  (boot, confirm, health, images, api)\n", cfg.HTTPSPort)
		fmt.Printf("  TLS cert:          %s\n", cfg.TLSCertFile)
	} else {
		fmt.Printf("  HTTPS port:        DISABLED (no cert — run scripts/gen-server-cert.sh)\n")
	}
	fmt.Printf("  Admin (interne):   %s\n", cfg.AdminAddr)
	fmt.Printf("  Directory:         %s\n", cfg.ServeDirectory)
	fmt.Printf("  Database:          postgres://%s@%s:%d/%s\n", cfg.DBUser, cfg.DBHost, cfg.DBPort, cfg.DBName)
	fmt.Printf("  Whitelist:         table box (boot_enabled)\n")
	fmt.Printf("  Monitoring window: %s\n", cfg.MonitoringWindow)
	fmt.Printf("  Failure threshold: %d x 404\n", cfg.FailureThreshold)
	fmt.Printf("  Chunk size:        %d KB\n", cfg.ChunkSize/1024)
	fmt.Println("======================================================================")
}
