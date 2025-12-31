package handlers

import (
	"log/slog"

	"minilinux-server/internal/arp"
	"minilinux-server/internal/config"
	"minilinux-server/internal/state"
	"minilinux-server/internal/storage"
	"minilinux-server/internal/whitelist"
)

// Handlers contains all HTTP handlers and their dependencies
type Handlers struct {
	cfg       *config.Config
	store     storage.Storage
	whitelist *whitelist.Whitelist
	arpCache  *arp.Cache
	machine   *state.Machine
	logger    *slog.Logger
}

// New creates a new Handlers instance
func New(cfg *config.Config, store storage.Storage, wl *whitelist.Whitelist, arpCache *arp.Cache, logger *slog.Logger) *Handlers {
	return &Handlers{
		cfg:       cfg,
		store:     store,
		whitelist: wl,
		arpCache:  arpCache,
		machine:   state.NewMachine(store, cfg.MonitoringWindow, cfg.FailureThreshold, logger),
		logger:    logger,
	}
}
