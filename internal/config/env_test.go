package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func cleanEnv(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, envPrefix) {
			t.Setenv(name, value)
			os.Unsetenv(name)
		}
	}
}

func inDir(t *testing.T, dir string) {
	t.Helper()
	cleanEnv(t)

	was, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(was) })
}

func TestTheEnvFileReallyDrivesTheConfiguration(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", `# rss-expert
RSS_EXPERT_DOMAIN=rss.example.org
RSS_EXPERT_ADMIN_EMAIL = you@example.org
RSS_EXPERT_ADMIN_PASSWORD="a long enough password"
export RSS_EXPERT_MEDIA_QUOTA_MB=128
RSS_EXPERT_POLL_WORKERS=6   # how many at once
RSS_EXPERT_BEHIND_PROXY=true
RSS_EXPERT_LOG_FORMAT='json'

not a pair
`)
	inDir(t, dir)

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Domain != "rss.example.org" {
		t.Errorf("domain = %q", c.Domain)
	}
	if c.AdminEmail != "you@example.org" {
		t.Errorf("admin email = %q; spaces around the = were not trimmed", c.AdminEmail)
	}
	if c.AdminPassword != "a long enough password" {
		t.Errorf("admin password = %q; the quotes were not stripped", c.AdminPassword)
	}
	if c.MediaQuota != 128<<20 {
		t.Errorf("media quota = %d; the export prefix was not handled", c.MediaQuota)
	}
	if c.PollWorkers != 6 {
		t.Errorf("poll workers = %d; the trailing comment leaked into the value", c.PollWorkers)
	}
	if !c.BehindProxy {
		t.Error("behind-proxy did not come through from the file")
	}
	if c.LogFormat != "json" {
		t.Errorf("log format = %q", c.LogFormat)
	}
}

func TestTheRealEnvironmentBeatsTheFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "RSS_EXPERT_DOMAIN=from-the-file.example\n")
	inDir(t, dir)

	t.Setenv("RSS_EXPERT_DOMAIN", "from-the-environment.example")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Domain != "from-the-environment.example" {
		t.Errorf("domain = %q; the file overwrote a variable that was already set", c.Domain)
	}
}

func TestLocalOverridesTheDeployedFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "RSS_EXPERT_DOMAIN=rss.example.org\nRSS_EXPERT_MEDIA_QUOTA_MB=512\n")
	writeFile(t, dir, ".env.local", "RSS_EXPERT_DOMAIN=localhost:11080\n")
	inDir(t, dir)

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Domain != "localhost:11080" {
		t.Errorf("domain = %q; .env.local did not win", c.Domain)
	}
	if c.MediaQuota != 512<<20 {
		t.Errorf("media quota = %d; .env stopped being read once .env.local existed", c.MediaQuota)
	}
}

func TestAnEnvFileNamedOnPurposeMustExist(t *testing.T) {
	inDir(t, t.TempDir())
	t.Setenv("RSS_EXPERT_ENV_FILE", "there-is-no-such-file.env")

	if _, err := Load(); err == nil {
		t.Error("a missing file that was named on purpose was passed over in silence")
	}
}

func TestNoEnvFileIsNotAnError(t *testing.T) {
	inDir(t, t.TempDir())
	t.Setenv("RSS_EXPERT_DOMAIN", "example.org")

	if _, err := Load(); err != nil {
		t.Errorf("running without any env file failed: %v", err)
	}
}
