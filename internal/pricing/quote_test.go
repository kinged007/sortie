package pricing

import (
	"reflect"
	"slices"
	"testing"

	"github.com/sortie-ai/sortie/internal/domain"
)

const (
	quoteTable = `{
  "full": {"litellm_provider": "acme", "input_cost_per_token": 1e-6, "output_cost_per_token": 5e-6, "cache_read_input_token_cost": 5e-7, "cache_creation_input_token_cost": 2e-6},
  "no-cache-read": {"litellm_provider": "acme", "input_cost_per_token": 1e-6, "output_cost_per_token": 5e-6, "cache_creation_input_token_cost": 2e-6},
  "free-cache-read": {"litellm_provider": "acme", "input_cost_per_token": 1e-6, "output_cost_per_token": 5e-6, "cache_read_input_token_cost": 0, "cache_creation_input_token_cost": 2e-6},
  "three": {"litellm_provider": "acme", "input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05, "cache_read_input_token_cost": 3e-07, "cache_creation_input_token_cost": 3.75e-06}
}`
	fetchedQuoteTable = `{"fetched-model": {"litellm_provider": "acme", "input_cost_per_token": 2e-6, "output_cost_per_token": 8e-6, "cache_read_input_token_cost": 2e-7, "cache_creation_input_token_cost": 1e-6}}`
	embeddedModel     = "claude-sonnet-4-5-20250929"
)

func rate(perMtok float64, layer RateLayer) ClassRate {
	return ClassRate{PerMtok: &perMtok, Layer: layer}
}

func inputRateOfThree() ClassRate {
	perMtok := 3.0
	return ClassRate{PerMtok: &perMtok, Layer: LayerKindEntry, FromInput: true}
}

func operatorExt() map[string]any { return map[string]any{"price_table": operatorTable()} }

func allClasses() []TokenClass {
	return []TokenClass{ClassInput, ClassOutput, ClassCacheRead, ClassCacheWrite}
}

func recordedPart(r *Rates, kind, model string) PartPrice {
	spent := domain.TokenUsage{InputTokens: 3, OutputTokens: 1, TotalTokens: 4, CacheReadTokens: 1, CacheWriteTokens: 1}
	return PriceRun(Spend{Kind: kind, Measured: true, Totals: spent, Parts: map[string]domain.TokenUsage{model: spent}}, r).Parts[model]
}

func TestQuote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ext      map[string]any
		nilRates bool
		fetched  string
		kinds    []string
		kind     string
		model    string
		want     ModelQuote
	}{
		{
			name:  "complete kind entry prices unset cache rates at its input rate",
			ext:   map[string]any{"price_table": noTables(), "token_rates": map[string]any{"k": perMtok(3, 15)}},
			kinds: []string{"k"}, kind: "k", model: "any-model",
			want: ModelQuote{
				Layer: LayerKindEntry, EntryKey: "k",
				Input: rate(3, LayerKindEntry), Output: rate(15, LayerKindEntry), CacheRead: inputRateOfThree(), CacheWrite: inputRateOfThree(),
				Status: CostEstimated,
			},
		},
		{
			name:  "kind entry without an output rate leaves it unset and unpriced",
			ext:   map[string]any{"price_table": noTables(), "token_rates": map[string]any{"k": map[string]any{"input_per_mtok": 3.0}}},
			kinds: []string{"k"}, kind: "k", model: "any-model",
			want: ModelQuote{
				Layer: LayerKindEntry, EntryKey: "k",
				Input: rate(3, LayerKindEntry), CacheRead: inputRateOfThree(), CacheWrite: inputRateOfThree(),
				Status: CostUnpriced, Unrated: []TokenClass{ClassOutput},
			},
		},
		{
			name:  "model entry takes the rates it leaves unset from the embedded table",
			ext:   map[string]any{"token_rates": map[string]any{embeddedModel: perMtok(1, 2)}},
			model: embeddedModel,
			want: ModelQuote{
				TableKey: embeddedModel, Layer: LayerModelEntry, EntryKey: embeddedModel,
				Input: rate(1, LayerModelEntry), Output: rate(2, LayerModelEntry), CacheRead: rate(0.3, LayerEmbedded), CacheWrite: rate(3.75, LayerEmbedded),
				Status: CostEstimated,
			},
		},
		{
			name:  "model entry for a name no table lists leaves the cache write unset",
			ext:   map[string]any{"price_table": noTables(), "token_rates": map[string]any{"unlisted": map[string]any{"input_per_mtok": 1.0, "output_per_mtok": 2.0, "cache_read_per_mtok": 0.1}}},
			model: "unlisted",
			want: ModelQuote{
				Layer: LayerModelEntry, EntryKey: "unlisted",
				Input: rate(1, LayerModelEntry), Output: rate(2, LayerModelEntry), CacheRead: rate(0.1, LayerModelEntry),
				Status: CostPartial, Unrated: []TokenClass{ClassCacheWrite},
			},
		},
		{
			name:    "name the fetched table lacks comes from the embedded table",
			ext:     map[string]any{},
			fetched: fetchedQuoteTable,
			model:   embeddedModel,
			want: ModelQuote{
				TableKey: embeddedModel, Layer: LayerEmbedded,
				Input: rate(3, LayerEmbedded), Output: rate(15, LayerEmbedded), CacheRead: rate(0.3, LayerEmbedded), CacheWrite: rate(3.75, LayerEmbedded),
				Status: CostEstimated,
			},
		},
		{
			name:    "name the fetched table lists comes from the fetched table",
			ext:     map[string]any{},
			fetched: fetchedQuoteTable,
			model:   "fetched-model",
			want: ModelQuote{
				TableKey: "fetched-model", Layer: LayerFetched,
				Input: rate(2, LayerFetched), Output: rate(8, LayerFetched), CacheRead: rate(0.2, LayerFetched), CacheWrite: rate(1, LayerFetched),
				Status: CostEstimated,
			},
		},
		{
			name:  "unmatched name has no layer and every class unrated",
			ext:   operatorExt(),
			model: "nope",
			want:  ModelQuote{Status: CostUnpriced, Unrated: allClasses()},
		},
		{
			name:  "empty name has no layer and every class unrated",
			ext:   operatorExt(),
			model: "",
			want:  ModelQuote{Status: CostUnpriced, Unrated: allClasses()},
		},
		{
			name:     "nil rates have no layer and every class unrated",
			nilRates: true,
			model:    "full",
			want:     ModelQuote{Status: CostUnpriced, Unrated: allClasses()},
		},
		{
			name:  "table entry without a cache read rate leaves it unset",
			ext:   operatorExt(),
			model: "no-cache-read",
			want: ModelQuote{
				TableKey: "no-cache-read", Layer: LayerOperatorTable,
				Input: rate(1, LayerOperatorTable), Output: rate(5, LayerOperatorTable), CacheWrite: rate(2, LayerOperatorTable),
				Status: CostPartial, Unrated: []TokenClass{ClassCacheRead},
			},
		},
		{
			name:  "rate of zero is a set rate",
			ext:   operatorExt(),
			model: "free-cache-read",
			want: ModelQuote{
				TableKey: "free-cache-read", Layer: LayerOperatorTable,
				Input: rate(1, LayerOperatorTable), Output: rate(5, LayerOperatorTable), CacheRead: rate(0, LayerOperatorTable), CacheWrite: rate(2, LayerOperatorTable),
				Status: CostEstimated,
			},
		},
		{
			name:  "per-token rates convert without float noise",
			ext:   operatorExt(),
			model: "three",
			want: ModelQuote{
				TableKey: "three", Layer: LayerOperatorTable,
				Input: rate(3, LayerOperatorTable), Output: rate(15, LayerOperatorTable), CacheRead: rate(0.3, LayerOperatorTable), CacheWrite: rate(3.75, LayerOperatorTable),
				Status: CostEstimated,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var rates *Rates
			if !tt.nilRates {
				in := LoadInput{WorkflowDir: writeFiles(t, map[string]string{"prices.json": quoteTable}), IsKind: kindsOf(tt.kinds...)}
				if tt.fetched != "" {
					fetched, err := ParseTable([]byte(tt.fetched), nil)
					if err != nil {
						t.Fatalf("ParseTable(%q) = %v, want nil", tt.fetched, err)
					}
					in.Downloaded = func(dl Download) *Table {
						if dl.Role == DownloadShared {
							return fetched
						}
						return nil
					}
				}
				var err error
				if rates, _, err = loadRates(t, tt.ext, in); err != nil {
					t.Fatalf("LoadRates(%v) = %v, want nil", tt.ext, err)
				}
			}

			got := rates.Quote(tt.kind, tt.model)

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Quote(%q, %q) = %s, want %s", tt.kind, tt.model, asJSON(t, got), asJSON(t, tt.want))
			}
			recorded := recordedPart(rates, tt.kind, tt.model)
			if got.Status != recorded.Status || !slices.Equal(got.Unrated, recorded.Unrated) {
				t.Errorf("Quote(%q, %q) status, unrated = %q, %v, want %q, %v as PriceRun records", tt.kind, tt.model, got.Status, got.Unrated, recorded.Status, recorded.Unrated)
			}
		})
	}
}
