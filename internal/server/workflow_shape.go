package server

import "github.com/sortie-ai/sortie/internal/orchestrator"

// configResponse is the JSON wire format of the config member of
// GET /api/v1/state.
type configResponse struct {
	orchestrator.WorkflowShape
	TokenRates map[string]tokenRateResponse `json:"token_rates"`
}

// tokenRateResponse is one agent kind's entry in config.token_rates.
// Unset cache rates stay null rather than showing the input rate they
// are priced at.
type tokenRateResponse struct {
	InputPerMtok      *float64 `json:"input_per_mtok"`
	OutputPerMtok     *float64 `json:"output_per_mtok"`
	CacheReadPerMtok  *float64 `json:"cache_read_per_mtok"`
	CacheWritePerMtok *float64 `json:"cache_write_per_mtok"`
	Complete          bool     `json:"complete"`
}

// toConfigResponse joins the shape with the rates the process started with.
// The rates serialize as null when none are configured.
func toConfigResponse(shape orchestrator.WorkflowShape, rates TokenRates) *configResponse {
	resp := &configResponse{WorkflowShape: shape}
	if len(rates) == 0 {
		return resp
	}

	resp.TokenRates = make(map[string]tokenRateResponse, len(rates))
	for kind, rate := range rates {
		resp.TokenRates[kind] = tokenRateResponse{
			InputPerMtok:      rate.InputPerMtok,
			OutputPerMtok:     rate.OutputPerMtok,
			CacheReadPerMtok:  rate.CacheReadPerMtok,
			CacheWritePerMtok: rate.CacheWritePerMtok,
			Complete:          rate.InputPerMtok != nil && rate.OutputPerMtok != nil,
		}
	}
	return resp
}
