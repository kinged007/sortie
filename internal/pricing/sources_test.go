package pricing

import (
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/config"
)

type downloadHistory string

const (
	noHistory      downloadHistory = ""
	failedTries    downloadHistory = "failed tries"
	failedThenGood downloadHistory = "failed then accepted"
	keptCopy       downloadHistory = "kept copy after a restart"
)

type sourceFacts struct {
	Status                          TableStatus
	InForce                         bool
	OffBy, File                     string
	Failures                        int
	Held, Dated                     bool
	HasErr, HasFailedAt, HasNextTry bool
}

func factsOf(s TableSource) sourceFacts {
	return sourceFacts{
		Status: s.Status, InForce: s.InForce, OffBy: s.OffBy, File: s.File, Failures: s.Failures,
		Held: s.Version != "" && s.Models > 0, Dated: !s.AsOf.IsZero(),
		HasErr: s.Err != "", HasFailedAt: !s.FailedAt.IsZero(), HasNextTry: !s.NextTryAt.IsZero(),
	}
}

func downloaderAfter(t *testing.T, history downloadHistory, price map[string]any) *Downloader {
	t.Helper()

	path := dbPath(t)
	if history == noHistory {
		return NewDownloader(path, slog.New(slog.DiscardHandler))
	}
	good := tableBody(t)
	f := newFixture(t, path, func(n int, w http.ResponseWriter) {
		if history == failedTries || (history == failedThenGood && n == 1) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(good)
	})
	price["fetch_url"] = f.dl.URL

	f.d.Want([]Download{f.dl})
	armed := <-f.armed
	if history == failedThenGood {
		armed.fire()
		<-f.armed
	}
	if history == keptCopy {
		return NewDownloader(path, slog.New(slog.DiscardHandler))
	}
	return f.d
}

func TestSources(t *testing.T) {
	const (
		userinfoFetch   = "https://user:secret@prices.test/shared.json?token=abc"
		userinfoReplace = "https://user:secret@prices.test/operator.json?token=abc"
	)
	replaceFileFailure := fmt.Errorf("loading workflow: %w", &config.ConfigError{Field: "price_table.replace_file", Check: "price_table.replace_file", Message: "bad table"})
	otherFailure := &config.ConfigError{Field: "agent.kind", Check: "agent.kind", Message: "unknown kind"}
	loadedCopy := sourceFacts{Status: TableLoaded, InForce: true, Held: true, Dated: true}

	tests := []struct {
		name         string
		price        map[string]any
		env          string
		history      downloadHistory
		loadErr      error
		nilRates     bool
		wantInForce  RateLayer
		wantOperator *sourceFacts
		wantFetched  sourceFacts
		wantEmbedded sourceFacts
		wantAddress  [2]string
	}{
		{
			name:        "off by setting hides the kept copy",
			price:       map[string]any{"fetch": false},
			history:     keptCopy,
			wantInForce: LayerEmbedded,
			wantFetched: sourceFacts{Status: TableOff, OffBy: "price_table.fetch"},
		},
		{
			name:        "off by environment hides the kept copy",
			price:       map[string]any{},
			env:         "false",
			history:     keptCopy,
			wantInForce: LayerEmbedded,
			wantFetched: sourceFacts{Status: TableOff, OffBy: "SORTIE_PRICE_TABLE_FETCH"},
		},
		{
			name:        "failed tries with no copy leave the embedded table in force",
			price:       map[string]any{},
			history:     failedTries,
			wantInForce: LayerEmbedded,
			wantFetched: sourceFacts{Status: TableFailing, Failures: 1, HasErr: true, HasFailedAt: true, HasNextTry: true},
		},
		{
			name:        "kept copy without a failure is loaded and in force",
			price:       map[string]any{},
			history:     keptCopy,
			wantInForce: LayerFetched,
			wantFetched: loadedCopy,
		},
		{
			name:        "nothing downloaded yet is not loaded",
			price:       map[string]any{},
			wantInForce: LayerEmbedded,
			wantFetched: sourceFacts{Status: TableNotLoaded},
		},
		{
			name:        "accepted try after failures clears them",
			price:       map[string]any{},
			history:     failedThenGood,
			wantInForce: LayerFetched,
			wantFetched: sourceFacts{Status: TableLoaded, InForce: true, Held: true, Dated: true, HasNextTry: true},
		},
		{
			name:         "served addresses keep scheme, host, and path only",
			price:        map[string]any{"fetch_url": userinfoFetch, "replace_url": userinfoReplace},
			wantInForce:  LayerEmbedded,
			wantOperator: &sourceFacts{Status: TableNotLoaded},
			wantFetched:  sourceFacts{Status: TableNotLoaded},
			wantAddress:  [2]string{"https://prices.test/operator.json", "https://prices.test/shared.json"},
		},
		{
			name:         "load error of replace_file fails its block",
			price:        map[string]any{"replace_file": "prices.json"},
			loadErr:      replaceFileFailure,
			wantInForce:  LayerOperatorTable,
			wantOperator: &sourceFacts{Status: TableFailing, InForce: true, File: "prices.json", Held: true, Dated: true, HasErr: true, HasFailedAt: true},
			wantFetched:  sourceFacts{Status: TableNotLoaded},
		},
		{
			name:         "another load error leaves the replace_file block loaded",
			price:        map[string]any{"replace_file": "prices.json"},
			loadErr:      otherFailure,
			wantInForce:  LayerOperatorTable,
			wantOperator: &sourceFacts{Status: TableLoaded, InForce: true, File: "prices.json", Held: true, Dated: true},
			wantFetched:  sourceFacts{Status: TableNotLoaded},
		},
		{
			name:         "embedded table off keeps the binary's facts",
			price:        map[string]any{"embedded": false},
			wantFetched:  sourceFacts{Status: TableNotLoaded},
			wantEmbedded: sourceFacts{Status: TableOff, Held: true, Dated: true},
		},
		{
			name:         "nil rates report fetched and embedded off",
			nilRates:     true,
			wantFetched:  sourceFacts{Status: TableOff},
			wantEmbedded: sourceFacts{Status: TableOff},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("SORTIE_PRICE_TABLE_FETCH", tt.env)
			} else {
				t.Parallel()
			}

			price := maps.Clone(tt.price)
			downloads := downloaderAfter(t, tt.history, price)
			var rates *Rates
			if !tt.nilRates {
				in := LoadInput{WorkflowDir: writeFiles(t, map[string]string{"prices.json": listedTable}), IsKind: kindsOf(), Downloaded: downloads.Table}
				var err error
				if rates, _, err = loadRates(t, map[string]any{"price_table": price}, in); err != nil {
					t.Fatalf("LoadRates(%v) = %v, want nil", price, err)
				}
			}
			loadEndedAt := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

			got := rates.Sources(SourcesInput{Downloads: downloads, LoadErr: tt.loadErr, LoadEndedAt: loadEndedAt, Now: loadEndedAt})

			if got.InForce != tt.wantInForce {
				t.Errorf("Sources(%v).InForce = %q, want %q", price, got.InForce, tt.wantInForce)
			}
			if fetched := factsOf(got.Fetched); fetched != tt.wantFetched {
				t.Errorf("Sources(%v).Fetched = %+v, want %+v", price, fetched, tt.wantFetched)
			}
			if tt.wantEmbedded.Status != "" {
				if embedded := factsOf(got.Embedded); embedded != tt.wantEmbedded {
					t.Errorf("Sources(%v).Embedded = %+v, want %+v", price, embedded, tt.wantEmbedded)
				}
			}
			if (got.Operator == nil) != (tt.wantOperator == nil) {
				t.Fatalf("Sources(%v).Operator = %+v, want %+v", price, got.Operator, tt.wantOperator)
			}
			if got.Operator != nil {
				if operator := factsOf(*got.Operator); operator != *tt.wantOperator {
					t.Errorf("Sources(%v).Operator = %+v, want %+v", price, operator, *tt.wantOperator)
				}
				if tt.loadErr != nil && got.Operator.Status == TableFailing && (got.Operator.Err != tt.loadErr.Error() || !got.Operator.FailedAt.Equal(loadEndedAt)) {
					t.Errorf("Sources(%v).Operator error, failed at = %q, %v, want %q, %v", price, got.Operator.Err, got.Operator.FailedAt, tt.loadErr.Error(), loadEndedAt)
				}
			}
			if tt.wantAddress != [2]string{} && (got.Operator.Address != tt.wantAddress[0] || got.Fetched.Address != tt.wantAddress[1]) {
				t.Errorf("Sources(%v) addresses = %q, %q, want %q, %q", price, got.Operator.Address, got.Fetched.Address, tt.wantAddress[0], tt.wantAddress[1])
			}
		})
	}
}

func TestSourcesAgeDays(t *testing.T) {
	t.Parallel()

	embedded, err := EmbeddedTable()
	if err != nil {
		t.Fatalf("EmbeddedTable() = %v, want nil", err)
	}
	rates := mustLoadRates(t, map[string]any{})
	tests := []struct {
		name  string
		after time.Duration
		want  int
	}{
		{"whole days are floored", 36 * time.Hour, 1},
		{"just under a day is zero", 24*time.Hour - time.Nanosecond, 0},
		{"three days", 72 * time.Hour, 3},
		{"an as-of date in the future never goes below zero", -48 * time.Hour, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := rates.Sources(SourcesInput{Now: embedded.AsOf().Add(tt.after)}).AgeDays

			if got != tt.want {
				t.Errorf("Sources(now = as-of %+v).AgeDays = %d, want %d", tt.after, got, tt.want)
			}
		})
	}
}
