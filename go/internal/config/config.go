package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all server configuration
type Config struct {
	// Server settings
	Port           int
	ServeDirectory string
	ChunkSize      int

	// Timeouts
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ReadHeaderTimeout time.Duration

	// State machine
	MonitoringWindow time.Duration
	FailureThreshold int

	// Files
	AllowedFiles  map[string]bool
	WhitelistFile string
	DatabasePath  string

	// Cache settings
	WhitelistReloadInterval time.Duration
	ARPCacheTTL             time.Duration

	// Endpoints
	ConfirmEndpoint string
}

// Default configuration values
const (
	DefaultPort           = 18743
	DefaultServeDirectory = "/home/sabuser/minilinux_buildroot"
	DefaultChunkSize      = 64 * 1024 // 64KB

	DefaultReadTimeout       = 10 * time.Second
	DefaultWriteTimeout      = 30 * time.Minute // Very long for large files (6.7GB)
	DefaultIdleTimeout       = 120 * time.Second
	DefaultReadHeaderTimeout = 10 * time.Second

	DefaultMonitoringWindow = 5 * time.Minute
	DefaultFailureThreshold = 3

	DefaultWhitelistReloadInterval = 60 * time.Second
	DefaultARPCacheTTL             = 30 * time.Second

	DefaultConfirmEndpoint = "/confirm/"
)

// Load creates a new Config with values from environment or defaults
func Load(configPath string) *Config {
	cfg := &Config{
		Port:           getEnvInt("SERVER_PORT", DefaultPort),
		ServeDirectory: getEnvString("SERVE_DIRECTORY", DefaultServeDirectory),
		ChunkSize:      getEnvInt("CHUNK_SIZE", DefaultChunkSize),

		ReadTimeout:       getEnvDuration("READ_TIMEOUT", DefaultReadTimeout),
		WriteTimeout:      getEnvDuration("WRITE_TIMEOUT", DefaultWriteTimeout),
		IdleTimeout:       getEnvDuration("IDLE_TIMEOUT", DefaultIdleTimeout),
		ReadHeaderTimeout: getEnvDuration("READ_HEADER_TIMEOUT", DefaultReadHeaderTimeout),

		MonitoringWindow: getEnvDuration("MONITORING_WINDOW", DefaultMonitoringWindow),
		FailureThreshold: getEnvInt("FAILURE_THRESHOLD", DefaultFailureThreshold),

		WhitelistReloadInterval: getEnvDuration("WHITELIST_RELOAD_INTERVAL", DefaultWhitelistReloadInterval),
		ARPCacheTTL:             getEnvDuration("ARP_CACHE_TTL", DefaultARPCacheTTL),

		ConfirmEndpoint: DefaultConfirmEndpoint,

		AllowedFiles: map[string]bool{
			"boot.img":              true,
			"boot.sig":              true,
			"images/final_image.img.xz": true,
			"images/final_image.sig":    true,
		},
	}

	// Set file paths relative to serve directory
	cfg.WhitelistFile = cfg.ServeDirectory + "/mac_whitelist.txt"
	cfg.DatabasePath = cfg.ServeDirectory + "/devices.db"

	// Override with env if set
	if v := os.Getenv("WHITELIST_FILE"); v != "" {
		cfg.WhitelistFile = v
	}
	if v := os.Getenv("DATABASE_PATH"); v != "" {
		cfg.DatabasePath = v
	}

	return cfg
}

// IsBootFile returns true if the file is a boot file (subject to state machine)
func (c *Config) IsBootFile(filename string) bool {
	return filename == "boot.img" || filename == "boot.sig"
}

// IsAllowedFile returns true if the file is in the allowed list
func (c *Config) IsAllowedFile(filename string) bool {
	return c.AllowedFiles[filename]
}

// Helper functions for environment variables

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
