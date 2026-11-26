package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	currentRevision  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	upstreamRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	priceFileName    = "model_prices_and_context_window.json"
)

type fileHandler func(w http.ResponseWriter, ref string)

func priceJSON(rates ...string) string {
	members := make([]string, len(rates))
	for i, rate := range rates {
		members[i] = fmt.Sprintf(`"m%d":{"litellm_provider":"openai","input_cost_per_token":%s}`, i, rate)
	}
	return "{" + strings.Join(members, ",") + "}"
}

func commitsBody(sha, committerDate string) string {
	return fmt.Sprintf(`[{"sha":%s,"commit":{"author":{"date":"2026-09-21T19:08:16Z"},"committer":{"date":%q}}}]`, sha, committerDate)
}

var upstreamCommits = commitsBody(strconv.Quote(upstreamRevision), "2026-09-21T19:10:33Z")

func serveFile(body string) fileHandler {
	return func(w http.ResponseWriter, _ string) { _, _ = w.Write([]byte(body)) }
}

func newRefresher(t *testing.T, current, commits string, file fileHandler) *refresher {
	t.Helper()

	dir := t.TempDir()
	seed := map[string]string{
		priceFileName:   current,
		"REVISION":      currentRevision + "\n",
		"REVISION_TIME": "2026-01-01T00:00:00Z\n",
	}
	for name, body := range seed {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/BerriAI/litellm/commits":
			_, _ = w.Write([]byte(commits))
		case "/repos/BerriAI/litellm/contents/" + priceFileName:
			file(w, r.URL.Query().Get("ref"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return &refresher{client: srv.Client(), apiBase: srv.URL, dir: dir}
}

func readDir(t *testing.T, dir string) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	files := make(map[string]string, len(entries))
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", e.Name(), err)
		}
		files[e.Name()] = string(body)
	}
	return files
}

func assertKept(t *testing.T, r *refresher) {
	t.Helper()

	before := readDir(t, r.dir)

	if _, err := r.refresh(t.Context()); err == nil {
		t.Fatal("refresh() error = nil, want error")
	}

	if got := readDir(t, r.dir); !maps.Equal(got, before) {
		t.Errorf("snapshot after a failed refresh = %q, want %q", got, before)
	}
}

func TestRefreshPairsRevisionWithItsCommitterTime(t *testing.T) {
	t.Parallel()

	file := func(w http.ResponseWriter, ref string) {
		if ref != upstreamRevision {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(priceJSON("2e-6")))
	}
	r := newRefresher(t, priceJSON("1e-6"), commitsBody(strconv.Quote(upstreamRevision), "2026-09-21T12:10:33-07:00"), file)

	outcome, err := r.refresh(t.Context())
	if err != nil {
		t.Fatalf("refresh() error = %v, want nil", err)
	}

	if outcome != "refreshed" {
		t.Errorf("refresh() outcome = %q, want %q", outcome, "refreshed")
	}
	files := readDir(t, r.dir)
	if got, want := files["REVISION"], upstreamRevision+"\n"; got != want {
		t.Errorf("REVISION = %q, want %q", got, want)
	}
	if got, want := files["REVISION_TIME"], "2026-09-21T19:10:33Z\n"; got != want {
		t.Errorf("REVISION_TIME = %q, want %q", got, want)
	}
}

func TestRefreshWritesUpstreamRatesWithoutZeroRetention(t *testing.T) {
	t.Parallel()

	r := newRefresher(t, priceJSON("1e-6"), upstreamCommits, serveFile(priceJSON("0")))

	if _, err := r.refresh(t.Context()); err != nil {
		t.Fatalf("refresh() error = %v, want nil", err)
	}

	var written map[string]struct {
		Input *float64 `json:"input_cost_per_token"`
	}
	if err := json.Unmarshal([]byte(readDir(t, r.dir)[priceFileName]), &written); err != nil {
		t.Fatalf("decode written snapshot: %v", err)
	}
	if got := written["m0"].Input; got == nil || *got != 0 {
		t.Errorf("written rate of m0 = %v, want 0", got)
	}
}

func TestRefreshKeepsSnapshotWhenUpstreamIsRejected(t *testing.T) {
	t.Parallel()

	valid := priceJSON("1", "1", "1", "1")
	tests := []struct {
		name    string
		current string
		commits string
		file    fileHandler
	}{
		{"candidate rates under half the current entries", valid, upstreamCommits, serveFile(priceJSON("1"))},
		{"truncated download", valid, upstreamCommits, serveFile(valid[:len(valid)/2])},
		{"server error on both tries", valid, upstreamCommits, func(w http.ResponseWriter, _ string) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(valid))
		}},
		{"null commit sha", valid, commitsBody("null", "2026-09-21T19:10:33Z"), serveFile(priceJSON("2", "2", "2", "2"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assertKept(t, newRefresher(t, tt.current, tt.commits, tt.file))
		})
	}
}

func TestRefreshKeepsSnapshotWhenDirectoryIsReadOnly(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not bind this user")
	}
	r := newRefresher(t, priceJSON("1e-6"), upstreamCommits, serveFile(priceJSON("2e-6")))
	if err := os.Chmod(r.dir, 0o555); err != nil {
		t.Fatalf("Chmod(%q): %v", r.dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.dir, 0o755) })

	assertKept(t, r)
}
