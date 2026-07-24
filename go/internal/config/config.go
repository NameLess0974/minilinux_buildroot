package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all server configuration
type Config struct {
	// Server settings
	Port           int // HTTP port (boot.img / boot.sig only — firmware EEPROM can't do TLS)
	HTTPSPort      int // HTTPS port (Pi-facing: confirm, health, images, telemetry ingestion)
	ServeDirectory string
	ChunkSize      int

	// Admin API listener. The admin routes (fleet, images, sessions, logs,
	// actions) are only ever called by the middleware backend, which runs on the
	// same machine. They therefore listen on a SEPARATE, internal-only address
	// and are absent from the public listeners: an endpoint that is not bound to
	// the public interface cannot be attacked from the internet, whatever the
	// state of its authentication.
	AdminAddr string // host:port, defaults to loopback

	// TLS (self-signed cert, pinned by clients via curl --cacert)
	TLSCertFile string
	TLSKeyFile  string
	EnableHTTPS bool

	// Timeouts
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ReadHeaderTimeout time.Duration

	// State machine
	MonitoringWindow time.Duration
	FailureThreshold int

	// PostgreSQL : base partagee avec le middleware. Le serveur y ecrit ses
	// propres tables (boot_*) et LIT la table box, qui fait office de liste
	// blanche (MAC + IP declares depuis la console). Il n'ecrit jamais dans box.
	DBHost     string
	DBPort     int
	DBUser     string
	DBPassword string
	DBName     string

	// Cache settings
	WhitelistReloadInterval time.Duration
	ARPCacheTTL             time.Duration

	// Telemetry retention: events/logs older than this are purged periodically.
	TelemetryRetention   time.Duration
	TelemetryPurgeEvery  time.Duration

	// Endpoints
	ConfirmEndpoint string

	// Admin auth: shared service token presented by the middleware backend, which
	// is the only admin client (the console never talks to this server directly).
	// The backend authenticates the human (JWT) and checks the sabsystem role
	// before relaying, so this token only proves "the caller is the backend".
	// If empty, admin auth is disabled (internal deployments only).
	ServiceToken string

	// Image directory and public key, used to report the served image's identity
	// and signature validity. The private key is never loaded by the server.
	ImagesDir     string
	BootPublicKey string

	// Proxies whose X-Forwarded-For / X-Real-IP we trust, as a list of source IPs.
	// Empty (the default) means we trust nobody and always use the real socket
	// address: this server is reachable from a public IP, where any client can
	// forge those headers to disguise its origin in the logs.
	TrustedProxies []string
}

// Default configuration values
const (
	DefaultPort        = 18743
	DefaultHTTPSPort   = 18443
	DefaultProjectRoot = "/home/sabuser/minilinux_buildroot"
	DefaultChunkSize   = 64 * 1024 // 64KB

	DefaultReadTimeout       = 10 * time.Second
	DefaultWriteTimeout      = 30 * time.Minute // Very long for large files (6.7GB)
	DefaultIdleTimeout       = 120 * time.Second
	DefaultReadHeaderTimeout = 10 * time.Second

	DefaultMonitoringWindow = 5 * time.Minute
	DefaultFailureThreshold = 3

	DefaultWhitelistReloadInterval = 60 * time.Second
	DefaultARPCacheTTL             = 30 * time.Second

	DefaultTelemetryRetention  = 14 * 24 * time.Hour // keep 14 days of telemetry
	DefaultTelemetryPurgeEvery = 6 * time.Hour

	DefaultConfirmEndpoint = "/confirm/"

	// Loopback by default: the backend runs on the same machine, so the admin API
	// never needs to leave it. Override only for a trusted private interface.
	DefaultAdminAddr = "127.0.0.1:18543"
)

// Load creates a new Config with values from environment or defaults
func Load(configPath string) *Config {
	// Project root anchors all default paths. Individual paths can still be
	// overridden independently via their own env vars below.
	root := getEnvString("PROJECT_ROOT", DefaultProjectRoot)

	cfg := &Config{
		Port:           getEnvInt("SERVER_PORT", DefaultPort),
		HTTPSPort:      getEnvInt("HTTPS_PORT", DefaultHTTPSPort),
		ServeDirectory: getEnvString("SERVE_DIRECTORY", root+"/data"),
		ChunkSize:      getEnvInt("CHUNK_SIZE", DefaultChunkSize),

		ReadTimeout:       getEnvDuration("READ_TIMEOUT", DefaultReadTimeout),
		WriteTimeout:      getEnvDuration("WRITE_TIMEOUT", DefaultWriteTimeout),
		IdleTimeout:       getEnvDuration("IDLE_TIMEOUT", DefaultIdleTimeout),
		ReadHeaderTimeout: getEnvDuration("READ_HEADER_TIMEOUT", DefaultReadHeaderTimeout),

		MonitoringWindow: getEnvDuration("MONITORING_WINDOW", DefaultMonitoringWindow),
		FailureThreshold: getEnvInt("FAILURE_THRESHOLD", DefaultFailureThreshold),

		WhitelistReloadInterval: getEnvDuration("WHITELIST_RELOAD_INTERVAL", DefaultWhitelistReloadInterval),
		ARPCacheTTL:             getEnvDuration("ARP_CACHE_TTL", DefaultARPCacheTTL),

		TelemetryRetention:  getEnvDuration("TELEMETRY_RETENTION", DefaultTelemetryRetention),
		TelemetryPurgeEvery: getEnvDuration("TELEMETRY_PURGE_EVERY", DefaultTelemetryPurgeEvery),

		ConfirmEndpoint: DefaultConfirmEndpoint,

		ServiceToken: getEnvString("SERVICE_TOKEN", ""),
		AdminAddr:    getEnvString("ADMIN_ADDR", DefaultAdminAddr),

		// Memes noms de variables que le .env du middleware.
		DBHost:     getEnvString("DB_HOST", "localhost"),
		DBPort:     getEnvInt("DB_PORT", 5432),
		DBUser:     getEnvString("DB_USER", "postgres"),
		DBPassword: getEnvString("DB_PASSWORD", ""),
		DBName:     getEnvString("DB_NAME", "sab"),

		TrustedProxies: getEnvList("TRUSTED_PROXIES"),
	}

	// Paths default under the project root but each can be overridden on its own.
	// They live OUTSIDE ServeDirectory (data/) so secrets and the DB are never
	// reachable through the file-serving handlers.

	// Images live inside ServeDirectory (they are served to the Pi); the public
	// key does not, it is only read to verify the signature we report.
	cfg.ImagesDir = getEnvString("IMAGES_DIR", cfg.ServeDirectory+"/images")
	cfg.BootPublicKey = getEnvString("BOOT_PUBLIC_KEY", root+"/private/keys/bootkey-public.pem")

	cfg.TLSCertFile = getEnvString("TLS_CERT_FILE", root+"/private/certs/server.crt")
	cfg.TLSKeyFile = getEnvString("TLS_KEY_FILE", root+"/private/certs/server.key")
	if fileExists(cfg.TLSCertFile) && fileExists(cfg.TLSKeyFile) {
		cfg.EnableHTTPS = true
	}

	return cfg
}

// Helper functions for environment variables

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// DSN construit la chaine de connexion PostgreSQL.
func (c *Config) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		url.QueryEscape(c.DBUser), url.QueryEscape(c.DBPassword),
		c.DBHost, c.DBPort, c.DBName)
}

// getEnvList reads a comma-separated env var into a slice, dropping empties.
func getEnvList(key string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func getEnvString(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}
