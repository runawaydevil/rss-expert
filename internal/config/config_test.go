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
	if c.MetricsToken != "" {
		t.Error("metrics answer without a token by default; they must be off until one is set")
	}
	if c.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v", c.LogLevel)
	}
	if !filepath.IsAbs(c.DataDir) {
		t.Errorf("DataDir = %q, want an absolute path", c.DataDir)
	}
}

func TestEnvironmentOverrides(t *testing.T) {
	t.Setenv("RSS_EXPERT_DOMAIN", "example.org")
	t.Setenv("RSS_EXPERT_LISTEN", ":9999")
	t.Setenv("RSS_EXPERT_METRICS_TOKEN", "a-token")
	t.Setenv("RSS_EXPERT_DATA_DIR", t.TempDir())
	t.Setenv("RSS_EXPERT_LOG_FORMAT", "json")
	t.Setenv("RSS_EXPERT_LOG_LEVEL", "debug")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Domain != "example.org" || c.Listen != ":9999" || c.MetricsToken != "a-token" {
		t.Errorf("not read from environment: %+v", c)
	}
	if c.LogFormat != "json" || c.LogLevel != slog.LevelDebug {
		t.Errorf("logging not read from environment: %+v", c)
	}
	if filepath.Base(c.DatabasePath()) != "rss-expert.db" {
		t.Errorf("DatabasePath = %q", c.DatabasePath())
	}
}

func TestEmptyEnvironmentFallsBackToDefault(t *testing.T) {
	t.Setenv("RSS_EXPERT_LISTEN", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":11080" {
		t.Errorf("Listen = %q, an empty variable should not blank the default", c.Listen)
	}
}

func TestRejectsBadValues(t *testing.T) {
	t.Setenv("RSS_EXPERT_LOG_LEVEL", "chatty")
	if _, err := Load(); err == nil {
		t.Error("an unknown log level was accepted")
	}

	t.Setenv("RSS_EXPERT_LOG_LEVEL", "info")
	t.Setenv("RSS_EXPERT_LOG_FORMAT", "yaml")
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

func TestLimitsComeFromTheEnvironment(t *testing.T) {
	t.Setenv("RSS_EXPERT_MEDIA_QUOTA_MB", "64")
	t.Setenv("RSS_EXPERT_FETCH_LIMIT_MB", "2")
	t.Setenv("RSS_EXPERT_POLL_WORKERS", "8")
	t.Setenv("RSS_EXPERT_BEHIND_PROXY", "true")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.MediaQuota != 64<<20 {
		t.Errorf("media quota = %d", c.MediaQuota)
	}
	if c.FetchLimit != 2<<20 {
		t.Errorf("fetch limit = %d", c.FetchLimit)
	}
	if c.PollWorkers != 8 {
		t.Errorf("poll workers = %d", c.PollWorkers)
	}
	if !c.BehindProxy {
		t.Error("behind-proxy was not read from the environment")
	}
}

func TestNonsenseLimitsAreRefusedAtStartup(t *testing.T) {
	for name, value := range map[string]string{
		"RSS_EXPERT_MEDIA_QUOTA_MB": "0",
		"RSS_EXPERT_FETCH_LIMIT_MB": "-3",
		"RSS_EXPERT_POLL_WORKERS":   "9000",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := Load(); err == nil {
				t.Errorf("%s=%q was accepted", name, value)
			}
		})
	}
}

func TestTheDefaultsAreSaneWithNothingSet(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.MediaQuota != 512<<20 || c.FetchLimit != 5<<20 || c.PollWorkers != 4 {
		t.Errorf("defaults drifted: quota=%d fetch=%d workers=%d", c.MediaQuota, c.FetchLimit, c.PollWorkers)
	}
	if c.BehindProxy || c.ShowPreview {
		t.Error("an unconfigured instance trusts proxy headers or exposes the design preview")
	}
}
