package whitelist

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Whitelist : boitiers autorises, lus depuis la table box.
//
// Regle stricte MAC + IP. Sur erreur de base on conserve la derniere version
// connue, sinon une panne Postgres bloquerait le boot de tout le parc.
type Whitelist struct {
	db             *sql.DB
	reloadInterval time.Duration

	mu       sync.RWMutex
	byMAC    map[string]entry // MAC normalise en majuscules -> entree
	byIP     map[string]entry // IP -> entree
	loadedAt time.Time

	stopCh chan struct{}
	logger *slog.Logger
}

type entry struct {
	MAC  string
	IP   string
	Name string
}

// New cree le gestionnaire. db peut etre nil (aucune autorisation accordee),
// ce qui n'arrive qu'en test.
func New(db *sql.DB, reloadInterval time.Duration) *Whitelist {
	return &Whitelist{
		db:             db,
		reloadInterval: reloadInterval,
		byMAC:          make(map[string]entry),
		byIP:           make(map[string]entry),
		stopCh:         make(chan struct{}),
		logger:         slog.Default(),
	}
}

func (w *Whitelist) Start() {
	w.reload()
	go w.reloadLoop()
}

func (w *Whitelist) Stop() { close(w.stopCh) }

// Contains : MAC declare et actif. Ne verifie pas l'IP (voir Authorized).
func (w *Whitelist) Contains(mac string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.byMAC[normalizeMAC(mac)]
	return ok
}

// Authorized : l'IP doit etre declaree, et le MAC ARP correspondre s'il est
// resolu. Sans entree ARP, l'IP declaree suffit. Renvoie le nom de la box.
func (w *Whitelist) Authorized(mac, ip string) (bool, string) {
	w.mu.RLock()
	e, ok := w.byIP[strings.TrimSpace(ip)]
	w.mu.RUnlock()
	if !ok {
		return false, ""
	}

	m := normalizeMAC(mac)
	if m == "" || m == "UNKNOWN" {
		return true, e.Name
	}
	return m == e.MAC, e.Name
}

// ExpectedMAC renvoie le MAC declare pour une IP, pour tracer un refus.
func (w *Whitelist) ExpectedMAC(ip string) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.byIP[strings.TrimSpace(ip)].MAC
}

func (w *Whitelist) Count() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.byMAC)
}

func (w *Whitelist) List() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	macs := make([]string, 0, len(w.byMAC))
	for mac := range w.byMAC {
		macs = append(macs, mac)
	}
	return macs
}

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

func (w *Whitelist) reload() {
	if w.db == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := w.db.QueryContext(ctx, `
		SELECT mac_address, ip_address, name
		FROM box
		WHERE boot_enabled = true`)
	if err != nil {
		// On garde la version precedente plutot que de refuser tout le parc.
		w.logger.Error("whitelist: lecture de box impossible, conservation du cache",
			"error", err, "entries", w.Count())
		return
	}
	defer rows.Close()

	newByMAC := make(map[string]entry)
	newByIP := make(map[string]entry)
	for rows.Next() {
		var mac, ip, name string
		if err := rows.Scan(&mac, &ip, &name); err != nil {
			w.logger.Error("whitelist: ligne box illisible", "error", err)
			return
		}
		mac = normalizeMAC(mac)
		ip = strings.TrimSpace(ip)
		if mac == "" || ip == "" {
			continue
		}
		e := entry{MAC: mac, IP: ip, Name: name}
		newByMAC[mac] = e

		// ip_address n'est pas unique en base : sans ce garde-fou, le boitier
		// autorise dependrait de l'ordre des lignes.
		if prev, dup := newByIP[ip]; dup {
			w.logger.Warn("whitelist: IP declaree sur plusieurs boitiers, la seconde est ignoree",
				"ip", ip, "retenu", prev.Name, "ignore", name)
			continue
		}
		newByIP[ip] = e
	}
	if err := rows.Err(); err != nil {
		w.logger.Error("whitelist: parcours de box interrompu", "error", err)
		return
	}

	w.mu.Lock()
	oldCount := len(w.byMAC)
	w.byMAC = newByMAC
	w.byIP = newByIP
	w.loadedAt = time.Now()
	w.mu.Unlock()

	if oldCount != len(newByMAC) {
		w.logger.Info("whitelist rechargee depuis box", "count", len(newByMAC))
	}
}

// normalizeMAC : comparaisons insensibles a la casse (box en minuscules,
// ARP en majuscules).
func normalizeMAC(mac string) string {
	return strings.ToUpper(strings.TrimSpace(mac))
}
