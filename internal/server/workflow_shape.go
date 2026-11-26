package server

import (
	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/pricing"
)

// configResponse is the JSON wire format of the config member of
// GET /api/v1/state.
type configResponse struct {
	orchestrator.WorkflowShape
	TokenRates map[string]tokenRateResponse `json:"token_rates"`
}

// tokenRateResponse is one entry in config.token_rates. Unset cache rates
// stay null rather than showing the rate they are priced at.
type tokenRateResponse struct {
	InputPerMtok      *float64 `json:"input_per_mtok"`
	OutputPerMtok     *float64 `json:"output_per_mtok"`
	CacheReadPerMtok  *float64 `json:"cache_read_per_mtok"`
	CacheWritePerMtok *float64 `json:"cache_write_per_mtok"`
	Complete          bool     `json:"complete"`
	PerKind           bool     `json:"per_kind"`
}

// toConfigResponse joins the shape with the rates in force.
// The rates serialize as null when no entry is configured.
func toConfigResponse(shape orchestrator.WorkflowShape, rates *pricing.Rates) *configResponse {
	resp := &configResponse{WorkflowShape: shape}
	entries := rates.Entries()
	if len(entries) == 0 {
		return resp
	}

	resp.TokenRates = make(map[string]tokenRateResponse, len(entries))
	for _, entry := range entries {
		resp.TokenRates[entry.Key] = tokenRateResponse{
			InputPerMtok:      entry.Rates.InputPerMtok,
			OutputPerMtok:     entry.Rates.OutputPerMtok,
			CacheReadPerMtok:  entry.Rates.CacheReadPerMtok,
			CacheWritePerMtok: entry.Rates.CacheWritePerMtok,
			Complete:          entry.Complete(),
			PerKind:           entry.PerKind,
		}
	}
	return resp
}
