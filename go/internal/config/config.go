package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all server configuration
type Config struct {
	// Server settings
	Port           int // HTTP port (boot.img / boot.sig only — firmware EEPROM can't do TLS)
	HTTPSPort      int // HTTPS port (everything else: confirm, health, images, api, dashboard)
	ServeDirectory string
	ChunkSize      int

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

	// Files
	WhitelistFile string
	DatabasePath  string

	// Cache settings
	WhitelistReloadInterval time.Duration
	ARPCacheTTL             time.Duration

	// Telemetry retention: events/logs older than this are purged periodically.
	TelemetryRetention   time.Duration
	TelemetryPurgeEvery  time.Duration

	// Endpoints
	ConfirmEndpoint string

	// Admin auth (HTTP Basic): protects the dashboard and destructive actions.
	// If AdminUser is empty, auth is disabled (open access).
	AdminUser string
	AdminPass string
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

		AdminUser: getEnvString("ADMIN_USER", ""),
		AdminPass: getEnvString("ADMIN_PASS", ""),
	}

	// Paths default under the project root but each can be overridden on its own.
	// They live OUTSIDE ServeDirectory (data/) so secrets and the DB are never
	// reachable through the file-serving handlers.
	cfg.WhitelistFile = getEnvString("WHITELIST_FILE", root+"/config/mac_whitelist.txt")
	cfg.DatabasePath = getEnvString("DATABASE_PATH", root+"/db/devices.db")

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
