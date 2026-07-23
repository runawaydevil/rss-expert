package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const envPrefix = "RSS_SOCIAL_"

type Config struct {
	Domain      string
	Listen      string
	AdminListen string
	DataDir     string
	SMTPURL     string
	LogFormat   string
	LogLevel    slog.Level
}

func Load() (Config, error) {
	c := Config{
		Domain:      env("DOMAIN", ""),
		Listen:      env("LISTEN", ":11080"),
		AdminListen: env("ADMIN_LISTEN", "127.0.0.1:11090"),
		DataDir:     env("DATA_DIR", "data"),
		SMTPURL:     env("SMTP_URL", ""),
		LogFormat:   strings.ToLower(env("LOG_FORMAT", "text")),
	}

	level, err := parseLevel(env("LOG_LEVEL", "info"))
	if err != nil {
		return c, err
	}
	c.LogLevel = level

	switch c.LogFormat {
	case "text", "json":
	default:
		return c, fmt.Errorf("%sLOG_FORMAT must be text or json, got %q", envPrefix, c.LogFormat)
	}

	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return c, fmt.Errorf("%sDATA_DIR: %w", envPrefix, err)
	}
	c.DataDir = abs

	return c, nil
}

func (c Config) DatabasePath() string {
	return filepath.Join(c.DataDir, "rss-social.db")
}

func (c Config) RequireServing() error {
	if c.Domain == "" {
		return fmt.Errorf("%sDOMAIN is required to serve: it is the public name this instance publishes in its feeds", envPrefix)
	}
	if strings.Contains(c.Domain, "/") {
		return fmt.Errorf("%sDOMAIN must be a hostname, not a url: %q", envPrefix, c.Domain)
	}
	return nil
}

func (c Config) Logger(w *os.File) *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

func env(name, fallback string) string {
	if v, ok := os.LookupEnv(envPrefix + name); ok && v != "" {
		return v
	}
	return fallback
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("%sLOG_LEVEL must be debug, info, warn or error, got %q", envPrefix, s)
}
