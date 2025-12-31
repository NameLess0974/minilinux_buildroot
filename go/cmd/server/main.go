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

	// Print banner
	printBanner(cfg)

	// Initialize storage (SQLite)
	store, err := storage.NewSQLiteStorage(cfg.DatabasePath)
	if err != nil {
		slog.Error("failed to initialize storage", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	// Initialize whitelist manager
	wl := whitelist.New(cfg.WhitelistFile, cfg.WhitelistReloadInterval)
	wl.Start()
	defer wl.Stop()

	// Initialize ARP cache
	arpCache := arp.NewCache(cfg.ARPCacheTTL)

	// Create HTTP server
	srv := server.New(cfg, store, wl, arpCache, logger)

	// Setup graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start server in goroutine
	go func() {
		slog.Info("server starting", "port", cfg.Port)
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

func printBanner(cfg *config.Config) {
	fmt.Println("======================================================================")
	fmt.Println("  HTTP Boot Server with SD Fallback Detection (Go)")
	fmt.Println("  Optimized for high-traffic (200-500 devices)")
	fmt.Println("======================================================================")
	fmt.Printf("  Port:              %d\n", cfg.Port)
	fmt.Printf("  Directory:         %s\n", cfg.ServeDirectory)
	fmt.Printf("  Database:          %s\n", cfg.DatabasePath)
	fmt.Printf("  Whitelist:         %s\n", cfg.WhitelistFile)
	fmt.Printf("  Monitoring window: %s\n", cfg.MonitoringWindow)
	fmt.Printf("  Failure threshold: %d x 404\n", cfg.FailureThreshold)
	fmt.Printf("  Chunk size:        %d KB\n", cfg.ChunkSize/1024)
	fmt.Println("======================================================================")
}
