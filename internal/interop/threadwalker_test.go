package interop

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const walkerOutline = `Alice: A thread has to start *somewhere*.
	Bob: Only if someone answers.
		Alice: Three levels is enough to prove it walks.
	Alice: Someone did.`

var goldenRoutes = map[string]string{
	"/rss.xml":         "conversation-root.xml",
	"/p/1/replies.xml": "conversation-replies-1.xml",
	"/p/7/replies.xml": "conversation-replies-7.xml",
}

var fixtureHosts = []string{"https://alice.example", "https://bob.example"}

func repoPath(parts ...string) string {
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

func TestThreadwalkerWalksOurFeeds(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; the threadwalker gate needs it")
	}
	walkerDir := repoPath("tools", "threadwalker")
	if _, err := os.Stat(filepath.Join(walkerDir, "node_modules", "xml2js")); err != nil {
		t.Skip("run npm install in tools/threadwalker first")
	}

	srv := serveGolden(t)
	script := buildWalker(t, walkerDir, srv.URL+"/rss.xml", srv.URL+"/p/1")

	cmd := exec.Command(node, script)
	cmd.Env = append(os.Environ(),
		"NODE_TLS_REJECT_UNAUTHORIZED=0",
		"NODE_PATH="+filepath.Join(walkerDir, "node_modules"),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("threadwalker failed: %v\n%s", err, stderr.String())
	}

	got := strings.ReplaceAll(strings.TrimRight(string(out), "\r\n"), "\r\n", "\n")
	if got != walkerOutline {
		t.Errorf("the reference threadwalker read our conversation differently.\n--- got ---\n%s\n--- want ---\n%s", got, walkerOutline)
	}
}

func serveGolden(t *testing.T) *httptest.Server {
	t.Helper()

	bodies := make(map[string][]byte, len(goldenRoutes))
	for route, name := range goldenRoutes {
		raw, err := os.ReadFile(repoPath("testdata", "golden", name))
		if err != nil {
			t.Fatal(err)
		}
		bodies[route] = raw
	}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		w.Write(raw)
	}))
	t.Cleanup(srv.Close)

	for route, raw := range bodies {
		rewritten := string(raw)
		for _, host := range fixtureHosts {
			rewritten = strings.ReplaceAll(rewritten, host, srv.URL)
		}
		bodies[route] = []byte(rewritten)
	}
	return srv
}

func buildWalker(t *testing.T, dir, feedURL, guid string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(dir, "walker.js"))
	if err != nil {
		t.Fatal(err)
	}

	source := string(raw)
	for pattern, value := range map[string]string{
		`const urlStartingFeed = "[^"]*";`:  `const urlStartingFeed = "` + feedURL + `";`,
		`const guidStartingPost = "[^"]*";`: `const guidStartingPost = "` + guid + `";`,
	} {
		re := regexp.MustCompile(pattern)
		if len(re.FindAllString(source, -1)) != 1 {
			t.Fatalf("walker.js no longer declares %s exactly once; the vendored copy has drifted", pattern)
		}
		source = re.ReplaceAllLiteralString(source, value)
	}

	path := filepath.Join(t.TempDir(), "walker.js")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
