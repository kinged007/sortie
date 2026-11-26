package pricing

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "embed" // go:embed of the snapshot files below
)

// The snapshot files are written only by the pricetable generator, so the
// revision always names the rates beside it.
var (
	//go:embed snapshot/model_prices_and_context_window.json
	snapshotJSON []byte

	//go:embed snapshot/REVISION
	snapshotRevision string

	//go:embed snapshot/REVISION_TIME
	snapshotRevisionTime []byte
)

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

var loadEmbeddedOnce = sync.OnceValues(loadEmbedded)

// EmbeddedTable returns the table compiled into the binary.
//
// Version is the pinned upstream revision and AsOf is the time that
// revision was committed. The snapshot is parsed on the first call only;
// every call returns the same table or the same error.
func EmbeddedTable() (*Table, error) {
	return loadEmbeddedOnce()
}

func loadEmbedded() (*Table, error) {
	table, err := ParseTable(snapshotJSON, nil)
	if err != nil {
		return nil, fmt.Errorf("embedded price table: %w", err)
	}
	revision := strings.TrimSpace(snapshotRevision)
	if !revisionPattern.MatchString(revision) {
		return nil, errors.New("embedded price table: revision is not 40 lowercase hex characters")
	}
	asOf, err := parseRevisionTime(snapshotRevisionTime)
	if err != nil {
		return nil, fmt.Errorf("embedded price table: revision time: %w", err)
	}
	return table.withRevision(revision, asOf), nil
}

func parseRevisionTime(data []byte) (time.Time, error) {
	return time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
}
