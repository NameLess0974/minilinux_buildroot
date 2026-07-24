package whitelist

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Whitelist tient en memoire la liste des boitiers autorises, lue depuis la
// table box du middleware (declaration faite dans la console).
//
// Regle : MAC ET IP doivent correspondre. L'IP est le point d'entree car c'est
// la seule donnee certaine au moment du boot (adresse de la socket) ; le MAC
// vient d'ARP et n'est pas toujours resolvable.
//
// Le cache n'est jamais vide sur erreur : si Postgres est injoignable, on
// conserve la derniere version connue. Sans cela une panne de base bloquerait
// le boot de tout le parc, alors que la liste change rarement.
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

// Contains indique si un MAC est declare et actif. Ne verifie pas l'IP : les
// appelants qui disposent de l'IP doivent utiliser Authorized.
func (w *Whitelist) Contains(mac string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.byMAC[normalizeMAC(mac)]
	return ok
}

// Authorized applique la regle stricte MAC + IP.
//
// L'IP doit etre declaree et active. Si ARP a resolu un MAC, il doit
// correspondre a celui declare pour cette IP ; sinon on refuse (usurpation).
// Si ARP n'a rien resolu (mac vide ou UNKNOWN), l'IP declaree suffit : elle
// prouve deja que le boitier a ete enregistre, et exiger ARP ferait echouer un
// premier boot dont l'entree n'est pas encore dans le cache du noyau.
//
// Retourne aussi le nom de la box, pour les logs.
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
		// On garde la version precedente : mieux vaut une liste un peu vieille
		// qu'un parc entier refuse.
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

// normalizeMAC uniformise la casse : la console enregistre les MAC en
// minuscules, ARP les renvoie en majuscules.
func normalizeMAC(mac string) string {
	return strings.ToUpper(strings.TrimSpace(mac))
}
