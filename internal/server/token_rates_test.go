package server

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/domain"
)

func TestParseTokenRates(t *testing.T) {
	t.Parallel()

	fptr := func(v float64) *float64 { return &v }

	tests := []struct {
		name         string
		extensions   map[string]any
		wantNil      bool
		wantWarnings int
		wantRates    TokenRates // only checked when wantNil is false
	}{
		{
			name:       "absent token_rates key returns nil",
			extensions: map[string]any{"other": "value"},
			wantNil:    true,
		},
		{
			name:       "nil extensions map returns nil",
			extensions: nil,
			wantNil:    true,
		},
		{
			name:       "empty token_rates map returns nil",
			extensions: map[string]any{"token_rates": map[string]any{}},
			wantNil:    true,
		},
		{
			name: "kind with empty map stores an incomplete entry with a needs-both warning",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"claude": map[string]any{},
				},
			},
			wantNil:      false,
			wantWarnings: 1,
			wantRates:    TokenRates{"claude": TokenRateConfig{}},
		},
		{
			name: "single kind all three rates present",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"claude": map[string]any{
						"input_per_mtok":      3.0,
						"output_per_mtok":     15.0,
						"cache_read_per_mtok": 0.3,
					},
				},
			},
			wantNil: false,
			wantRates: TokenRates{
				"claude": TokenRateConfig{
					InputPerMtok:     fptr(3.0),
					OutputPerMtok:    fptr(15.0),
					CacheReadPerMtok: fptr(0.3),
				},
			},
		},
		{
			name: "single kind with cache_write_per_mtok also parses",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"claude": map[string]any{
						"input_per_mtok":       3.0,
						"output_per_mtok":      15.0,
						"cache_read_per_mtok":  0.3,
						"cache_write_per_mtok": 6.25,
					},
				},
			},
			wantNil: false,
			wantRates: TokenRates{
				"claude": TokenRateConfig{
					InputPerMtok:      fptr(3.0),
					OutputPerMtok:     fptr(15.0),
					CacheReadPerMtok:  fptr(0.3),
					CacheWritePerMtok: fptr(6.25),
				},
			},
		},
		{
			name: "multiple kinds all parsed",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"claude": map[string]any{
						"input_per_mtok":  3.0,
						"output_per_mtok": 15.0,
					},
					"gpt4": map[string]any{
						"input_per_mtok":  10.0,
						"output_per_mtok": 30.0,
					},
				},
			},
			wantNil: false,
			wantRates: TokenRates{
				"claude": TokenRateConfig{InputPerMtok: fptr(3.0), OutputPerMtok: fptr(15.0)},
				"gpt4":   TokenRateConfig{InputPerMtok: fptr(10.0), OutputPerMtok: fptr(30.0)},
			},
		},
		{
			name: "non-map token_rates value yields warning",
			extensions: map[string]any{
				"token_rates": "not-a-map",
			},
			wantNil:      true,
			wantWarnings: 1,
		},
		{
			name: "non-map kind sub-value yields warning, incomplete kind still stores an entry",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"claude": map[string]any{"input_per_mtok": 3.0},
					"bad":    "not-a-map",
				},
			},
			wantNil:      false,
			wantWarnings: 2,
			wantRates: TokenRates{
				"claude": TokenRateConfig{InputPerMtok: fptr(3.0)},
				"bad":    TokenRateConfig{},
			},
		},
		{
			name: "negative rate yields nil pointer for that field and warning",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"claude": map[string]any{
						"input_per_mtok":  -1.0,
						"output_per_mtok": 15.0,
					},
				},
			},
			wantNil:      false,
			wantWarnings: 2,
			wantRates: TokenRates{
				"claude": TokenRateConfig{InputPerMtok: nil, OutputPerMtok: fptr(15.0)},
			},
		},
		{
			name: "empty kind alongside populated kind each report their own needs-both warning",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"empty":  map[string]any{},
					"claude": map[string]any{"input_per_mtok": 3.0},
				},
			},
			wantNil:      false,
			wantWarnings: 2,
			wantRates: TokenRates{
				"claude": TokenRateConfig{InputPerMtok: fptr(3.0)},
				"empty":  TokenRateConfig{},
			},
		},
		{
			name: "kind with all negative rates stores an incomplete entry with three warnings",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"bad": map[string]any{
						"input_per_mtok":  -1.0,
						"output_per_mtok": -2.0,
					},
				},
			},
			wantNil:      false,
			wantWarnings: 3,
			wantRates:    TokenRates{"bad": TokenRateConfig{}},
		},
		{
			name: "explicit zero rate produces non-nil pointer to 0.0 but stays incomplete",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"claude": map[string]any{
						"input_per_mtok": 0.0,
					},
				},
			},
			wantNil:      false,
			wantWarnings: 1,
			wantRates: TokenRates{
				"claude": TokenRateConfig{InputPerMtok: fptr(0.0)},
			},
		},
		{
			name: "integer values coerced to float64",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"claude": map[string]any{
						"input_per_mtok":  int(3),
						"output_per_mtok": int64(15),
					},
				},
			},
			wantNil: false,
			wantRates: TokenRates{
				"claude": TokenRateConfig{
					InputPerMtok:  fptr(3.0),
					OutputPerMtok: fptr(15.0),
				},
			},
		},
		{
			name: "entry keyed to the empty string is dropped with one warning",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"": map[string]any{"input_per_mtok": 3.0},
				},
			},
			wantNil:      true,
			wantWarnings: 1,
		},
		{
			name: "entry keyed to the empty string is dropped, a populated kind beside it still warns for itself",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"":       map[string]any{"input_per_mtok": 3.0},
					"claude": map[string]any{"input_per_mtok": 5.0},
				},
			},
			wantNil:      false,
			wantWarnings: 2,
			wantRates: TokenRates{
				"claude": TokenRateConfig{InputPerMtok: fptr(5.0)},
			},
		},
		{
			name: "missing individual field yields nil pointer for that field",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"claude": map[string]any{
						"output_per_mtok": 15.0,
						// input_per_mtok absent
						// cache_read_per_mtok absent
					},
				},
			},
			wantNil:      false,
			wantWarnings: 1,
			wantRates: TokenRates{
				"claude": TokenRateConfig{
					InputPerMtok:     nil,
					OutputPerMtok:    fptr(15.0),
					CacheReadPerMtok: nil,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rawSection, present := tt.extensions["token_rates"]
			got, warnings := ParseTokenRates(rawSection, present)

			if len(warnings) != tt.wantWarnings {
				t.Errorf("ParseTokenRates warnings = %d, want %d: %v", len(warnings), tt.wantWarnings, warnings)
			}

			if tt.wantNil {
				if got != nil {
					t.Errorf("ParseTokenRates = %v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Fatal("ParseTokenRates = nil, want non-nil")
			}

			for kind, wantCfg := range tt.wantRates {
				gotCfg, ok := got[kind]
				if !ok {
					t.Errorf("missing kind %q in result", kind)
					continue
				}
				assertRateField(t, kind, "input_per_mtok", gotCfg.InputPerMtok, wantCfg.InputPerMtok)
				assertRateField(t, kind, "output_per_mtok", gotCfg.OutputPerMtok, wantCfg.OutputPerMtok)
				assertRateField(t, kind, "cache_read_per_mtok", gotCfg.CacheReadPerMtok, wantCfg.CacheReadPerMtok)
				assertRateField(t, kind, "cache_write_per_mtok", gotCfg.CacheWritePerMtok, wantCfg.CacheWritePerMtok)
			}
		})
	}
}

func assertRateField(t *testing.T, kind, field string, got, want *float64) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("token_rates.%s.%s = %v, want nil", kind, field, *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("token_rates.%s.%s = nil, want %v", kind, field, *want)
	}
	if *got != *want {
		t.Errorf("token_rates.%s.%s = %v, want %v", kind, field, *got, *want)
	}
}

// TestParseTokenRates_EmptyKeyWarningMessage pins the exact warning
// text an empty-keyed entry produces, so tokenRates[""] never
// resolves for any of the three callers that share this type.
func TestParseTokenRates_EmptyKeyWarningMessage(t *testing.T) {
	t.Parallel()

	rates, warnings := ParseTokenRates(map[string]any{
		"": map[string]any{"input_per_mtok": 3.0},
	}, true)

	if rates != nil {
		t.Errorf("ParseTokenRates rates = %v, want nil (empty-keyed entry dropped)", rates)
	}
	want := "token_rates: entry keyed to the empty string is dropped"
	if len(warnings) != 1 || warnings[0] != want {
		t.Errorf("ParseTokenRates warnings = %v, want [%q]", warnings, want)
	}
}

// TestParseTokenRates_IncompleteEntryPricesNothing drives an entry
// missing input_per_mtok through both ParseTokenRates and EstimateCost,
// proving the stored entry is kept (not dropped) yet prices nothing.
func TestParseTokenRates_IncompleteEntryPricesNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         map[string]any
		wantWarning string
	}{
		{
			name:        "only output_per_mtok present",
			raw:         map[string]any{"claude-code": map[string]any{"output_per_mtok": 15.0}},
			wantWarning: "token_rates.claude-code: entry needs both input_per_mtok and output_per_mtok and prices nothing",
		},
		{
			name:        "entry present but empty",
			raw:         map[string]any{"claude-code": map[string]any{}},
			wantWarning: "token_rates.claude-code: entry needs both input_per_mtok and output_per_mtok and prices nothing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rates, warnings := ParseTokenRates(tt.raw, true)
			if len(warnings) != 1 || warnings[0] != tt.wantWarning {
				t.Errorf("ParseTokenRates warnings = %v, want [%q]", warnings, tt.wantWarning)
			}

			cfg, ok := rates["claude-code"]
			if !ok {
				t.Fatal(`rates["claude-code"] missing, want a stored incomplete entry`)
			}
			if got := EstimateCost(domain.TokenUsage{InputTokens: 1_000_000}, &cfg); got != nil {
				t.Errorf("EstimateCost(incomplete entry) = %v, want nil", *got)
			}
		})
	}
}

// TestParseTokenRates_NonMapKindValueStoresZeroEntry verifies a kind
// whose raw value is not a map warns once, using the value's dynamic
// type in the message, and still keeps an all-unset entry rather than
// also emitting the needs-both warning a second time.
func TestParseTokenRates_NonMapKindValueStoresZeroEntry(t *testing.T) {
	t.Parallel()

	rates, warnings := ParseTokenRates(map[string]any{"claude-code": 3}, true)

	want := "token_rates.claude-code: expected map, got int"
	if len(warnings) != 1 || warnings[0] != want {
		t.Errorf("ParseTokenRates warnings = %v, want [%q]", warnings, want)
	}
	cfg, ok := rates["claude-code"]
	if !ok {
		t.Fatal(`rates["claude-code"] missing, want a stored zero-value entry`)
	}
	if cfg != (TokenRateConfig{}) {
		t.Errorf("rates[claude-code] = %+v, want the zero value", cfg)
	}
}

// TestParseTokenRates_NegativeRatesWarnThenNeedsBoth pins the warning
// order within one incomplete entry: each invalid rate warns in field
// order, and the needs-both warning always comes last.
func TestParseTokenRates_NegativeRatesWarnThenNeedsBoth(t *testing.T) {
	t.Parallel()

	_, warnings := ParseTokenRates(map[string]any{
		"bad": map[string]any{"input_per_mtok": -1.0, "output_per_mtok": -2.0},
	}, true)

	want := []string{
		"token_rates.bad.input_per_mtok: negative rate -1",
		"token_rates.bad.output_per_mtok: negative rate -2",
		"token_rates.bad: entry needs both input_per_mtok and output_per_mtok and prices nothing",
	}
	if !slices.Equal(warnings, want) {
		t.Errorf("ParseTokenRates warnings = %v, want %v", warnings, want)
	}
}

// TestParseTokenRates_NonFiniteRateWarns covers the non-finite check
// ahead of the existing negative-value check, for both a NaN and an
// infinite rate, on the newly-added cache_write_per_mtok key.
func TestParseTokenRates_NonFiniteRateWarns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rate float64
	}{
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
		{"NaN", math.NaN()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rates, warnings := ParseTokenRates(map[string]any{
				"claude-code": map[string]any{
					"input_per_mtok":       3.0,
					"output_per_mtok":      15.0,
					"cache_write_per_mtok": tt.rate,
				},
			}, true)

			found := slices.ContainsFunc(warnings, func(w string) bool {
				return strings.HasPrefix(w, "token_rates.claude-code.cache_write_per_mtok: rate")
			})
			if !found {
				t.Errorf("ParseTokenRates warnings = %v, want a non-finite cache_write_per_mtok warning", warnings)
			}
			cfg, ok := rates["claude-code"]
			if !ok {
				t.Fatal(`rates["claude-code"] missing`)
			}
			if cfg.CacheWritePerMtok != nil {
				t.Errorf("CacheWritePerMtok = %v, want nil (non-finite rate rejected)", *cfg.CacheWritePerMtok)
			}
		})
	}
}

// TestParseTokenRates_UnrecognizedKeyWarnsButEntryStillPrices verifies
// a misspelled rate key warns without preventing the rest of an
// otherwise-complete entry from pricing.
func TestParseTokenRates_UnrecognizedKeyWarnsButEntryStillPrices(t *testing.T) {
	t.Parallel()

	rates, warnings := ParseTokenRates(map[string]any{
		"claude-code": map[string]any{
			"input_per_mtok": 3.0, "output_per_mtok": 15.0, "cache_reed_per_mtok": 1.0,
		},
	}, true)

	want := "token_rates.claude-code.cache_reed_per_mtok: unrecognized key is ignored"
	if len(warnings) != 1 || warnings[0] != want {
		t.Errorf("ParseTokenRates warnings = %v, want [%q]", warnings, want)
	}
	cfg, ok := rates["claude-code"]
	if !ok {
		t.Fatal(`rates["claude-code"] missing`)
	}
	if got := EstimateCost(domain.TokenUsage{InputTokens: 1_000_000, OutputTokens: 500_000}, &cfg); got == nil {
		t.Error("EstimateCost(entry beside an unrecognized key) = nil, want a priced result")
	}
}

// TestParseTokenRates_WarningsOrderedByAscendingKind verifies warning
// order follows sorted kind order rather than Go's randomized map
// iteration, so repeated runs over the same input never reorder it.
func TestParseTokenRates_WarningsOrderedByAscendingKind(t *testing.T) {
	t.Parallel()

	raw := map[string]any{
		"zeta":  map[string]any{"output_per_mtok": 1.0},
		"alpha": map[string]any{"output_per_mtok": 1.0},
	}
	want := []string{
		"token_rates.alpha: entry needs both input_per_mtok and output_per_mtok and prices nothing",
		"token_rates.zeta: entry needs both input_per_mtok and output_per_mtok and prices nothing",
	}

	for range 5 {
		_, warnings := ParseTokenRates(raw, true)
		if !slices.Equal(warnings, want) {
			t.Errorf("ParseTokenRates warnings = %v, want %v (ascending kind order)", warnings, want)
		}
	}
}

func TestEstimateCost(t *testing.T) {
	t.Parallel()

	fptr := func(v float64) *float64 { return &v }

	tests := []struct {
		name       string
		usage      domain.TokenUsage
		rates      *TokenRateConfig
		wantNil    bool
		wantResult float64
	}{
		{
			name:    "nil rates returns nil",
			usage:   domain.TokenUsage{InputTokens: 100, OutputTokens: 200, CacheReadTokens: 50},
			rates:   nil,
			wantNil: true,
		},
		{
			name:    "all rate fields nil on non-nil config returns nil",
			usage:   domain.TokenUsage{InputTokens: 1000, OutputTokens: 500},
			rates:   &TokenRateConfig{},
			wantNil: true,
		},
		{
			name:    "missing input rate returns nil regardless of cache rates",
			usage:   domain.TokenUsage{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 100, CacheWriteTokens: 50},
			rates:   &TokenRateConfig{OutputPerMtok: fptr(15.0), CacheReadPerMtok: fptr(1.0), CacheWritePerMtok: fptr(2.0)},
			wantNil: true,
		},
		{
			name:    "missing output rate returns nil regardless of cache rates",
			usage:   domain.TokenUsage{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 100, CacheWriteTokens: 50},
			rates:   &TokenRateConfig{InputPerMtok: fptr(5.0), CacheReadPerMtok: fptr(1.0), CacheWritePerMtok: fptr(2.0)},
			wantNil: true,
		},
		{
			// The issue's own recorded session: every cache-read token
			// priced once, at the cache-read rate, not again at the
			// input rate.
			name: "cache reads price once at the cache-read rate",
			usage: domain.TokenUsage{
				InputTokens: 6_417_958, CacheReadTokens: 5_888_455, OutputTokens: 36_065,
			},
			rates:      &TokenRateConfig{InputPerMtok: fptr(5), OutputPerMtok: fptr(25), CacheReadPerMtok: fptr(0.5)},
			wantResult: 6.4933675,
		},
		{
			name: "with cache_read_per_mtok unset, every input token prices once at the input rate",
			usage: domain.TokenUsage{
				InputTokens: 6_417_958, CacheReadTokens: 5_888_455, OutputTokens: 36_065,
			},
			rates:      &TokenRateConfig{InputPerMtok: fptr(5), OutputPerMtok: fptr(25)},
			wantResult: 32.991415,
		},
		{
			name: "recorded copilot-cli fixture with cache_write_per_mtok set",
			usage: domain.TokenUsage{
				InputTokens: 193_011, CacheReadTokens: 154_053, CacheWriteTokens: 38_948, OutputTokens: 596,
			},
			rates: &TokenRateConfig{
				InputPerMtok: fptr(5), OutputPerMtok: fptr(25), CacheReadPerMtok: fptr(0.5), CacheWritePerMtok: fptr(6.25),
			},
			wantResult: 0.3354015,
		},
		{
			name: "same copilot-cli fixture with cache_write_per_mtok unset prices writes at the input rate",
			usage: domain.TokenUsage{
				InputTokens: 193_011, CacheReadTokens: 154_053, CacheWriteTokens: 38_948, OutputTokens: 596,
			},
			rates:      &TokenRateConfig{InputPerMtok: fptr(5), OutputPerMtok: fptr(25), CacheReadPerMtok: fptr(0.5)},
			wantResult: 0.2867165,
		},
		{
			name:       "zero cache counts equals fresh input plus output, the pre-cache-pricing formula",
			usage:      domain.TokenUsage{InputTokens: 1_000_000, OutputTokens: 500_000},
			rates:      &TokenRateConfig{InputPerMtok: fptr(3.0), OutputPerMtok: fptr(15.0), CacheReadPerMtok: fptr(0.3)},
			wantResult: 10.5,
		},
		{
			// A row from before cache reads were split out of input:
			// CacheReadTokens exceeds InputTokens, so fresh input clamps
			// at zero rather than going negative.
			name:       "cache reads exceeding input clamps fresh input at zero, never negative",
			usage:      domain.TokenUsage{InputTokens: 10, CacheReadTokens: 154_053, OutputTokens: 596},
			rates:      &TokenRateConfig{InputPerMtok: fptr(5), OutputPerMtok: fptr(25), CacheReadPerMtok: fptr(0.5)},
			wantResult: 0.0919265,
		},
		{
			name:       "zero token counts with rates returns pointer to 0.0",
			usage:      domain.TokenUsage{},
			rates:      &TokenRateConfig{InputPerMtok: fptr(3.0), OutputPerMtok: fptr(15.0)},
			wantResult: 0.0,
		},
		{
			name:       "zero rates with tokens returns pointer to 0.0",
			usage:      domain.TokenUsage{InputTokens: 1_000_000, OutputTokens: 500_000},
			rates:      &TokenRateConfig{InputPerMtok: fptr(0.0), OutputPerMtok: fptr(0.0)},
			wantResult: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := EstimateCost(tt.usage, tt.rates)

			if tt.wantNil {
				if got != nil {
					t.Errorf("EstimateCost(%+v) = %v, want nil", tt.usage, *got)
				}
				return
			}

			if got == nil {
				t.Fatalf("EstimateCost(%+v) = nil, want non-nil", tt.usage)
			}
			if math.IsInf(*got, 0) || math.IsNaN(*got) {
				t.Fatalf("EstimateCost(%+v) = %v, want finite number", tt.usage, *got)
			}
			if diff := *got - tt.wantResult; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("EstimateCost(%+v) = %.10f, want %.10f", tt.usage, *got, tt.wantResult)
			}
		})
	}
}

// TestTokenRateAdvisories verifies each ParseTokenRates warning becomes
// exactly one config.Advisory, in warning order, under the token_rates
// check.
func TestTokenRateAdvisories(t *testing.T) {
	t.Parallel()

	t.Run("no token_rates extension yields no advisories", func(t *testing.T) {
		t.Parallel()

		var cfg config.ServiceConfig
		got := TokenRateAdvisories(cfg)
		if len(got) != 0 {
			t.Errorf("TokenRateAdvisories = %v, want none", got)
		}
	})

	t.Run("one advisory per warning, in order, under the token_rates check", func(t *testing.T) {
		t.Parallel()

		var cfg config.ServiceConfig
		cfg.SetExtensionSection("token_rates", map[string]any{
			"zeta":  map[string]any{"output_per_mtok": 1.0},
			"alpha": map[string]any{"output_per_mtok": 1.0},
		})

		got := TokenRateAdvisories(cfg)
		wantTexts := []string{
			"token_rates.alpha: entry needs both input_per_mtok and output_per_mtok and prices nothing",
			"token_rates.zeta: entry needs both input_per_mtok and output_per_mtok and prices nothing",
		}
		if len(got) != len(wantTexts) {
			t.Fatalf("TokenRateAdvisories returned %d advisories, want %d: %+v", len(got), len(wantTexts), got)
		}
		for i, adv := range got {
			if adv.Check != "token_rates" {
				t.Errorf("advisory %d Check = %q, want %q", i, adv.Check, "token_rates")
			}
			if adv.Text != wantTexts[i] {
				t.Errorf("advisory %d Text = %q, want %q", i, adv.Text, wantTexts[i])
			}
			if adv.Message != "skipped invalid token rate entry" {
				t.Errorf("advisory %d Message = %q, want %q", i, adv.Message, "skipped invalid token rate entry")
			}
			if len(adv.Attrs) != 1 || adv.Attrs[0].Key != "detail" || adv.Attrs[0].Value.String() != wantTexts[i] {
				t.Errorf("advisory %d Attrs = %+v, want one detail attribute holding the warning", i, adv.Attrs)
			}
		}
	})

	t.Run("a complete entry produces no advisory", func(t *testing.T) {
		t.Parallel()

		var cfg config.ServiceConfig
		cfg.SetExtensionSection("token_rates", map[string]any{
			"claude-code": map[string]any{"input_per_mtok": 3.0, "output_per_mtok": 15.0},
		})

		if got := TokenRateAdvisories(cfg); len(got) != 0 {
			t.Errorf("TokenRateAdvisories = %v, want none for a complete entry", got)
		}
	})
}

func TestFormatCost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input float64
		want  string
	}{
		{"zero", 0.00, "$0.00"},
		{"small", 0.05, "$0.05"},
		{"under 10", 9.99, "$9.99"},
		{"under 1000", 999.99, "$999.99"},
		{"rounds up to 1000 from below", 999.999, "$1,000.00"},
		{"rounds up fractional near boundary", 1234.9999, "$1,235.00"},
		{"exact 1000", 1000.00, "$1,000.00"},
		{"mid four digits", 1234.56, "$1,234.56"},
		{"five digits with cents", 10000.50, "$10,000.50"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := FormatCost(tt.input)
			if got != tt.want {
				t.Errorf("FormatCost(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
