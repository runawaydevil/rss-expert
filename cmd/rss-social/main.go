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

var version = "0.0.1-dev"

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
	fmt.Fprint(w, `rss-social -- a social reader for the open web

usage: rss-social <command> [flags]

  serve        run the instance
  migrate      apply pending schema migrations
  doctor       check this installation and report what is wrong
  healthcheck  probe the admin readiness endpoint; exits non-zero if unhealthy
  version      print version and build information

Configuration is read from the environment:

  RSS_SOCIAL_DOMAIN         public hostname; required to serve
  RSS_SOCIAL_LISTEN         application address       (default :11080)
  RSS_SOCIAL_ADMIN_LISTEN   admin address             (default 127.0.0.1:11090)
  RSS_SOCIAL_DATA_DIR       database and attachments  (default data)
  RSS_SOCIAL_SMTP_URL       outgoing mail
  RSS_SOCIAL_LOG_FORMAT     text or json              (default text)
  RSS_SOCIAL_LOG_LEVEL      debug, info, warn, error  (default info)
`)
}

func versionString() string {
	revision, modified := buildRevision()
	s := fmt.Sprintf("rss-social %s %s/%s %s", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
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
