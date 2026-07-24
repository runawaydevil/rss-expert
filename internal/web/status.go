package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/runawaydevil/rss-expert/internal/backup"
)

type gauge struct {
	Label  string
	Value  string
	Detail string
	Bad    bool
}

func (a *App) statusPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	readings := []gauge{
		{Label: "version", Value: a.ops.version},
		{Label: "up for", Value: since(builtAt)},
		{Label: "go", Value: runtime.Version() + ", " + fmt.Sprint(runtime.NumGoroutine()) + " goroutines"},
		{Label: "heap", Value: mib(heapInUse()), Detail: "live objects plus the spans the runtime keeps ready to reuse"},
	}

	if reason := a.ops.notReady(ctx); reason != "" {
		readings = append(readings, gauge{Label: "ready", Value: "no", Detail: reason, Bad: true})
	} else {
		readings = append(readings, gauge{Label: "ready", Value: "yes", Detail: "database answering, schema current"})
	}

	if size, err := a.db.SizeOnDisk(); err == nil {
		readings = append(readings, gauge{Label: "database", Value: mib(size), Detail: "including the write-ahead log"})
	}

	if depth, err := a.queue.Depth(ctx); err == nil {
		readings = append(readings, gauge{
			Label:  "queue",
			Value:  fmt.Sprintf("%d waiting, %d running", depth.Ready, depth.Leased),
			Detail: fmt.Sprintf("%d gave up and need a human", depth.DeadLetter),
			Bad:    depth.DeadLetter > 0,
		})
	}

	if payloads, observations, items, err := a.sources.Counts(ctx); err == nil {
		readings = append(readings, gauge{
			Label:  "store",
			Value:  fmt.Sprintf("%d items", items),
			Detail: fmt.Sprintf("%d readings over %d stored payloads", observations, payloads),
		})
	}

	failing, err := a.sources.Failing(ctx, 10)
	if err != nil {
		a.log.Error("could not list failing sources", "error", err)
	}

	a.render(w, r, "status.html", map[string]any{
		"Title":    "Status — RSS Expert",
		"Readings": readings,
		"Failing":  failing,
		"Metrics":  a.ops.token != "",
		"Backups":  a.backups(),
		"CanBack":  a.dataDir != "",
		"Problem":  r.URL.Query().Get("problem"),
	})
}

func since(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d d", int(d.Hours()/24))
}

func mib(n int64) string {
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

func heapInUse() int64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapInuse)
}

func (a *App) takeBackup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Redirect(w, r, "/admin/status", http.StatusSeeOther)
		return
	}
	if a.dataDir == "" {
		a.log.Error("a backup was asked for but this instance has no data directory configured")
		http.Redirect(w, r, "/admin/status", http.StatusSeeOther)
		return
	}

	stamp := time.Now().UTC().Format("2006-01-02-1504")
	into := filepath.Join(a.dataDir, "backups", stamp)

	manifest, err := backup.TakeWithMedia(r.Context(), a.db, into, a.ops.version, filepath.Join(a.dataDir, "media"))
	if err != nil {
		a.log.Error("backup failed", "into", into, "error", err)
		http.Redirect(w, r, "/admin/status?problem="+urlEscape(err.Error()), http.StatusSeeOther)
		return
	}

	a.log.Info("backup taken", "into", into, "files", len(manifest.Files), "uploads", len(manifest.Media),
		"by", accountFrom(r.Context()).Email)
	http.Redirect(w, r, "/admin/status", http.StatusSeeOther)
}

func (a *App) backups() []gauge {
	if a.dataDir == "" {
		return nil
	}

	entries, err := os.ReadDir(filepath.Join(a.dataDir, "backups"))
	if err != nil {
		return nil
	}

	out := make([]gauge, 0, len(entries))
	for i := len(entries) - 1; i >= 0 && len(out) < 10; i-- {
		if !entries[i].IsDir() {
			continue
		}
		where := filepath.Join(a.dataDir, "backups", entries[i].Name())
		taken, err := backup.Verify(where)
		if err != nil {
			out = append(out, gauge{Label: entries[i].Name(), Value: "damaged", Detail: err.Error(), Bad: true})
			continue
		}

		var total int64
		for _, file := range append(append([]backup.File{}, taken.Files...), taken.Media...) {
			total += file.Bytes
		}
		out = append(out, gauge{
			Label:  entries[i].Name(),
			Value:  mib(total),
			Detail: fmt.Sprintf("schema %d, %d uploads, verified just now", taken.SchemaVersion, len(taken.Media)),
		})
	}
	return out
}
