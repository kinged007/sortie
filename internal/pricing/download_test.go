package pricing

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
)

const fixtureRequestURI = "/prices.json?token=secret"

type seenRequest struct {
	method  string
	uri     string
	header  http.Header
	bodyLen int
}

type armedWait struct {
	wait time.Duration
	fire func()
}

type fixture struct {
	d     *Downloader
	srv   *httptest.Server
	dl    Download
	seen  chan seenRequest
	armed chan armedWait
	stop  func()
}

func newFixture(t *testing.T, dbPath string, serve func(n int, w http.ResponseWriter)) *fixture {
	t.Helper()

	f := &fixture{seen: make(chan seenRequest, 32), armed: make(chan armedWait, 32)}
	var requests atomic.Int32
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.seen <- seenRequest{method: r.Method, uri: r.URL.RequestURI(), header: r.Header.Clone(), bodyLen: len(body)}
		serve(int(requests.Add(1)), w)
	}))
	t.Cleanup(f.srv.Close)

	f.dl = Download{Role: DownloadShared, URL: f.srv.URL + fixtureRequestURI}
	f.d = NewDownloader(dbPath, slog.New(slog.DiscardHandler))
	f.d.client = f.srv.Client()
	f.d.afterFunc = func(wait time.Duration, fire func()) func() bool {
		f.armed <- armedWait{wait: wait, fire: fire}
		return func() bool { return true }
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.d.Run(ctx, nil)
	}()
	f.stop = sync.OnceFunc(func() {
		cancel()
		<-done
	})
	t.Cleanup(f.stop)
	return f
}

func (f *fixture) drain() []seenRequest {
	var requests []seenRequest
	for {
		select {
		case r := <-f.seen:
			requests = append(requests, r)
		default:
			return requests
		}
	}
}

func dbPath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "sortie.db")
}

func serveBody(body []byte) func(int, http.ResponseWriter) {
	return func(_ int, w http.ResponseWriter) { _, _ = w.Write(body) }
}

func addModel(name string) func(map[string]map[string]any) {
	return func(doc map[string]map[string]any) {
		doc[name] = map[string]any{"litellm_provider": "acme", "input_cost_per_token": 1e-6, "output_cost_per_token": 5e-6}
	}
}

func tableBody(t *testing.T, edits ...func(map[string]map[string]any)) []byte {
	t.Helper()

	embedded, err := EmbeddedTable()
	if err != nil {
		t.Fatalf("EmbeddedTable() = %v, want nil", err)
	}
	var doc map[string]map[string]any
	if err := json.Unmarshal(embedded.Encode(), &doc); err != nil {
		t.Fatalf("decoding the embedded table: %v", err)
	}
	for _, edit := range edits {
		edit(doc)
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encoding the table: %v", err)
	}
	return body
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

func TestDownloadKeepsTableInForceOnBadResponse(t *testing.T) {
	t.Parallel()

	good := tableBody(t, addModel("fetched-only-model"))
	other := tableBody(t, addModel("other-model"))

	tests := []struct {
		name  string
		serve func(w http.ResponseWriter)
	}{
		{"body ParseTable rejects", func(w http.ResponseWriter) { _, _ = w.Write(good[:len(good)/2]) }},
		{"status 500", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(other)
		}},
		{"body over 32 MiB", func(w http.ResponseWriter) {
			_, _ = w.Write(other)
			padding := bytes.Repeat([]byte(" "), 1<<20)
			for range maxBodyBytes/len(padding) + 1 {
				_, _ = w.Write(padding)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := dbPath(t)
			f := newFixture(t, path, func(n int, w http.ResponseWriter) {
				if n == 1 {
					_, _ = w.Write(good)
					return
				}
				tt.serve(w)
			})
			f.d.Want([]Download{f.dl})

			accepted := <-f.armed
			before := f.d.Table(f.dl)
			if before == nil {
				t.Fatalf("Table(%v) after the first download = nil, want the accepted table", f.dl.Role)
			}
			copyBefore := mustReadFile(t, f.d.copyPath(f.dl.Role))
			accepted.fire()
			failed := <-f.armed

			after := f.d.Table(f.dl)
			if after.Version() != before.Version() || !after.AsOf().Equal(before.AsOf()) {
				t.Errorf("Table(%v) = %q as of %v, want %q as of %v", f.dl.Role, after.Version(), after.AsOf(), before.Version(), before.AsOf())
			}
			if copyAfter := mustReadFile(t, f.d.copyPath(f.dl.Role)); !bytes.Equal(copyAfter, copyBefore) {
				t.Errorf("copy after the %s changed, want it unchanged", tt.name)
			}
			if failed.wait != time.Minute {
				t.Errorf("wait after the %s = %v, want %v", tt.name, failed.wait, time.Minute)
			}
		})
	}
}

func TestDownloadFirstSharedMeasuredAgainstEmbedded(t *testing.T) {
	t.Parallel()

	embedded, err := EmbeddedTable()
	if err != nil {
		t.Fatalf("EmbeddedTable() = %v, want nil", err)
	}
	sonnet, ok := embedded.Lookup("claude-sonnet-4-5")
	if !ok || sonnet.InputPerToken == nil || *sonnet.InputPerToken == 0 {
		t.Fatalf("embedded Lookup(claude-sonnet-4-5) = %+v, want a priced entry", sonnet)
	}

	tests := []struct {
		name         string
		body         []byte
		wantAccepted bool
	}{
		{
			name: "zeroed rate keeps the embedded rate and the rest is taken",
			body: tableBody(t, addModel("fetched-only-model"), func(doc map[string]map[string]any) {
				doc[sonnet.Key]["input_cost_per_token"] = 0.0
			}),
			wantAccepted: true,
		},
		{
			name: "a table far smaller than the embedded one is rejected",
			body: []byte(`{"fetched-only-model": {"litellm_provider": "acme", "input_cost_per_token": 0.000001}}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, dbPath(t), serveBody(tt.body))
			f.d.Want([]Download{f.dl})
			<-f.armed

			table := f.d.Table(f.dl)
			if !tt.wantAccepted {
				if table != nil {
					t.Fatalf("Table(%v) = %q, want nil", f.dl.Role, table.Version())
				}
				return
			}
			if table == nil {
				t.Fatalf("Table(%v) = nil, want the accepted table", f.dl.Role)
			}
			kept, _ := table.Lookup(sonnet.Key)
			if kept.InputPerToken == nil || *kept.InputPerToken != *sonnet.InputPerToken {
				t.Errorf("Lookup(%q).InputPerToken = %v, want %v", sonnet.Key, kept.InputPerToken, *sonnet.InputPerToken)
			}
			if _, ok := table.Lookup("fetched-only-model"); !ok {
				t.Errorf("Lookup(fetched-only-model) = false, want true")
			}
		})
	}
}

func TestDownloadCopySurvivesRestart(t *testing.T) {
	t.Parallel()

	path := dbPath(t)
	f := newFixture(t, path, serveBody(tableBody(t, addModel("fetched-only-model"))))
	f.d.Want([]Download{f.dl})
	<-f.armed
	first := f.d.Table(f.dl)
	if first == nil {
		t.Fatalf("Table(%v) after the download = nil, want the accepted table", f.dl.Role)
	}
	f.srv.Close()

	restarted := NewDownloader(path, slog.New(slog.DiscardHandler))
	state := restarted.State(f.dl)
	if state.Table == nil || state.Table.Version() != first.Version() || !state.Table.AsOf().Equal(first.AsOf()) || state.Failures != 0 || !state.NextTryAt.IsZero() {
		t.Errorf("State(%v) after a restart = %+v, want the stored copy %q as of %v, no failures, no next try", f.dl.Role, state, first.Version(), first.AsOf())
	}
	got := restarted.Table(f.dl)
	if got == nil {
		t.Fatalf("Table(%v) after a restart = nil, want the stored copy", f.dl.Role)
	}
	if got.Version() != first.Version() || !got.AsOf().Equal(first.AsOf()) {
		t.Errorf("Table(%v) after a restart = %q as of %v, want %q as of %v", f.dl.Role, got.Version(), got.AsOf(), first.Version(), first.AsOf())
	}

	ext := map[string]any{"price_table": map[string]any{"fetch_url": f.dl.URL}}
	rates, _, err := loadRates(t, ext, LoadInput{IsKind: kindsOf(), Downloaded: restarted.Table})
	if err != nil {
		t.Fatalf("LoadRates(%v) = %v, want nil", ext, err)
	}
	usage := domain.TokenUsage{InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000}
	part := PriceRun(spendOnce("fetched-only-model", usage), rates).Parts["fetched-only-model"]
	if part.Source != SourceFetched || part.TableVersion != first.Version() {
		t.Errorf("PriceRun(fetched-only-model) = source %q version %q, want %q version %q", part.Source, part.TableVersion, SourceFetched, first.Version())
	}

	copyPath := restarted.copyPath(f.dl.Role)
	stored := mustReadFile(t, copyPath)
	if err := os.WriteFile(copyPath, stored[:len(stored)/2], 0o600); err != nil {
		t.Fatalf("truncating the copy: %v", err)
	}
	if table := NewDownloader(path, slog.New(slog.DiscardHandler)).Table(f.dl); table != nil {
		t.Errorf("Table(%v) over a truncated copy = %q, want nil", f.dl.Role, table.Version())
	}
}

func TestDownloadRequestCarriesNothingFromTheWorkflow(t *testing.T) {
	t.Parallel()

	f := newFixture(t, dbPath(t), serveBody(tableBody(t)))
	f.d.Want([]Download{f.dl})

	req := <-f.seen

	if req.method != http.MethodGet || req.bodyLen != 0 || req.uri != fixtureRequestURI {
		t.Errorf("request = %s %s with a %d byte body, want GET %s with an empty body", req.method, req.uri, req.bodyLen, fixtureRequestURI)
	}
	for name := range req.header {
		if !slices.Contains([]string{"User-Agent", "Accept-Encoding"}, name) {
			t.Errorf("request header %q, want only User-Agent and Accept-Encoding", name)
		}
	}
}

func TestDownloadEmptyWantSendsNoRequest(t *testing.T) {
	t.Parallel()

	f := newFixture(t, dbPath(t), serveBody(tableBody(t)))
	f.d.Want(nil)
	f.stop()

	if requests := f.drain(); len(requests) != 0 || len(f.armed) != 0 {
		t.Errorf("requests = %d, timers = %d, want none of either", len(requests), len(f.armed))
	}
}

func TestDownloadSchedule(t *testing.T) {
	t.Parallel()

	good := tableBody(t)
	f := newFixture(t, dbPath(t), func(n int, w http.ResponseWriter) {
		if n <= 8 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(good)
	})
	want := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute,
		time.Hour, time.Hour, 24 * time.Hour,
	}
	var got []time.Duration

	f.d.Want([]Download{f.dl})
	for i := range want {
		armed := <-f.armed
		got = append(got, armed.wait)
		if requests := f.drain(); len(requests) != 1 {
			t.Fatalf("requests before wait %d = %d, want 1", i+1, len(requests))
		}
		if i < len(want)-1 {
			armed.fire()
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("waits = %v, want %v", got, want)
	}

	moved := Download{Role: DownloadShared, URL: f.dl.URL + "&v=2"}
	f.d.Want([]Download{moved})
	<-f.armed

	requests := f.drain()
	if len(requests) != 1 || requests[0].uri != fixtureRequestURI+"&v=2" {
		t.Errorf("requests after Want of a new address = %+v, want one for %s", requests, fixtureRequestURI+"&v=2")
	}
}

func TestDownloadWantAndTableDoNotWaitOnRequest(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	f := newFixture(t, dbPath(t), func(_ int, w http.ResponseWriter) { <-release })
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)

	f.d.Want([]Download{f.dl})
	<-f.seen

	f.d.Want([]Download{f.dl})
	if table := f.d.Table(f.dl); table != nil {
		t.Errorf("Table(%v) while the request is held = %q, want nil", f.dl.Role, table.Version())
	}
	releaseOnce()
	<-f.armed
}
