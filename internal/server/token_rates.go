package server

import (
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/orchestrator"
)

// rateKeys lists the four recognized token-rate keys, in the order
// ParseTokenRates checks them within one kind's entry.
var rateKeys = []string{"input_per_mtok", "output_per_mtok", "cache_read_per_mtok", "cache_write_per_mtok"}

// TokenRateConfig holds per-token-type USD rates for cost estimation.
// All rates are in USD per 1 million tokens (per-mtok). A nil pointer
// indicates the rate is not configured; cost estimation is suppressed
// for that token type.
type TokenRateConfig struct {
	InputPerMtok      *float64
	OutputPerMtok     *float64
	CacheReadPerMtok  *float64
	CacheWritePerMtok *float64
}

// TokenRates maps agent adapter kind strings to their token rate
// configuration. A nil or empty map means no cost estimates are shown
// on the dashboard.
type TokenRates map[string]TokenRateConfig

// ParseTokenRates extracts and validates token rates from the raw
// "token_rates" extension value. present tells whether the key
// existed in the extensions map at all, distinguishing an absent key
// from one holding a nil value. Returns nil rates when the key is
// absent or empty. Warnings are advisory and do not prevent boot.
func ParseTokenRates(rawSection any, present bool) (TokenRates, []string) {
	if !present || rawSection == nil {
		return nil, nil
	}

	topMap, ok := rawSection.(map[string]any)
	if !ok {
		return nil, []string{fmt.Sprintf("token_rates: expected map, got %T", rawSection)}
	}
	if len(topMap) == 0 {
		return nil, nil
	}

	var warnings []string
	rates := make(TokenRates, len(topMap))

	for _, kind := range slices.Sorted(maps.Keys(topMap)) {
		val := topMap[kind]
		if kind == "" {
			warnings = append(warnings, "token_rates: entry keyed to the empty string is dropped")
			continue
		}

		var cfg TokenRateConfig
		kindMap, ok := val.(map[string]any)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("token_rates.%s: expected map, got %T", kind, val))
			rates[kind] = cfg
			continue
		}

		if v, w := extractRate(kindMap, "input_per_mtok", kind); w != "" {
			warnings = append(warnings, w)
		} else {
			cfg.InputPerMtok = v
		}
		if v, w := extractRate(kindMap, "output_per_mtok", kind); w != "" {
			warnings = append(warnings, w)
		} else {
			cfg.OutputPerMtok = v
		}
		if v, w := extractRate(kindMap, "cache_read_per_mtok", kind); w != "" {
			warnings = append(warnings, w)
		} else {
			cfg.CacheReadPerMtok = v
		}
		if v, w := extractRate(kindMap, "cache_write_per_mtok", kind); w != "" {
			warnings = append(warnings, w)
		} else {
			cfg.CacheWritePerMtok = v
		}

		for _, key := range slices.Sorted(maps.Keys(kindMap)) {
			if slices.Contains(rateKeys, key) {
				continue
			}
			warnings = append(warnings, fmt.Sprintf("token_rates.%s.%s: unrecognized key is ignored", kind, key))
		}

		if cfg.InputPerMtok == nil || cfg.OutputPerMtok == nil {
			warnings = append(warnings,
				fmt.Sprintf("token_rates.%s: entry needs both input_per_mtok and output_per_mtok and prices nothing", kind))
		}

		rates[kind] = cfg
	}

	if len(rates) == 0 {
		return nil, warnings
	}
	return rates, warnings
}

// TokenRateAdvisories runs ParseTokenRates over cfg's "token_rates"
// extension value and returns one [config.Advisory] per warning, in
// warning order. It performs no I/O and no mutation.
func TokenRateAdvisories(cfg config.ServiceConfig) []config.Advisory {
	rawTokenRates, present := cfg.ExtensionValue("token_rates")
	_, warnings := ParseTokenRates(rawTokenRates, present)

	var advisories []config.Advisory
	for _, w := range warnings {
		advisories = append(advisories, config.Advisory{
			Check:   "token_rates",
			Text:    w,
			Message: "skipped invalid token rate entry",
			Attrs:   []slog.Attr{slog.String("detail", w)},
		})
	}
	return advisories
}

// extractRate reads a non-negative, finite float64 from a map entry.
// Returns nil with empty warning when the key is absent, nil with a
// warning when the value is invalid.
func extractRate(m map[string]any, key, kind string) (*float64, string) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil, ""
	}

	var f float64
	switch v := raw.(type) {
	case float64:
		f = v
	case int:
		f = float64(v)
	case int64:
		f = float64(v)
	default:
		return nil, fmt.Sprintf("token_rates.%s.%s: expected number, got %T", kind, key, raw)
	}

	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Sprintf("token_rates.%s.%s: rate %v is not finite", kind, key, f)
	}
	if f < 0 {
		return nil, fmt.Sprintf("token_rates.%s.%s: negative rate %v", kind, key, f)
	}
	return &f, ""
}

// EstimateCost prices usage under the disjoint-bucket rule: fresh
// input, cache reads, cache writes, and output are each priced once,
// at their own rate. An unset cache-read or cache-write rate prices
// that class at the input rate. Returns nil when rates is nil or
// lacks InputPerMtok or OutputPerMtok; it never reads usage.TotalTokens.
func EstimateCost(usage domain.TokenUsage, rates *TokenRateConfig) *float64 {
	if rates == nil || rates.InputPerMtok == nil || rates.OutputPerMtok == nil {
		return nil
	}

	readRate := *rates.InputPerMtok
	if rates.CacheReadPerMtok != nil {
		readRate = *rates.CacheReadPerMtok
	}
	writeRate := *rates.InputPerMtok
	if rates.CacheWritePerMtok != nil {
		writeRate = *rates.CacheWritePerMtok
	}

	fresh := max(usage.InputTokens-usage.CacheReadTokens-usage.CacheWriteTokens, 0)
	cost := (float64(fresh)*(*rates.InputPerMtok) +
		float64(usage.CacheReadTokens)*readRate +
		float64(usage.CacheWriteTokens)*writeRate +
		float64(usage.OutputTokens)*(*rates.OutputPerMtok)) / 1_000_000
	return &cost
}

// runningEntryCost prices usage under kind's configured rate. priced is
// true only when rates holds an entry for kind and EstimateCost returns
// a non-nil figure for it.
func runningEntryCost(kind string, usage domain.TokenUsage, rates TokenRates) (cost *float64, priced bool) {
	rc, ok := rates[kind]
	if !ok {
		return nil, false
	}
	c := EstimateCost(usage, &rc)
	return c, c != nil
}

// activeCostTotal sums the estimated USD cost of running, measured
// sessions with a configured rate, and counts the measured sessions
// left out for want of one. anySet is false when no session priced,
// distinguishing that from a priced total of zero.
func activeCostTotal(running []orchestrator.SnapshotRunningEntry, tokenRates TokenRates) (total float64, anySet bool, unpriced int) {
	for _, e := range running {
		if !e.UsageMeasured {
			continue
		}
		usage := domain.TokenUsage{
			InputTokens:      e.AgentInputTokens,
			OutputTokens:     e.AgentOutputTokens,
			CacheReadTokens:  e.CacheReadTokens,
			CacheWriteTokens: e.CacheWriteTokens,
		}
		if c, priced := runningEntryCost(e.AgentKind, usage, tokenRates); priced {
			total += *c
			anySet = true
		} else {
			unpriced++
		}
	}
	return total, anySet, unpriced
}

// FormatCost formats a USD cost value as a string with two decimal places.
// Values >= 1000 receive comma thousand separators (e.g. "$1,234.56").
// Rounding is performed on the integer-cents representation to avoid
// float splitting artifacts near boundaries (e.g. 999.999 -> "$1,000.00").
func FormatCost(v float64) string {
	cents := int64(math.Round(v * 100))
	dollars := cents / 100
	remainder := cents % 100
	if dollars >= 1000 {
		return fmt.Sprintf("$%s.%02d", FormatInt(dollars), remainder)
	}
	return fmt.Sprintf("$%d.%02d", dollars, remainder)
}
