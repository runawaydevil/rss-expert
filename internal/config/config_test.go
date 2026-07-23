package config

import (
	"log/slog"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":11080" {
		t.Errorf("Listen = %q", c.Listen)
	}
	if c.AdminListen != "127.0.0.1:11090" {
		t.Errorf("AdminListen = %q, the admin panel must not default to a public address", c.AdminListen)
	}
	if c.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v", c.LogLevel)
	}
	if !filepath.IsAbs(c.DataDir) {
		t.Errorf("DataDir = %q, want an absolute path", c.DataDir)
	}
}

func TestEnvironmentOverrides(t *testing.T) {
	t.Setenv("RSS_SOCIAL_DOMAIN", "example.org")
	t.Setenv("RSS_SOCIAL_LISTEN", ":9999")
	t.Setenv("RSS_SOCIAL_ADMIN_LISTEN", "127.0.0.1:9998")
	t.Setenv("RSS_SOCIAL_DATA_DIR", t.TempDir())
	t.Setenv("RSS_SOCIAL_LOG_FORMAT", "json")
	t.Setenv("RSS_SOCIAL_LOG_LEVEL", "debug")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Domain != "example.org" || c.Listen != ":9999" || c.AdminListen != "127.0.0.1:9998" {
		t.Errorf("addresses not read from environment: %+v", c)
	}
	if c.LogFormat != "json" || c.LogLevel != slog.LevelDebug {
		t.Errorf("logging not read from environment: %+v", c)
	}
	if filepath.Base(c.DatabasePath()) != "rss-social.db" {
		t.Errorf("DatabasePath = %q", c.DatabasePath())
	}
}

func TestEmptyEnvironmentFallsBackToDefault(t *testing.T) {
	t.Setenv("RSS_SOCIAL_LISTEN", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":11080" {
		t.Errorf("Listen = %q, an empty variable should not blank the default", c.Listen)
	}
}

func TestRejectsBadValues(t *testing.T) {
	t.Setenv("RSS_SOCIAL_LOG_LEVEL", "chatty")
	if _, err := Load(); err == nil {
		t.Error("an unknown log level was accepted")
	}

	t.Setenv("RSS_SOCIAL_LOG_LEVEL", "info")
	t.Setenv("RSS_SOCIAL_LOG_FORMAT", "yaml")
	if _, err := Load(); err == nil {
		t.Error("an unknown log format was accepted")
	}
}

func TestRequireServing(t *testing.T) {
	var c Config
	if err := c.RequireServing(); err == nil {
		t.Error("serving without a domain was allowed")
	}

	c.Domain = "https://example.org/"
	if err := c.RequireServing(); err == nil {
		t.Error("a url was accepted where a hostname is required")
	}

	c.Domain = "example.org"
	if err := c.RequireServing(); err != nil {
		t.Errorf("a plain hostname was rejected: %v", err)
	}
}
