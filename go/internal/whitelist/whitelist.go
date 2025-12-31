package whitelist

import (
	"bufio"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// Whitelist manages a cached set of allowed MAC addresses
type Whitelist struct {
	filePath       string
	reloadInterval time.Duration
	macs           sync.Map // map[string]bool
	count          int
	mu             sync.RWMutex
	stopCh         chan struct{}
	logger         *slog.Logger
}

// New creates a new Whitelist manager
func New(filePath string, reloadInterval time.Duration) *Whitelist {
	return &Whitelist{
		filePath:       filePath,
		reloadInterval: reloadInterval,
		stopCh:         make(chan struct{}),
		logger:         slog.Default(),
	}
}

// Start begins the background reload loop
func (w *Whitelist) Start() {
	// Initial load
	w.reload()

	// Start background reload goroutine
	go w.reloadLoop()
}

// Stop stops the background reload loop
func (w *Whitelist) Stop() {
	close(w.stopCh)
}

// Contains checks if a MAC address is in the whitelist
func (w *Whitelist) Contains(mac string) bool {
	_, ok := w.macs.Load(strings.ToUpper(mac))
	return ok
}

// Count returns the number of MACs in the whitelist
func (w *Whitelist) Count() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.count
}

// List returns all MACs in the whitelist
func (w *Whitelist) List() []string {
	var macs []string
	w.macs.Range(func(key, _ interface{}) bool {
		macs = append(macs, key.(string))
		return true
	})
	return macs
}

// reloadLoop periodically reloads the whitelist
func (w *Whitelist) reloadLoop() {
	ticker := time.NewTicker(w.reloadInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.reload()
		}
	}
}

// reload reads the whitelist file and updates the cache
func (w *Whitelist) reload() {
	file, err := os.Open(w.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			w.logger.Error("failed to open whitelist file",
				"path", w.filePath,
				"error", err)
		}
		return
	}
	defer file.Close()

	// Build new map
	newMacs := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		mac := strings.ToUpper(line)
		newMacs[mac] = true
	}

	if err := scanner.Err(); err != nil {
		w.logger.Error("error reading whitelist file",
			"path", w.filePath,
			"error", err)
		return
	}

	// Check if changed
	w.mu.Lock()
	oldCount := w.count
	w.count = len(newMacs)
	w.mu.Unlock()

	// Clear old entries and add new ones
	w.macs.Range(func(key, _ interface{}) bool {
		if !newMacs[key.(string)] {
			w.macs.Delete(key)
		}
		return true
	})
	for mac := range newMacs {
		w.macs.Store(mac, true)
	}

	if oldCount != len(newMacs) {
		w.logger.Info("whitelist reloaded",
			"count", len(newMacs),
			"path", w.filePath)
	}
}
