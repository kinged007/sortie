package pricing

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

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
		wantTexts    []string
		wantRates    TokenRates
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
			name: "kind with empty map stores an incomplete entry without a warning",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"claude": map[string]any{},
				},
			},
			wantNil:   false,
			wantRates: TokenRates{"claude": TokenRateConfig{}},
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
			wantWarnings: 1,
			wantRates: TokenRates{
				"claude": TokenRateConfig{InputPerMtok: fptr(3.0)},
				"bad":    TokenRateConfig{},
			},
		},
		{
			name: "non-map kind value warns once with its dynamic type and stores a zero entry",
			extensions: map[string]any{
				"token_rates": map[string]any{"claude-code": 3},
			},
			wantNil:      false,
			wantWarnings: 1,
			wantTexts:    []string{"token_rates.claude-code: expected map, got int"},
			wantRates:    TokenRates{"claude-code": TokenRateConfig{}},
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
			wantWarnings: 1,
			wantRates: TokenRates{
				"claude": TokenRateConfig{InputPerMtok: nil, OutputPerMtok: fptr(15.0)},
			},
		},
		{
			name: "kind with all negative rates stores an incomplete entry with two warnings",
			extensions: map[string]any{
				"token_rates": map[string]any{
					"bad": map[string]any{
						"input_per_mtok":  -1.0,
						"output_per_mtok": -2.0,
					},
				},
			},
			wantNil:      false,
			wantWarnings: 2,
			wantTexts: []string{
				"token_rates.bad.input_per_mtok: negative rate -1",
				"token_rates.bad.output_per_mtok: negative rate -2",
			},
			wantRates: TokenRates{"bad": TokenRateConfig{}},
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
			wantNil: false,
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
			wantTexts:    []string{"token_rates: entry keyed to the empty string is dropped"},
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
			wantWarnings: 1,
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
					},
				},
			},
			wantNil: false,
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
			if tt.wantTexts != nil && !slices.Equal(warnings, tt.wantTexts) {
				t.Errorf("ParseTokenRates warnings = %q, want %q", warnings, tt.wantTexts)
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

func TestParseTokenRates_IncompleteEntryPricesNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  map[string]any
	}{
		{"only output_per_mtok present", map[string]any{"claude-code": map[string]any{"output_per_mtok": 15.0}}},
		{"entry present but empty", map[string]any{"claude-code": map[string]any{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rates, warnings := ParseTokenRates(tt.raw, true)
			if len(warnings) != 0 {
				t.Errorf("ParseTokenRates warnings = %v, want none", warnings)
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

func TestParseTokenRates_WarningsOrderedByAscendingKind(t *testing.T) {
	t.Parallel()

	raw := map[string]any{
		"zeta":  map[string]any{"input_per_mtok": -1.0},
		"alpha": map[string]any{"input_per_mtok": -1.0},
	}
	want := []string{
		"token_rates.alpha.input_per_mtok: negative rate -1",
		"token_rates.zeta.input_per_mtok: negative rate -1",
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

func TestReloadTokenRates(t *testing.T) {
	t.Parallel()

	k := func(input any) map[string]any {
		return map[string]any{"input_per_mtok": input, "output_per_mtok": 10.0, "cache_read_per_mtok": 0.5, "cache_write_per_mtok": 4.0}
	}
	invalid := map[string]any{"input_per_mtok": -1.0, "output_per_mtok": 1.0}
	r1k := fixtureRates(2)["k"]

	tests := []struct {
		name    string
		raw     any
		present bool
		want    TokenRates
	}{
		{"non-map section keeps the rates in force", "oops", true, fixtureRates(2)},
		{"removed section yields nil", nil, false, nil},
		{"valid change replaces the entry", map[string]any{"k": k(4.0)}, true, fixtureRates(4)},
		{"entry that is not a map keeps the entry", map[string]any{"k": "oops"}, true, fixtureRates(2)},
		{"negative rate keeps the entry", map[string]any{"k": k(-1.0)}, true, fixtureRates(2)},
		{"non-finite rate keeps the entry", map[string]any{"k": k(math.Inf(1))}, true, fixtureRates(2)},
		{"non-numeric rate keeps the entry", map[string]any{"k": k("4")}, true, fixtureRates(2)},
		{"unrecognized key is ignored", map[string]any{"k": map[string]any{"input_per_mtok": 2.0, "output_per_mtok": 10.0, "bogus": 1.0}}, true,
			TokenRates{"k": {InputPerMtok: r1k.InputPerMtok, OutputPerMtok: r1k.OutputPerMtok}}},
		{"incomplete entry applies as parsed", map[string]any{"k": map[string]any{"output_per_mtok": 10.0}}, true,
			TokenRates{"k": {OutputPerMtok: r1k.OutputPerMtok}}},
		{"key missing from the section is removed", map[string]any{"x": map[string]any{"input_per_mtok": 1, "output_per_mtok": int64(2)}}, true,
			TokenRates{"x": {InputPerMtok: new(1.0), OutputPerMtok: new(2.0)}}},
		{"added entry with an invalid rate is read as at startup", map[string]any{"k": k(2.0), "n": invalid}, true,
			TokenRates{"k": r1k, "n": {OutputPerMtok: new(1.0)}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inForce := fixtureRates(2)

			got := reloadTokenRates(inForce, tt.raw, tt.present)

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("reloadTokenRates(%v, %v) = %s, want %s", tt.raw, tt.present, asJSON(t, got), asJSON(t, tt.want))
			}
			if !reflect.DeepEqual(inForce, fixtureRates(2)) {
				t.Errorf("reloadTokenRates mutated inForce: %s", asJSON(t, inForce))
			}
		})
	}
}

func TestTokenRatesVersion(t *testing.T) {
	t.Parallel()

	parse := func(input, output, cacheRead any) TokenRates {
		rates, _ := ParseTokenRates(map[string]any{"k": map[string]any{"input_per_mtok": input, "output_per_mtok": output, "cache_read_per_mtok": cacheRead}}, true)
		return rates
	}

	tests := []struct {
		name      string
		a, b      TokenRates
		wantEqual bool
	}{
		{"integer and float spellings", parse(3, 15, 0.3), parse(3.0, int64(15), 0.3), true},
		{"input differs", parse(3, 15, 0.3), parse(4, 15, 0.3), false},
		{"cache read differs", parse(3, 15, 0.3), parse(3, 15, 0.4), false},
		{"unset cache read differs from zero", parse(3, 15, nil), parse(3, 15, 0), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, b := tt.a.Version(), tt.b.Version()

			if (a == b) != tt.wantEqual || !strings.HasPrefix(a, "sha256:") {
				t.Errorf("Version() of %s and %s = %q, %q, want sha256 digests, equal %t", asJSON(t, tt.a), asJSON(t, tt.b), a, b, tt.wantEqual)
			}
		})
	}

	if got := TokenRates(nil).Version(); got != "" {
		t.Errorf("Version() of nil rates = %q, want empty", got)
	}
}
