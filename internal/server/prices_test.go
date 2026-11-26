package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/pricing"
)

type priceRow struct {
	Kind, Model, LastUsedAt string
	Runs                    int
	Input                   string
}

func mustPriceRates(t *testing.T) *pricing.Rates {
	t.Helper()

	rates := func(input, output float64) map[string]any {
		return map[string]any{"input_per_mtok": input, "output_per_mtok": output}
	}
	cfg, err := config.NewServiceConfig(map[string]any{
		"price_table": map[string]any{"embedded": false, "fetch": false},
		"token_rates": map[string]any{"priced": rates(3, 15), "free": rates(0, 0)},
	})
	if err != nil {
		t.Fatalf("NewServiceConfig = %v, want nil", err)
	}
	loaded, _, err := pricing.LoadRates(cfg, pricing.LoadInput{IsKind: func(string) bool { return false }})
	if err != nil {
		t.Fatalf("LoadRates = %v, want nil", err)
	}
	return loaded
}

func TestPricesRoute(t *testing.T) {
	t.Parallel()

	older := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	newer := older.Add(48 * time.Hour)
	stamp := func(at time.Time) string { return at.Format(time.RFC3339Nano) }
	rates := mustPriceRates(t)

	tests := []struct {
		name       string
		uses       []ModelUse
		configured []orchestrator.ConfiguredModel
		want       []priceRow
	}{
		{
			name:       "configured pair merges with its recorded pair",
			uses:       []ModelUse{{AgentKind: "claude-code", Model: "priced", LastUsedAt: newer, Runs: 3}},
			configured: []orchestrator.ConfiguredModel{{AgentKind: "claude-code", Model: "priced"}},
			want:       []priceRow{{"claude-code", "priced", stamp(newer), 3, "3"}},
		},
		{
			name: "configured-only pairs follow every recorded pair and stay per agent kind",
			uses: []ModelUse{{AgentKind: "claude-code", Model: "priced", LastUsedAt: older, Runs: 1}},
			configured: []orchestrator.ConfiguredModel{
				{AgentKind: "codex", Model: "priced"},
				{AgentKind: "claude-code", Model: "free"},
			},
			want: []priceRow{
				{"claude-code", "priced", stamp(older), 1, "3"},
				{"claude-code", "free", "", 0, "0"},
				{"codex", "priced", "", 0, "3"},
			},
		},
		{
			name: "unset rate serves null and zero rate serves 0",
			uses: []ModelUse{
				{AgentKind: "claude-code", Model: "free", LastUsedAt: newer, Runs: 1},
				{AgentKind: "claude-code", Model: "unlisted", LastUsedAt: older, Runs: 1},
			},
			want: []priceRow{
				{"claude-code", "free", stamp(newer), 1, "0"},
				{"claude-code", "unlisted", stamp(older), 1, "null"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := New(Params{
				SnapshotFn:         fixedSnapshot(orchestrator.RuntimeSnapshotResult{}),
				RefreshFn:          acceptingRefresh(),
				Logger:             slog.New(slog.DiscardHandler),
				ModelUse:           func(context.Context) ([]ModelUse, error) { return tt.uses, nil },
				ConfiguredModelsFn: func() []orchestrator.ConfiguredModel { return tt.configured },
				RatesFn:            func() *pricing.Rates { return rates },
			})
			ts := httptest.NewServer(srv.Mux())
			t.Cleanup(ts.Close)

			status, body := getJSON[pricesResponse](t, ts, "/api/v1/prices")

			if status != http.StatusOK {
				t.Fatalf("GET /api/v1/prices = %d, want %d", status, http.StatusOK)
			}
			var got []priceRow
			for _, m := range body.Models {
				row := priceRow{Kind: m.AgentKind, Model: m.Model, Runs: m.Runs, Input: "null"}
				if m.LastUsedAt != nil {
					row.LastUsedAt = *m.LastUsedAt
				}
				if m.Input.PerMtok != nil {
					row.Input = fmt.Sprint(*m.Input.PerMtok)
				}
				got = append(got, row)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GET /api/v1/prices models = %+v, want %+v", got, tt.want)
			}
		})
	}
}
