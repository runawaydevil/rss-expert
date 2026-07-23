package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/runawaydevil/rss-social/internal/config"
	"github.com/runawaydevil/rss-social/internal/store"
)

type check struct {
	name   string
	detail string
	err    error
}

func doctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, cfgErr := config.Load()
	checks := []check{{name: "configuration", detail: describeConfig(cfg), err: cfgErr}}
	if cfgErr == nil {
		checks = append(checks, checkAll(ctx, cfg)...)
	}

	var failed int
	for _, c := range checks {
		switch {
		case c.err != nil:
			failed++
			fmt.Printf("FAIL  %-16s %v\n", c.name, c.err)
		default:
			fmt.Printf("ok    %-16s %s\n", c.name, c.detail)
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d of %d checks failed", failed, len(checks))
	}
	return nil
}

func checkAll(ctx context.Context, cfg config.Config) []check {
	checks := []check{
		checkDataDir(cfg),
		{name: "serving", detail: "domain " + cfg.Domain, err: cfg.RequireServing()},
	}

	if running, detail := instanceIsRunning(ctx, cfg); running {
		checks = append(checks, check{name: "instance", detail: detail})
	} else {
		checks = append(checks,
			check{name: "instance", detail: "not running"},
			checkListener("app port", cfg.Listen),
			checkListener("admin port", cfg.AdminListen),
		)
	}

	db, err := store.Open(ctx, cfg.DatabasePath())
	if err != nil {
		return append(checks, check{name: "database", err: err})
	}
	defer db.Close()

	size, _ := db.SizeOnDisk()
	checks = append(checks, check{
		name:   "database",
		detail: fmt.Sprintf("%s, %.1f MiB on disk", db.Path, float64(size)/(1<<20)),
	})

	writerPragmas, werr := db.Pragmas(ctx, db.Write)
	readerPragmas, rerr := db.Pragmas(ctx, db.Read)
	checks = append(checks,
		check{name: "writer pragmas", detail: describePragmas(writerPragmas), err: werr},
		check{name: "reader pragmas", detail: describePragmas(readerPragmas), err: rerr},
		checkPragmaValues(writerPragmas),
	)

	state, err := db.MigrationState(ctx)
	if err != nil {
		return append(checks, check{name: "migrations", err: err})
	}
	if !state.UpToDate() {
		return append(checks, check{
			name: "migrations",
			err:  fmt.Errorf("schema is at %d, latest is %d; run: rss-social migrate", state.Applied, state.Latest),
		})
	}
	return append(checks, check{
		name:   "migrations",
		detail: fmt.Sprintf("schema at version %d, nothing pending", state.Applied),
	})
}

func checkDataDir(cfg config.Config) check {
	c := check{name: "data directory", detail: cfg.DataDir}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		c.err = err
		return c
	}
	probe := filepath.Join(cfg.DataDir, ".write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		c.err = fmt.Errorf("not writable: %w", err)
		return c
	}
	os.Remove(probe)
	c.detail += ", writable"
	return c
}

func instanceIsRunning(ctx context.Context, cfg config.Config) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	url := "http://" + dialableAddress(cfg.AdminListen) + "/readyz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, ""
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<12))

	if resp.StatusCode == http.StatusOK {
		return true, "running and ready, admin on " + cfg.AdminListen
	}
	return true, fmt.Sprintf("running but not ready, admin on %s returned %d", cfg.AdminListen, resp.StatusCode)
}

func checkListener(name, address string) check {
	c := check{name: name, detail: address}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		c.err = fmt.Errorf("cannot bind %s: %w", address, err)
		return c
	}
	ln.Close()
	c.detail += ", free"
	return c
}

func checkPragmaValues(pragmas map[string]string) check {
	c := check{name: "durability"}
	if pragmas["journal_mode"] != "wal" {
		c.err = fmt.Errorf("journal_mode is %q, want wal", pragmas["journal_mode"])
		return c
	}
	if pragmas["foreign_keys"] != "1" {
		c.err = errors.New("foreign_keys is off")
		return c
	}
	c.detail = "wal on, foreign keys enforced"
	return c
}

func describeConfig(cfg config.Config) string {
	return fmt.Sprintf("%s/%s, %s, log %s/%s",
		runtime.GOOS, runtime.GOARCH, versionString(), cfg.LogFormat, cfg.LogLevel)
}

func describePragmas(pragmas map[string]string) string {
	keys := make([]string, 0, len(pragmas))
	for k := range pragmas {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+pragmas[k])
	}
	return strings.Join(parts, " ")
}
