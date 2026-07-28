package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
)

var version = "0.0.1"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command, args := os.Args[1], os.Args[2:]

	var err error
	switch command {
	case "serve":
		err = serve(ctx, args)
	case "migrate":
		err = migrate(ctx, args)
	case "doctor":
		err = doctor(ctx, args)
	case "sources":
		err = sources(ctx, args)
	case "backup":
		err = backupCommand(ctx, args)
	case "restore":
		err = restoreCommand(ctx, args)
	case "healthcheck":
		err = healthcheck(ctx, args)
	case "version":
		fmt.Println(versionString())
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", command)
		usage(os.Stderr)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `rss-expert -- a social reader for the open web

usage: rss-expert <command> [flags]

  serve        run the instance
  migrate      apply pending schema migrations
  sources      list, add, read and remove the feeds this instance follows
  backup       write a verified snapshot to a directory
  restore      restore a snapshot, or --check one without touching anything
  doctor       check this installation and report what is wrong
  healthcheck  probe the admin readiness endpoint; exits non-zero if unhealthy
  version      print version and build information

Configuration comes from .env.local, then .env, then the real environment,
which wins over both:

  RSS_EXPERT_DOMAIN         public hostname; required to serve
  RSS_EXPERT_LISTEN         application address        (default :11080)
  RSS_EXPERT_DATA_DIR       database and attachments   (default data)
  RSS_EXPERT_ENV_FILE       read this file instead of the two above
  RSS_EXPERT_SMTP_URL       mail server for links and confirmation
  RSS_EXPERT_MAIL_FROM      the From address on those messages
  RSS_EXPERT_REGISTRATION   closed, invite or open      (default closed)
  RSS_EXPERT_ADMIN_EMAIL    owner account, created on first boot only
  RSS_EXPERT_ADMIN_PASSWORD owner password, hashed at once and never stored
  RSS_EXPERT_BEHIND_PROXY   trust X-Forwarded-* headers (default false)
  RSS_EXPERT_MEDIA_QUOTA_MB uploads kept per account   (default 512)
  RSS_EXPERT_FETCH_LIMIT_MB ceiling on a single fetch  (default 5)
  RSS_EXPERT_POLL_WORKERS   feeds fetched at once      (default 4)
  RSS_EXPERT_SHOW_PREVIEW   serve /dev/preview         (default false)
  RSS_EXPERT_LOG_FORMAT     text or json               (default text)
  RSS_EXPERT_LOG_LEVEL      debug, info, warn, error   (default info)
`)
}

func versionString() string {
	revision, modified := buildRevision()
	s := fmt.Sprintf("rss-expert %s %s/%s %s", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	if revision != "" {
		s += " " + revision
		if modified {
			s += "-dirty"
		}
	}
	return s
}

func buildRevision() (string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	var revision string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
			if len(revision) > 12 {
				revision = revision[:12]
			}
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return revision, modified
}
