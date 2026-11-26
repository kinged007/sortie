package pricing

import (
	"strconv"

	"github.com/sortie-ai/sortie/internal/domain"
)

// RateLayer names where a rate comes from; "" is no layer.
type RateLayer string

const (
	// LayerKindEntry is a token_rates entry keyed by an agent kind.
	LayerKindEntry RateLayer = "kind_entry"

	// LayerModelEntry is a token_rates entry keyed by a model.
	LayerModelEntry RateLayer = "model_entry"

	// LayerOperatorTable is the price table the operator names with
	// price_table.replace_file or price_table.replace_url.
	LayerOperatorTable RateLayer = "operator_table"

	// LayerFetched is the shared price table downloaded at runtime.
	LayerFetched RateLayer = "fetched"

	// LayerEmbedded is the price table compiled into the binary.
	LayerEmbedded RateLayer = "embedded"
)

// ClassRate is the rate of one token class and where it comes from.
type ClassRate struct {
	// PerMtok is USD per one million tokens; nil when no layer rates the
	// class.
	PerMtok *float64

	// Layer is "" when PerMtok is nil.
	Layer RateLayer

	// FromInput marks a kind entry's unset cache rate, priced at its input
	// rate.
	FromInput bool
}

// ModelQuote states the rates the next run of one agent kind and model gets,
// and how that run is priced.
type ModelQuote struct {
	// TableKey is the key in the first table layer that lists the name; ""
	// when none does.
	TableKey string

	// Layer is the layer that prices the model; "" when none does.
	Layer RateLayer

	// EntryKey is the token_rates key of a kind or model entry; "" otherwise.
	EntryKey string

	Input, Output, CacheRead, CacheWrite ClassRate

	// Status is [CostEstimated], [CostPartial], or [CostUnpriced].
	Status CostStatus

	// Unrated lists the classes no rate covers, in the order of
	// [PartPrice.Unrated]; nil when none.
	Unrated []TokenClass
}

// Quote states the rates [PriceRun] applies to a run of the agent kind and
// model, taking every key, entry, layer, and rate from the lookups
// [PriceRun] uses so the quote cannot disagree with the price. A nil *Rates
// rates nothing. Quote is pure.
func (r *Rates) Quote(kind, model string) ModelQuote {
	var quote ModelQuote
	if r != nil {
		quote = r.quoteRates(kind, model)
	}
	quote.Status, quote.Unrated = quoteOutcome(r, kind, model)
	return quote
}

func (r *Rates) quoteRates(kind, model string) ModelQuote {
	var (
		row    TableEntry
		layer  int
		listed bool
	)
	for i, candidate := range r.layers {
		if entry, ok := candidate.table.Lookup(model); ok {
			row, layer, listed = entry, i, true
			break
		}
	}

	quote := ModelQuote{TableKey: row.Key}
	var tableLayer RateLayer
	if listed {
		tableLayer = layerOf(r.layers[layer].source)
	}
	var fromTable [rateClasses]ClassRate
	for class, perToken := range [rateClasses]*float64{
		rateInput:      row.InputPerToken,
		rateOutput:     row.OutputPerToken,
		rateCacheRead:  row.CacheReadPerToken,
		rateCacheWrite: row.CacheWritePerToken,
	} {
		if perToken != nil {
			fromTable[class] = ClassRate{PerMtok: new(ratePerMtok(*perToken)), Layer: tableLayer}
		}
	}

	var rates [rateClasses]ClassRate
	entry, entryKey, hasEntry := r.operatorModelEntry(model, layer, row, listed)
	switch {
	case r.kinds[kind]:
		quote.Layer, quote.EntryKey = LayerKindEntry, kind
		rates = kindEntryRates(r.entries[kind])
	case hasEntry:
		quote.Layer, quote.EntryKey = LayerModelEntry, entryKey
		for class, own := range entryRates(entry) {
			rates[class] = configClassRate(own, LayerModelEntry)
			if rates[class].PerMtok == nil {
				rates[class] = fromTable[class]
			}
		}
	case listed:
		quote.Layer = tableLayer
		rates = fromTable
	}
	quote.Input, quote.Output = rates[rateInput], rates[rateOutput]
	quote.CacheRead, quote.CacheWrite = rates[rateCacheRead], rates[rateCacheWrite]
	return quote
}

// kindEntryRates reads a kind entry as [PriceRun] prices it: an unset cache
// rate takes the input rate.
func kindEntryRates(entry TokenRateConfig) [rateClasses]ClassRate {
	own := entryRates(entry)
	rates := [rateClasses]ClassRate{
		rateInput:  configClassRate(own[rateInput], LayerKindEntry),
		rateOutput: configClassRate(own[rateOutput], LayerKindEntry),
	}
	for _, class := range [...]int{rateCacheRead, rateCacheWrite} {
		rates[class] = configClassRate(own[class], LayerKindEntry)
		if rates[class].PerMtok == nil && rates[rateInput].PerMtok != nil {
			rates[class] = ClassRate{PerMtok: new(*rates[rateInput].PerMtok), Layer: LayerKindEntry, FromInput: true}
		}
	}
	return rates
}

func entryRates(entry TokenRateConfig) [rateClasses]*float64 {
	return [rateClasses]*float64{
		rateInput:      entry.InputPerMtok,
		rateOutput:     entry.OutputPerMtok,
		rateCacheRead:  entry.CacheReadPerMtok,
		rateCacheWrite: entry.CacheWritePerMtok,
	}
}

func configClassRate(rate *float64, layer RateLayer) ClassRate {
	if rate == nil {
		return ClassRate{}
	}
	return ClassRate{PerMtok: new(*rate), Layer: layer}
}

// ratePerMtok converts a table rate to USD per one million tokens, rounded to
// ten significant digits so that 3e-06 per token reads 3 and not
// 3.0000000000000004.
func ratePerMtok(perToken float64) float64 {
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(perToken*1_000_000, 'g', 10, 64), 64)
	return rounded
}

// layerOf names the rate layer a table layer recorded under source supplies.
func layerOf(source PriceSource) RateLayer {
	switch source {
	case SourceOperator:
		return LayerOperatorTable
	case SourceFetched:
		return LayerFetched
	case SourceEmbedded:
		return LayerEmbedded
	}
	return ""
}

// quoteOutcome takes the cost status and unrated classes from the part
// [PriceRun] records for model when a run of kind spends tokens in every
// class.
func quoteOutcome(r *Rates, kind, model string) (CostStatus, []TokenClass) {
	spent := domain.TokenUsage{InputTokens: 3, OutputTokens: 1, TotalTokens: 4, CacheReadTokens: 1, CacheWriteTokens: 1}
	price := PriceRun(Spend{Kind: kind, Measured: true, Totals: spent, Parts: map[string]domain.TokenUsage{model: spent}}, r)
	part := price.Parts[model]
	return part.Status, part.Unrated
}
