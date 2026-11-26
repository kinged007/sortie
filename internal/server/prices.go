package server

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/pricing"
)

// ModelUse is one agent kind and model pair that recorded runs used; it
// mirrors the persistence type because the server keeps its own types
// behind the run-history reader.
type ModelUse struct {
	AgentKind  string
	Model      string
	LastUsedAt time.Time
	Runs       int
}

// ModelUseFunc lists every agent kind and model pair with a recorded run;
// ctx is the request's context. It must be safe for concurrent use.
type ModelUseFunc func(ctx context.Context) ([]ModelUse, error)

type pricesResponse struct {
	GeneratedAt  string                   `json:"generated_at"`
	TableInForce *tableInForceResponse    `json:"table_in_force"`
	TokenRates   tokenRatesSourceResponse `json:"token_rates"`
	Operator     *tableSourceResponse     `json:"operator"`
	Fetched      tableSourceResponse      `json:"fetched"`
	Embedded     tableSourceResponse      `json:"embedded"`
	Models       []modelPriceResponse     `json:"models"`
}

type tableInForceResponse struct {
	Layer   string `json:"layer"`
	AsOf    string `json:"as_of"`
	AgeDays int    `json:"age_days"`
}

type tokenRatesSourceResponse struct {
	KindEntries  int     `json:"kind_entries"`
	ModelEntries int     `json:"model_entries"`
	Version      *string `json:"version"`
}

type tableSourceResponse struct {
	Status    string  `json:"status"`
	InForce   bool    `json:"in_force"`
	OffBy     *string `json:"off_by"`
	File      *string `json:"file"`
	Address   *string `json:"address"`
	Version   *string `json:"version"`
	AsOf      *string `json:"as_of"`
	Models    *int    `json:"models"`
	NextTryAt *string `json:"next_try_at"`
	Failures  int     `json:"failures"`
	FailedAt  *string `json:"failed_at"`
	Error     *string `json:"error"`
}

type modelPriceResponse struct {
	AgentKind      string            `json:"agent_kind"`
	Model          string            `json:"model"`
	TableKey       *string           `json:"table_key"`
	Layer          *string           `json:"layer"`
	EntryKey       *string           `json:"entry_key"`
	Input          classRateResponse `json:"input"`
	Output         classRateResponse `json:"output"`
	CacheRead      classRateResponse `json:"cache_read"`
	CacheWrite     classRateResponse `json:"cache_write"`
	CostStatus     string            `json:"cost_status"`
	UnratedClasses []string          `json:"unrated_classes"`
	LastUsedAt     *string           `json:"last_used_at"`
	Runs           int               `json:"runs"`
}

type classRateResponse struct {
	PerMtok   *float64 `json:"per_mtok"`
	Layer     *string  `json:"layer"`
	FromInput bool     `json:"from_input"`
}

func (s *Server) routePrices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.methodNotAllowed(w, r, http.MethodGet)
		return
	}
	s.handlePrices(w, r)
}

func (s *Server) handlePrices(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	if s.modelUse == nil {
		writeHistoryUnavailable(w, s.logger)
		return
	}

	uses, err := s.modelUse(r.Context())
	if r.Context().Err() != nil {
		s.logger.Debug("prices request canceled",
			slog.String("path", r.URL.Path),
			slog.Any("error", err),
		)
		return
	}
	if err != nil {
		s.historyReadFailed(w, r, err)
		return
	}

	now := start.UTC()
	var rates *pricing.Rates
	if s.ratesFn != nil {
		rates = s.ratesFn()
	}
	var loadEndedAt time.Time
	var loadErr error
	if s.workflowLoadFn != nil {
		loadEndedAt, loadErr = s.workflowLoadFn()
	}
	var configured []orchestrator.ConfiguredModel
	if s.configuredModelsFn != nil {
		configured = s.configuredModelsFn()
	}

	response := sourcesBody(rates.Sources(pricing.SourcesInput{
		Downloads:   s.priceDownloads,
		LoadErr:     loadErr,
		LoadEndedAt: loadEndedAt,
		Now:         now,
	}), s.maskErrorFn)
	response.GeneratedAt = timeText(now)
	response.Models = make([]modelPriceResponse, 0, len(uses)+len(configured))
	for _, use := range modelRows(uses, configured) {
		response.Models = append(response.Models, quoteRow(use, rates.Quote(use.AgentKind, use.Model)))
	}
	writeJSON(w, s.logger, http.StatusOK, response)

	s.logger.Debug("request served",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", http.StatusOK),
		slog.Duration("duration", time.Since(start)),
	)
}

// modelRows merges the recorded pairs with the configured ones, which have no
// last use and no runs, and orders them latest use first.
func modelRows(uses []ModelUse, configured []orchestrator.ConfiguredModel) []ModelUse {
	type key struct{ agentKind, model string }
	seen := make(map[key]bool, len(uses)+len(configured))
	rows := make([]ModelUse, 0, len(uses)+len(configured))
	for _, use := range uses {
		if !seen[key{use.AgentKind, use.Model}] {
			seen[key{use.AgentKind, use.Model}] = true
			rows = append(rows, use)
		}
	}
	for _, c := range configured {
		if !seen[key{c.AgentKind, c.Model}] {
			seen[key{c.AgentKind, c.Model}] = true
			rows = append(rows, ModelUse{AgentKind: c.AgentKind, Model: c.Model})
		}
	}
	slices.SortFunc(rows, func(a, b ModelUse) int {
		return cmp.Or(
			b.LastUsedAt.Compare(a.LastUsedAt),
			strings.Compare(a.AgentKind, b.AgentKind),
			strings.Compare(a.Model, b.Model),
		)
	})
	return rows
}

// quoteRow maps the quote of one agent kind and model, with its recorded use,
// onto a row.
func quoteRow(use ModelUse, quote pricing.ModelQuote) modelPriceResponse {
	unrated := make([]string, 0, len(quote.Unrated))
	for _, class := range quote.Unrated {
		unrated = append(unrated, string(class))
	}
	return modelPriceResponse{
		AgentKind:      use.AgentKind,
		Model:          use.Model,
		TableKey:       textOrNil(quote.TableKey),
		Layer:          textOrNil(string(quote.Layer)),
		EntryKey:       textOrNil(quote.EntryKey),
		Input:          classRateBody(quote.Input),
		Output:         classRateBody(quote.Output),
		CacheRead:      classRateBody(quote.CacheRead),
		CacheWrite:     classRateBody(quote.CacheWrite),
		CostStatus:     string(quote.Status),
		UnratedClasses: unrated,
		LastUsedAt:     timeOrNil(use.LastUsedAt),
		Runs:           use.Runs,
	}
}

func classRateBody(rate pricing.ClassRate) classRateResponse {
	return classRateResponse{PerMtok: rate.PerMtok, Layer: textOrNil(string(rate.Layer)), FromInput: rate.FromInput}
}

// sourcesBody maps the price sources onto the response; the caller sets the
// generation time and the rows. Every error passes mask, and a nil mask serves
// no error text.
func sourcesBody(sources pricing.Sources, mask func(string) string) pricesResponse {
	body := pricesResponse{
		TokenRates: tokenRatesSourceResponse{
			KindEntries:  sources.TokenRates.KindEntries,
			ModelEntries: sources.TokenRates.ModelEntries,
			Version:      textOrNil(sources.TokenRates.Version),
		},
		Fetched:  tableSourceBody(sources.Fetched, mask),
		Embedded: tableSourceBody(sources.Embedded, mask),
	}
	if sources.InForce != "" {
		body.TableInForce = &tableInForceResponse{
			Layer:   string(sources.InForce),
			AsOf:    timeText(sources.AsOf),
			AgeDays: sources.AgeDays,
		}
	}
	if sources.Operator != nil {
		operator := tableSourceBody(*sources.Operator, mask)
		body.Operator = &operator
	}
	return body
}

func tableSourceBody(source pricing.TableSource, mask func(string) string) tableSourceResponse {
	body := tableSourceResponse{
		Status:    string(source.Status),
		InForce:   source.InForce,
		OffBy:     textOrNil(source.OffBy),
		File:      textOrNil(source.File),
		Address:   textOrNil(source.Address),
		Version:   textOrNil(source.Version),
		AsOf:      timeOrNil(source.AsOf),
		NextTryAt: timeOrNil(source.NextTryAt),
		Failures:  source.Failures,
		FailedAt:  timeOrNil(source.FailedAt),
	}
	if source.Models != 0 {
		body.Models = &source.Models
	}
	if mask != nil && source.Err != "" {
		body.Error = textOrNil(mask(source.Err))
	}
	return body
}

func textOrNil(text string) *string {
	if text == "" {
		return nil
	}
	return &text
}

func timeOrNil(at time.Time) *string {
	if at.IsZero() {
		return nil
	}
	return new(timeText(at))
}

func timeText(at time.Time) string {
	return at.UTC().Format(time.RFC3339Nano)
}
