package pricing

import (
	"errors"
	"slices"
	"time"

	"github.com/sortie-ai/sortie/internal/config"
)

const (
	fetchSettingPath = "price_table.fetch"
	fetchEnvVar      = "SORTIE_PRICE_TABLE_FETCH"
)

// TableStatus is the state of one price table source.
type TableStatus string

const (
	// TableOff marks a source the settings turn off.
	TableOff TableStatus = "off"

	// TableNotLoaded marks an enabled download that has delivered no table
	// and has not failed.
	TableNotLoaded TableStatus = "not_loaded"

	// TableLoaded marks a source whose table is held.
	TableLoaded TableStatus = "loaded"

	// TableFailing marks a source whose latest load or try failed.
	TableFailing TableStatus = "failing"
)

// TableSource is the state of one price table source.
type TableSource struct {
	Status TableStatus

	// InForce is true when the table is one of the table layers of the
	// [Rates].
	InForce bool

	// OffBy names what turned a fetched table off: "SORTIE_PRICE_TABLE_FETCH"
	// or "price_table.fetch". It is empty unless Status is [TableOff].
	OffBy string

	// File is the price_table.replace_file setting value, after $VAR
	// resolution and before ~ expansion and path joining. It is empty for
	// every other source.
	File string

	// Address is the download address as scheme, host, and path only; empty
	// when the source has none or the address does not parse.
	Address string

	// Version is [Table.Version]; empty when no table is held.
	Version string

	// AsOf is [Table.AsOf]; zero when no table is held.
	AsOf time.Time

	// Models is [Table.Len]; 0 when no table is held.
	Models int

	NextTryAt time.Time
	Failures  int

	// FailedAt is zero unless Status is [TableFailing].
	FailedAt time.Time

	// Err is empty unless Status is [TableFailing]. It is unmasked; the
	// caller masks it before serving it.
	Err string
}

// TokenRatesSource states the token_rates entries in force.
type TokenRatesSource struct {
	KindEntries, ModelEntries int

	// Version is [TokenRates.Version]; empty when there is no entry.
	Version string
}

// Sources states every price source of a [Rates].
type Sources struct {
	// InForce is the layer of the highest-precedence table in force; empty
	// when no table layer is in force.
	InForce RateLayer

	// AsOf is the [Table.AsOf] of that table.
	AsOf time.Time

	// AgeDays is the whole days from AsOf to [SourcesInput.Now], never below
	// 0.
	AgeDays int

	TokenRates TokenRatesSource

	// Operator is nil unless price_table.replace_file or
	// price_table.replace_url is set.
	Operator *TableSource

	Fetched  TableSource
	Embedded TableSource
}

// SourcesInput is what [Rates.Sources] reads beyond the rates.
type SourcesInput struct {
	// Downloads reports the state of the downloaded tables; nil reports no
	// download state.
	Downloads *Downloader

	// LoadErr is the latest workflow load's error, nil when it succeeded.
	LoadErr error

	// LoadEndedAt is when that load ended.
	LoadEndedAt time.Time

	Now time.Time
}

// Sources states the state of every price source r holds. It does no I/O
// beyond [EmbeddedTable] and [Downloader.State] and sends no request. A nil
// *Rates holds no entry, no table in force, and no operator table, and reports
// the fetched and embedded tables off.
func (r *Rates) Sources(in SourcesInput) Sources {
	if r == nil {
		return Sources{Fetched: TableSource{Status: TableOff}, Embedded: TableSource{Status: TableOff}}
	}

	sources := Sources{
		TokenRates: TokenRatesSource{KindEntries: len(r.kinds), ModelEntries: len(r.models), Version: r.version},
		Operator:   r.operatorSource(in),
		Fetched:    r.fetchedSource(in),
		Embedded:   r.embeddedSource(),
	}
	if len(r.layers) > 0 {
		top := r.layers[0]
		sources.InForce, sources.AsOf = layerOf(top.source), top.table.AsOf()
		sources.AgeDays = max(0, int(in.Now.Sub(sources.AsOf)/(24*time.Hour)))
	}
	return sources
}

func (r *Rates) inForce(source PriceSource) bool {
	return slices.ContainsFunc(r.layers, func(layer tableLayer) bool { return layer.source == source })
}

func (r *Rates) operatorSource(in SourcesInput) *TableSource {
	switch {
	case r.settings.replaceFile != "":
		source := withTable(TableSource{Status: TableLoaded, InForce: r.inForce(SourceOperator), File: r.settings.replaceFile}, r.fileTable)
		if loadErr, ok := errors.AsType[*config.ConfigError](in.LoadErr); ok && loadErr.Check == priceTableReplaceFileCheck {
			source.Status, source.FailedAt, source.Err = TableFailing, in.LoadEndedAt, in.LoadErr.Error()
		}
		return &source
	case r.settings.replaceURL != "":
		source := fromDownload(in.Downloads.State(Download{Role: DownloadOperator, URL: r.settings.replaceURL}))
		source.InForce, source.Address = r.inForce(SourceOperator), displayAddress(r.settings.replaceURL)
		return &source
	}
	return nil
}

func (r *Rates) fetchedSource(in SourcesInput) TableSource {
	address := displayAddress(r.settings.fetchURL)
	if !r.settings.fetch {
		offBy := fetchSettingPath
		if r.settings.fetchFromEnv {
			offBy = fetchEnvVar
		}
		return TableSource{Status: TableOff, OffBy: offBy, Address: address}
	}
	source := fromDownload(in.Downloads.State(Download{Role: DownloadShared, URL: r.settings.fetchURL}))
	source.InForce, source.Address = r.inForce(SourceFetched), address
	return source
}

func (r *Rates) embeddedSource() TableSource {
	source := TableSource{Status: TableOff, InForce: r.inForce(SourceEmbedded)}
	if r.settings.embedded {
		source.Status = TableLoaded
	}
	if table, err := EmbeddedTable(); err == nil {
		source = withTable(source, table)
	}
	return source
}

func fromDownload(state DownloadState) TableSource {
	source := TableSource{
		Status:    TableNotLoaded,
		NextTryAt: state.NextTryAt,
		Failures:  state.Failures,
		FailedAt:  state.FailedAt,
		Err:       state.Err,
	}
	switch {
	case state.Failures > 0:
		source.Status = TableFailing
	case state.Table != nil:
		source.Status = TableLoaded
	}
	return withTable(source, state.Table)
}

// withTable sets the facts of table on source; a nil table sets none.
func withTable(source TableSource, table *Table) TableSource {
	if table == nil {
		return source
	}
	source.Version, source.AsOf, source.Models = table.Version(), table.AsOf(), table.Len()
	return source
}
