package pricing

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

var wantRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func readSnapshotFile(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile("snapshot/" + name)
	if err != nil {
		t.Fatalf("reading snapshot file %s: %v", name, err)
	}
	return strings.TrimSpace(string(data))
}

func TestEmbeddedTableVersionAndDate(t *testing.T) {
	t.Parallel()

	table, err := EmbeddedTable()
	if err != nil {
		t.Fatalf("EmbeddedTable() error = %v, want nil", err)
	}
	wantTime, err := time.Parse(time.RFC3339, readSnapshotFile(t, "REVISION_TIME"))
	if err != nil {
		t.Fatalf("REVISION_TIME: %v", err)
	}

	if revision := readSnapshotFile(t, "REVISION"); table.Version() != revision || !wantRevisionPattern.MatchString(revision) {
		t.Errorf("EmbeddedTable().Version() = %q, want REVISION %q of 40 [0-9a-f]", table.Version(), revision)
	}
	if table.AsOf().IsZero() || !table.AsOf().Equal(wantTime) {
		t.Errorf("EmbeddedTable().AsOf() = %v, want %v", table.AsOf(), wantTime)
	}

	reparsed := mustParse(t, string(table.Encode()), nil)
	if !strings.HasPrefix(reparsed.Version(), "sha256:") || !reparsed.AsOf().IsZero() {
		t.Errorf("ParseTable(Encode()) = version %q, AsOf %v, want sha256 version and zero AsOf", reparsed.Version(), reparsed.AsOf())
	}
}

func TestEmbeddedTableLookup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model   string
		wantKey string
	}{
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5-20250929"},
		{"anthropic/claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"claude-opus-4.6", "claude-opus-4-6"},
		{"gpt-5-codex", "gpt-5-codex"},
		{"gemini-2.5-pro", "gemini-2.5-pro"},
		{"google/gemini-2.5-pro", "gemini/gemini-2.5-pro"},
		{"google-vertex/gemini-2.5-pro", "gemini-2.5-pro"},
		{"amazon-bedrock/anthropic.claude-haiku-4-5-20251001-v1:0", "anthropic.claude-haiku-4-5-20251001-v1:0"},
		{"opencode/claude-sonnet-4-5", ""},
		{"<synthetic>", ""},
		{"claude-sonnet-4", ""},
		{"no-such-model", ""},
	}

	table, err := EmbeddedTable()
	if err != nil {
		t.Fatalf("EmbeddedTable() error = %v, want nil", err)
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			t.Parallel()

			got, ok := table.Lookup(tt.model)

			if ok != (tt.wantKey != "") || got.Key != tt.wantKey {
				t.Errorf("EmbeddedTable().Lookup(%q) = key %q, %t, want key %q, %t", tt.model, got.Key, ok, tt.wantKey, tt.wantKey != "")
			}
		})
	}
}

func TestParseRevisionTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{"empty", "", true},
		{"date without time", "2026-10-18", true},
		{"surrounding whitespace trimmed", " 2026-10-18T08:58:40Z\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseRevisionTime([]byte(tt.data))

			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRevisionTime(%q) = %v, want error", tt.data, got)
				}
				return
			}
			if want := time.Date(2026, 10, 18, 8, 58, 40, 0, time.UTC); err != nil || !got.Equal(want) {
				t.Errorf("parseRevisionTime(%q) = %v, %v, want %v", tt.data, got, err, want)
			}
		})
	}
}
