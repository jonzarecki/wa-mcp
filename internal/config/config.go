package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Config holds application configuration.
type Config struct {
	DBDir      string
	LogLevel   slog.Level
	FFmpegPath string
	WhatsApp   WhatsAppConfig
	MCP        MCPConfig
}

// WhatsAppConfig holds WhatsApp-specific configuration.
type WhatsAppConfig struct {
	QRTimeout time.Duration
}

// MCPConfig holds MCP server configuration.
type MCPConfig struct {
	MaxPageSize int
	Transport   string // "stdio" (default) or "http"
	HTTPAddr    string // listen address for HTTP mode (e.g. ":8085")
	APIKey      string // optional Bearer token for HTTP auth
}

// Load loads configuration from environment variables.
func Load() (*Config, error) {
	cfg := &Config{
		DBDir:      getEnv("DB_DIR", "store"),
		FFmpegPath: getEnv("FFMPEG_PATH", "ffmpeg"),
		WhatsApp: WhatsAppConfig{
			QRTimeout: 3 * time.Minute,
		},
		MCP: MCPConfig{
			MaxPageSize: 200,
			Transport:   getEnv("MCP_TRANSPORT", "stdio"),
			HTTPAddr:    getEnv("MCP_HTTP_ADDR", ":8085"),
			APIKey:      getEnv("MCP_API_KEY", ""),
		},
	}

	logLevelStr := getEnv("LOG_LEVEL", "INFO")
	cfg.LogLevel = parseLogLevel(logLevelStr)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.DBDir == "" {
		return fmt.Errorf("DB_DIR cannot be empty")
	}
	if c.MCP.MaxPageSize < 1 {
		return fmt.Errorf("MCP.MaxPageSize must be positive")
	}
	return nil
}

// getEnv gets an environment variable with a default value.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// parseLogLevel parses a log level string to slog.Level.
func parseLogLevel(level string) slog.Level {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// LogLevelString returns the string representation of the log level.
func (c *Config) LogLevelString() string {
	switch c.LogLevel {
	case slog.LevelDebug:
		return "DEBUG"
	case slog.LevelWarn:
		return "WARN"
	case slog.LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}
