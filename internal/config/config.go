package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const envPrefix = "RSS_EXPERT_"

type Config struct {
	Domain        string
	Listen        string
	MetricsToken  string
	DataDir       string
	SMTPURL       string
	LogFormat     string
	LogLevel      slog.Level
	AdminEmail    string
	AdminPassword string
	BehindProxy   bool
	ActivityPub   bool
	ShowPreview   bool
	MediaQuota    int64
	FetchLimit    int64
	PollWorkers   int
	CacheMiB      int
	Registration  string
	MailFrom      string
}

func Load() (Config, error) {
	if err := LoadEnvFiles(); err != nil {
		return Config{}, fmt.Errorf("%sENV_FILE: %w", envPrefix, err)
	}

	c := Config{
		Domain:        env("DOMAIN", ""),
		Listen:        env("LISTEN", ":11080"),
		MetricsToken:  env("METRICS_TOKEN", ""),
		DataDir:       env("DATA_DIR", "data"),
		SMTPURL:       env("SMTP_URL", ""),
		LogFormat:     strings.ToLower(env("LOG_FORMAT", "text")),
		AdminEmail:    env("ADMIN_EMAIL", ""),
		AdminPassword: env("ADMIN_PASSWORD", ""),
		BehindProxy:   truthy(env("BEHIND_PROXY", "")),
		ActivityPub:   truthy(env("ACTIVITYPUB", "")),
		ShowPreview:   truthy(env("SHOW_PREVIEW", "")),
	}

	quota, err := megabytes("MEDIA_QUOTA_MB", 512)
	if err != nil {
		return c, err
	}
	c.MediaQuota = quota

	limit, err := megabytes("FETCH_LIMIT_MB", 5)
	if err != nil {
		return c, err
	}
	c.FetchLimit = limit

	workers, err := count("POLL_WORKERS", 4, 1, 64)
	if err != nil {
		return c, err
	}
	c.PollWorkers = workers

	cache, err := count("DB_CACHE_MB", 20, 1, 1024)
	if err != nil {
		return c, err
	}
	c.CacheMiB = cache

	c.Registration = strings.ToLower(strings.TrimSpace(env("REGISTRATION", "closed")))
	switch c.Registration {
	case "closed", "invite", "open":
	default:
		return c, fmt.Errorf("%sREGISTRATION must be closed, invite or open, got %q", envPrefix, c.Registration)
	}
	c.MailFrom = env("MAIL_FROM", "")

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
	return filepath.Join(c.DataDir, "rss-expert.db")
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

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func megabytes(name string, fallback int64) (int64, error) {
	raw := env(name, "")
	if raw == "" {
		return fallback << 20, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s%s must be a positive whole number of megabytes, got %q", envPrefix, name, raw)
	}
	return n << 20, nil
}

func count(name string, fallback, low, high int) (int, error) {
	raw := env(name, "")
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < low || n > high {
		return 0, fmt.Errorf("%s%s must be a whole number between %d and %d, got %q",
			envPrefix, name, low, high, raw)
	}
	return n, nil
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
