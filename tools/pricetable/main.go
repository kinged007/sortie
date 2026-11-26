// Command pricetable refreshes the embedded price table snapshot to the
// latest upstream LiteLLM commit that changed the price file. It is a
// maintainer tool and never enters the shipped binary.
//
// Usage, from the repository root:
//
//	make price-table
//
// The tool takes no flag and no argument. It resolves the latest commit
// that changed model_prices_and_context_window.json through the GitHub
// API, downloads the file at that commit, and accepts it only when it
// passes the validation shared with runtime downloads against the snapshot
// it replaces. An optional GITHUB_TOKEN raises the API rate limit.
//
// The write is all-or-nothing: the price file, REVISION and REVISION_TIME
// replace their predecessors together, as the last step, so a revision
// always names the rates beside it. On any failure the snapshot is left
// as it was and the exit status is 1.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sortie-ai/sortie/internal/pricing"
)

const (
	outcomeRefreshed = "refreshed"
	outcomeUnchanged = "unchanged"
	outcomeKept      = "kept"
)

const (
	snapshotJSONName     = "model_prices_and_context_window.json"
	snapshotRevisionName = "REVISION"
	snapshotTimeName     = "REVISION_TIME"
)

const (
	commitsPath  = "/repos/BerriAI/litellm/commits?path=" + snapshotJSONName + "&per_page=1"
	contentsPath = "/repos/BerriAI/litellm/contents/" + snapshotJSONName + "?ref="

	commitsTimeout  = 30 * time.Second
	downloadTimeout = 120 * time.Second
	commitsLimit    = 1 << 20
	downloadLimit   = 64 << 20
	retryDelay      = 2 * time.Second
)

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type refresher struct {
	client  *http.Client
	apiBase string // "https://api.github.com"
	token   string
	dir     string // "internal/pricing/snapshot"
}

// trio is the three snapshot files, which change together or not at all.
type trio struct {
	json         []byte
	revision     string
	revisionTime string
}

type snapshotFile struct {
	name string
	body []byte
}

func (t trio) files() []snapshotFile {
	return []snapshotFile{
		{name: snapshotJSONName, body: t.json},
		{name: snapshotRevisionName, body: []byte(t.revision + "\n")},
		{name: snapshotTimeName, body: []byte(t.revisionTime + "\n")},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pricetable:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("takes no flag and no argument")
	}
	r := &refresher{
		client: &http.Client{
			Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
			// A redirect fails the run instead of fetching from a host the
			// endpoint did not name.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		apiBase: "https://api.github.com",
		token:   os.Getenv("GITHUB_TOKEN"),
		dir:     "internal/pricing/snapshot",
	}
	outcome, err := r.refresh(ctx)
	if err != nil {
		return err
	}
	current, err := r.read()
	if err != nil {
		return err
	}
	fmt.Printf("pricetable: %s revision %s committed %s\n", outcome, current.revision, current.revisionTime)
	return nil
}

func (r *refresher) refresh(ctx context.Context) (outcome string, err error) {
	current, err := r.read()
	if err != nil {
		return outcomeKept, err
	}
	previous, err := pricing.ParseTable(current.json, nil)
	if err != nil {
		return outcomeKept, fmt.Errorf("current snapshot: %w", err)
	}

	revision, asOf, err := r.latestRevision(ctx)
	if err != nil {
		return outcomeKept, err
	}
	if revision == current.revision {
		return outcomeUnchanged, nil
	}

	body, err := r.download(ctx, revision)
	if err != nil {
		return outcomeKept, err
	}
	// The gate applies the half-size rule against the current snapshot but
	// its table is discarded: zero-rate retention would make the written
	// file differ from the upstream file the revision names.
	if _, err := pricing.ParseTable(body, previous); err != nil {
		return outcomeKept, fmt.Errorf("revision %s: %w", revision, err)
	}
	table, err := pricing.ParseTable(body, nil)
	if err != nil {
		return outcomeKept, fmt.Errorf("revision %s: %w", revision, err)
	}

	next := trio{
		json:         table.Encode(),
		revision:     revision,
		revisionTime: asOf.Format(time.RFC3339),
	}
	if err := r.write(next, current); err != nil {
		return outcomeKept, err
	}
	return outcomeRefreshed, nil
}

// latestRevision returns the latest commit that changed the price file and
// that commit's committer time in UTC, so a revision and its time cannot
// disagree.
func (r *refresher) latestRevision(ctx context.Context) (sha string, asOf time.Time, err error) {
	body, err := r.get(ctx, commitsPath, "application/vnd.github+json", commitsTimeout, commitsLimit)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("resolve latest revision: %w", err)
	}
	var commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date string `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &commits); err != nil {
		return "", time.Time{}, fmt.Errorf("resolve latest revision: %w", err)
	}
	if len(commits) == 0 {
		return "", time.Time{}, errors.New("resolve latest revision: no commit changed the price file")
	}
	if !revisionPattern.MatchString(commits[0].SHA) {
		return "", time.Time{}, errors.New("resolve latest revision: sha is not 40 lowercase hex characters")
	}
	committed, err := time.Parse(time.RFC3339, commits[0].Commit.Committer.Date)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("resolve latest revision: committer date: %w", err)
	}
	return commits[0].SHA, committed.UTC(), nil
}

func (r *refresher) download(ctx context.Context, revision string) ([]byte, error) {
	body, err := r.get(ctx, contentsPath+revision, "application/vnd.github.raw+json", downloadTimeout, downloadLimit)
	if err != nil {
		return nil, fmt.Errorf("download revision %s: %w", revision, err)
	}
	return body, nil
}

// get makes one request and retries it once after retryDelay when the
// failure may be transient: a network error, a timeout, a 429 or a 5xx.
func (r *refresher) get(ctx context.Context, path, accept string, timeout time.Duration, limit int64) ([]byte, error) {
	body, transient, err := r.try(ctx, path, accept, timeout, limit)
	if err == nil || !transient {
		return body, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(retryDelay):
	}
	body, _, err = r.try(ctx, path, accept, timeout, limit)
	return body, err
}

func (r *refresher) try(ctx context.Context, path, accept string, timeout time.Duration, limit int64) (body []byte, transient bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.apiBase+path, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", accept)
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close() //nolint:errcheck // response body already read or abandoned

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500,
			fmt.Errorf("status %d", resp.StatusCode)
	}
	// One byte past the limit tells an oversized body from one that fits
	// exactly; a truncated copy must never reach validation.
	body, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, true, err
	}
	if int64(len(body)) > limit {
		return nil, false, fmt.Errorf("body exceeds %d bytes", limit)
	}
	return body, false, nil
}

func (r *refresher) read() (trio, error) {
	var current trio
	var err error
	if current.json, err = r.readFile(snapshotJSONName); err != nil {
		return trio{}, err
	}
	revision, err := r.readFile(snapshotRevisionName)
	if err != nil {
		return trio{}, err
	}
	revisionTime, err := r.readFile(snapshotTimeName)
	if err != nil {
		return trio{}, err
	}
	current.revision = strings.TrimSpace(string(revision))
	current.revisionTime = strings.TrimSpace(string(revisionTime))
	return current, nil
}

func (r *refresher) readFile(name string) ([]byte, error) {
	body, err := os.ReadFile(filepath.Join(r.dir, name)) //nolint:gosec // G304: name is one of the fixed snapshot file names
	if err != nil {
		return nil, fmt.Errorf("read snapshot (run from the repository root): %w", err)
	}
	return body, nil
}

// write replaces the snapshot with next. Every file is staged before any
// target is replaced, so a full or read-only disk fails while the snapshot
// is still untouched; a failed replacement restores prior.
func (r *refresher) write(next, prior trio) error {
	files := next.files()
	staged := make([]string, len(files))
	defer func() {
		for _, path := range staged {
			if path != "" {
				_ = os.Remove(path)
			}
		}
	}()
	for i, f := range files {
		path, err := r.stage(f)
		if err != nil {
			return err
		}
		staged[i] = path
	}
	for i, f := range files {
		if err := os.Rename(staged[i], filepath.Join(r.dir, f.name)); err != nil {
			return errors.Join(fmt.Errorf("replace %s: %w", f.name, err), r.restore(prior.files()[:i]))
		}
		staged[i] = ""
	}
	return nil
}

func (r *refresher) restore(files []snapshotFile) error {
	var errs []error
	for _, f := range files {
		path, err := r.stage(f)
		if err == nil {
			if err = os.Rename(path, filepath.Join(r.dir, f.name)); err != nil {
				_ = os.Remove(path)
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", f.name, err))
		}
	}
	return errors.Join(errs...)
}

// stage writes f to a temporary file beside its target, because rename is
// atomic only within one filesystem.
func (r *refresher) stage(f snapshotFile) (string, error) {
	tmp, err := os.CreateTemp(r.dir, f.name+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("stage %s: %w", f.name, err)
	}
	_, err = tmp.Write(f.body)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("stage %s: %w", f.name, err)
	}
	return tmp.Name(), nil
}
