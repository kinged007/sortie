package pricing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sync"
	"time"
)

const (
	requestTimeout  = 2 * time.Minute
	maxBodyBytes    = 32 << 20
	maxRedirects    = 10
	refreshInterval = 24 * time.Hour
	firstRetryWait  = time.Minute
	maxRetryWait    = time.Hour
)

var (
	errInvalidAddress = errors.New("invalid address")
	errBodyTooLarge   = errors.New("response body exceeds 32 MiB")
)

// DefaultFetchURL is the address of the shared price table when
// price_table.fetch_url is unset.
const DefaultFetchURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// DownloadRole says which price table a [Download] feeds.
type DownloadRole string

const (
	// DownloadShared is the table at price_table.fetch_url, priced as
	// [SourceFetched].
	DownloadShared DownloadRole = "shared"

	// DownloadOperator is the table at price_table.replace_url, priced as
	// [SourceOperator].
	DownloadOperator DownloadRole = "operator"
)

// Download names one table the configuration wants downloaded.
type Download struct {
	Role DownloadRole
	URL  string
}

// storedTable is the document a last good copy file holds.
type storedTable struct {
	Source    string          `json:"source"`     // "sha256:" + lowercase hex SHA-256 of the address
	FetchedAt time.Time       `json:"fetched_at"` // end of the download that delivered the table
	Table     json.RawMessage `json:"table"`      // Table.Encode() of the table put in force
}

type tryResult int

const (
	tryAccepted tryResult = iota
	tryFailed
	tryDiscarded // the download left the wanted set or the run ended
)

// schedule is the timing state of one wanted download.
type schedule struct {
	failures  int         // consecutive failed tries
	failedAt  time.Time   // end of the latest failed try, zero when failures is 0
	errText   string      // error text of the latest failed try, "" when failures is 0
	due       bool        // a try is owed
	nextTryAt time.Time   // end of the armed wait, zero when none is armed
	stop      func() bool // stops the armed wait, nil when none is armed
}

// claim is the try the run loop is working on.
type claim struct {
	download Download
	schedule *schedule
	ctx      context.Context
	cancel   context.CancelFunc
}

// Downloader downloads the tables the configuration wants, validates them,
// keeps the last accepted copy of each beside the database, and serves them
// to [LoadRates] through [Downloader.Table]. Only [Downloader.Run] sends
// requests and writes copies.
type Downloader struct {
	dbPath string
	logger *slog.Logger
	client *http.Client

	// afterFunc arms a wait and returns its stop function. It MUST NOT run
	// fire before it returns.
	afterFunc func(wait time.Duration, fire func()) (stop func() bool)

	wake chan struct{}

	mu             sync.Mutex
	order          []Download             // wanted downloads in the order Want named them; guarded by mu
	schedules      map[Download]*schedule // wanted downloads only; guarded by mu
	tables         map[Download]*Table    // table in force, nil once a copy was looked for and not found; guarded by mu
	inflight       Download               // guarded by mu
	cancelInflight context.CancelFunc     // guarded by mu
}

// NewDownloader returns a Downloader that keeps its copies beside the
// database file at dbPath. It does no I/O. A nil logger means
// [slog.Default].
func NewDownloader(dbPath string, logger *slog.Logger) *Downloader {
	if logger == nil {
		logger = slog.Default()
	}
	return &Downloader{
		dbPath: dbPath,
		logger: logger,
		client: &http.Client{Timeout: requestTimeout},
		afterFunc: func(wait time.Duration, fire func()) func() bool {
			return time.AfterFunc(wait, fire).Stop
		},
		wake:      make(chan struct{}, 1),
		schedules: make(map[Download]*schedule),
		tables:    make(map[Download]*Table),
	}
}

// Table returns the last table accepted from dl.URL for dl.Role in this
// process, else the stored copy when it came from dl.URL, else nil. It reads
// the copy on the first call for dl and never sends a request. Safe for
// concurrent use.
func (d *Downloader) Table(dl Download) *Table {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tableLocked(dl)
}

// tableLocked is [Downloader.Table] for a caller that holds d.mu.
func (d *Downloader) tableLocked(dl Download) *Table {
	if table, looked := d.tables[dl]; looked {
		return table
	}
	table := d.readCopy(dl)
	d.tables[dl] = table
	return table
}

// DownloadState is what a [Downloader] holds about one download.
type DownloadState struct {
	// Table is what [Downloader.Table] returns for the download.
	Table *Table

	// NextTryAt is the end of the armed wait; zero while a try is due or
	// running, or when the download is not wanted.
	NextTryAt time.Time

	// Failures counts the consecutive failed tries since the last accepted
	// one.
	Failures int

	// FailedAt is the end of the latest failed try; zero when Failures is 0.
	FailedAt time.Time

	// Err is the error text of that try; empty when Failures is 0.
	Err string
}

// State reports the table, timing, and failures of dl without sending a
// request. A download that is not wanted reports its table with zero timing
// and failure fields, and a nil Downloader reports the zero value. Safe for
// concurrent use.
func (d *Downloader) State(dl Download) DownloadState {
	if d == nil {
		return DownloadState{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	state := DownloadState{Table: d.tableLocked(dl)}
	if s := d.schedules[dl]; s != nil {
		state.NextTryAt, state.Failures, state.FailedAt, state.Err = s.nextTryAt, s.failures, s.failedAt, s.errText
	}
	return state
}

func (d *Downloader) copyPath(role DownloadRole) string {
	return d.dbPath + ".price-table-" + string(role) + ".json"
}

func (d *Downloader) readCopy(dl Download) *Table {
	path := d.copyPath(dl.Role)
	data, err := os.ReadFile(path) //nolint:gosec // the path is the database path with a fixed suffix
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return d.ignoreCopy(path, err)
	}
	var stored storedTable
	if err := json.Unmarshal(data, &stored); err != nil {
		return d.ignoreCopy(path, err)
	}
	if stored.Source != addressDigest(dl.URL) {
		return nil
	}
	if stored.FetchedAt.IsZero() {
		return d.ignoreCopy(path, errors.New("fetched_at is missing"))
	}
	table, err := ParseTable(stored.Table, nil)
	if err != nil {
		return d.ignoreCopy(path, err)
	}
	return table.withRevision(table.Version(), stored.FetchedAt)
}

func (d *Downloader) ignoreCopy(path string, err error) *Table {
	d.logger.Warn("stored price table ignored", slog.String("path", path), slog.Any("error", err))
	return nil
}

func addressDigest(address string) string {
	sum := sha256.Sum256([]byte(address))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// store writes the copy of table atomically. A failure is logged and leaves
// the previous copy in place.
func (d *Downloader) store(dl Download, table *Table) {
	path := d.copyPath(dl.Role)
	err := writeCopy(path, storedTable{
		Source:    addressDigest(dl.URL),
		FetchedAt: table.AsOf().UTC(),
		Table:     table.Encode(),
	})
	if err != nil {
		d.logger.Warn("failed to store price table", slog.String("path", path), slog.Any("error", err))
	}
}

func writeCopy(path string, stored storedTable) error {
	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode price table copy: %w", err)
	}
	// A rename is atomic where a write in place is not, so a crash leaves
	// either the previous copy or the new one, never a torn file.
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) //nolint:gosec // the copy holds public prices
	if err != nil {
		return fmt.Errorf("write price table copy: %w", err)
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp) //nolint:errcheck,gosec // best-effort cleanup of the temporary file
		return fmt.Errorf("write price table copy: %w", err)
	}
	return nil
}

// try downloads dl once. The result is accepted only when a table entered
// force.
//
// The error is non-nil only when the result is [tryFailed].
func (d *Downloader) try(ctx context.Context, dl Download) (tryResult, error) {
	body, err := d.fetch(ctx, dl)
	if err != nil {
		return d.fail(ctx, dl, err)
	}
	receivedAt := time.Now().UTC()
	if !d.wants(dl) {
		return tryDiscarded, nil
	}

	baseline := d.Table(dl)
	if baseline == nil && dl.Role == DownloadShared {
		// The embedded table measures the first shared download whatever
		// price_table.embedded says; a load failure leaves no baseline.
		baseline, _ = EmbeddedTable()
	}
	parsed, err := ParseTable(body, baseline)
	if err != nil {
		return d.fail(ctx, dl, err)
	}
	table := parsed.withRevision(parsed.Version(), receivedAt)
	if !d.accept(dl, table) {
		return tryDiscarded, nil
	}

	d.store(dl, table)
	d.logger.LogAttrs(ctx, slog.LevelInfo, "price table downloaded",
		append(downloadAttrs(dl), slog.String("version", table.Version()))...)
	return tryAccepted, nil
}

// fail logs a failed try and returns the error it logged, unless ctx ended,
// which discards the try.
func (d *Downloader) fail(ctx context.Context, dl Download, err error) (tryResult, error) {
	if ctx.Err() != nil {
		return tryDiscarded, nil
	}
	d.logger.LogAttrs(ctx, slog.LevelWarn, "price table download failed",
		append(downloadAttrs(dl), slog.Any("error", err))...)
	return tryFailed, err
}

// fetch returns the body of dl.URL. No error it returns carries the address,
// which may hold credentials.
func (d *Downloader) fetch(ctx context.Context, dl Download) ([]byte, error) {
	address, ok := parseAddress(dl.URL)
	if !ok {
		return nil, errInvalidAddress
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dl.URL, nil)
	if err != nil {
		return nil, errInvalidAddress
	}

	client := *d.client
	client.CheckRedirect = nil
	if address.Scheme == "https" {
		client.CheckRedirect = refuseDowngrade
	}
	resp, err := client.Do(req)
	if err != nil {
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			return nil, urlErr.Err
		}
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck,gosec // nothing is lost by a failed close of a read body

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return nil, errBodyTooLarge
	}
	return body, nil
}

// refuseDowngrade keeps an https address authenticated by TLS end to end.
func refuseDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if req.URL.Scheme != "https" {
		return errors.New("redirect to a non-https address refused")
	}
	return nil
}

func parseAddress(raw string) (*url.URL, bool) {
	address, err := url.Parse(raw)
	if err != nil || address.Host == "" || (address.Scheme != "http" && address.Scheme != "https") {
		return nil, false
	}
	return address, true
}

// downloadAttrs names dl for a log line. The address contributes scheme,
// host, and path only, because userinfo and query may hold credentials.
func downloadAttrs(dl Download) []slog.Attr {
	attrs := []slog.Attr{slog.String("table", string(dl.Role))}
	if address := displayAddress(dl.URL); address != "" {
		attrs = append(attrs, slog.String("url", address))
	}
	return attrs
}

// displayAddress returns raw as scheme, host, and path only, because userinfo
// and query may hold credentials. It returns "" for an address that does not
// parse.
func displayAddress(raw string) string {
	address, ok := parseAddress(raw)
	if !ok {
		return ""
	}
	safe := url.URL{Scheme: address.Scheme, Host: address.Host, Path: address.Path}
	return safe.String()
}

func (d *Downloader) wants(dl Download) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, wanted := d.schedules[dl]
	return wanted
}

// accept puts table in force for dl unless dl left the wanted set.
func (d *Downloader) accept(dl Download, table *Table) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, wanted := d.schedules[dl]; !wanted {
		return false
	}
	d.tables[dl] = table
	return true
}

// Want replaces the set of downloads to keep fresh. A download newly named is
// tried at once; one no longer named is canceled, its in-flight result
// discarded. Naming the same set changes no wait. It never waits on the
// network and is safe for concurrent use.
func (d *Downloader) Want(downloads []Download) {
	next := make(map[Download]bool, len(downloads))
	for _, dl := range downloads {
		next[dl] = true
	}

	d.mu.Lock()
	for dl, s := range d.schedules {
		if next[dl] {
			continue
		}
		if s.stop != nil {
			s.stop()
		}
		delete(d.schedules, dl)
		if d.cancelInflight != nil && d.inflight == dl {
			d.cancelInflight()
		}
	}
	arrived := false
	for _, dl := range downloads {
		if _, known := d.schedules[dl]; !known {
			d.schedules[dl] = &schedule{due: true}
			arrived = true
		}
	}
	d.order = slices.Clone(downloads)
	d.mu.Unlock()

	if arrived {
		d.signal()
	}
}

func (d *Downloader) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run sends every request and writes every copy until ctx is done. After each
// accepted table enters force it calls landed, which MAY be nil, with no
// Downloader lock held. Run MUST be called at most once.
//
// A wanted download is tried at once, again after 24 hours once a try is
// accepted, and again after a wait that doubles from one minute to one hour
// while tries fail. Tries run one at a time.
func (d *Downloader) Run(ctx context.Context, landed func()) {
	defer d.stopTimers()
	for ctx.Err() == nil {
		c, ok := d.claimDue(ctx)
		if !ok {
			select {
			case <-ctx.Done():
			case <-d.wake:
			}
			continue
		}
		result, failure := d.try(c.ctx, c.download)
		d.release(c)
		if result == tryAccepted && landed != nil {
			landed()
		}
		d.settle(c, result, failure)
	}
}

// claimDue takes the first due download in the order Want named them.
func (d *Downloader) claimDue(ctx context.Context) (claim, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, dl := range d.order {
		s := d.schedules[dl]
		if s == nil || !s.due {
			continue
		}
		s.due = false
		tryCtx, cancel := context.WithCancel(ctx)
		d.inflight, d.cancelInflight = dl, cancel
		return claim{download: dl, schedule: s, ctx: tryCtx, cancel: cancel}, true
	}
	return claim{}, false
}

func (d *Downloader) release(c claim) {
	d.mu.Lock()
	d.inflight, d.cancelInflight = Download{}, nil
	d.mu.Unlock()
	c.cancel()
}

// settle records the outcome of a try and arms the wait that follows it,
// unless the download left the wanted set meanwhile. failure is the error of a
// failed try.
func (d *Downloader) settle(c claim, result tryResult, failure error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.schedules[c.download] != c.schedule {
		return
	}
	switch result {
	case tryAccepted:
		c.schedule.failures, c.schedule.failedAt, c.schedule.errText = 0, time.Time{}, ""
		d.arm(c, refreshInterval)
	case tryFailed:
		c.schedule.failures++
		c.schedule.failedAt, c.schedule.errText = time.Now().UTC(), failure.Error()
		d.arm(c, retryWait(c.schedule.failures))
	case tryDiscarded:
	}
}

// arm starts the wait after which c is due again. The caller MUST hold d.mu.
func (d *Downloader) arm(c claim, wait time.Duration) {
	c.schedule.nextTryAt = time.Now().Add(wait).UTC()
	c.schedule.stop = d.afterFunc(wait, func() {
		d.mu.Lock()
		if d.schedules[c.download] == c.schedule {
			c.schedule.due = true
			c.schedule.nextTryAt = time.Time{}
			c.schedule.stop = nil
		}
		d.mu.Unlock()
		d.signal()
	})
}

// retryWait is one minute after the first failed try, doubling per further
// failure up to one hour.
func retryWait(failures int) time.Duration {
	return min(firstRetryWait<<min(failures-1, 6), maxRetryWait)
}

func (d *Downloader) stopTimers() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.schedules {
		if s.stop != nil {
			s.stop()
			s.stop = nil
		}
	}
}
