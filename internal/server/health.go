package server

import (
	"time"

	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/sentence"
)

type stateHealthResponse struct {
	Database  healthCheckResponse    `json:"database"`
	Preflight healthCheckResponse    `json:"preflight"`
	Workflow  workflowHealthResponse `json:"workflow"`
	Poll      pollHealthResponse     `json:"poll"`
	SCM       *scmHealthResponse     `json:"scm"`
}

type healthCheckResponse struct {
	Status              string     `json:"status"`
	CheckedAt           *time.Time `json:"checked_at"`
	LastSuccessAt       *time.Time `json:"last_success_at"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	FailingSince        *time.Time `json:"failing_since"`
	Error               string     `json:"error,omitempty"`
}

type workflowHealthResponse struct {
	File string `json:"file,omitempty"`
	healthCheckResponse
}

type pollHealthResponse struct {
	healthCheckResponse
	Outcome     string     `json:"outcome,omitempty"`
	NextPollAt  *time.Time `json:"next_poll_at"`
	SummaryText string     `json:"summary_text"`
}

type scmHealthResponse struct {
	Provider string `json:"provider"`
	healthCheckResponse
}

func toStateHealthResponse(h orchestrator.SnapshotHealth, words sentence.Context) *stateHealthResponse {
	resp := &stateHealthResponse{
		Database:  toHealthCheckResponse(h.Database),
		Preflight: toHealthCheckResponse(h.Preflight.SnapshotHealthCheck),
		Workflow: workflowHealthResponse{
			File:                h.Workflow.File,
			healthCheckResponse: toHealthCheckResponse(h.Workflow.SnapshotHealthCheck),
		},
		Poll: pollHealthResponse{
			healthCheckResponse: toHealthCheckResponse(h.Poll.SnapshotHealthCheck),
			Outcome:             h.Poll.Outcome,
			NextPollAt:          utcOrNil(h.Poll.NextPollAt),
			SummaryText:         sentence.PollHealth(h.Poll, words),
		},
	}
	if h.SCM != nil {
		resp.SCM = &scmHealthResponse{
			Provider:            h.SCM.Provider,
			healthCheckResponse: toHealthCheckResponse(h.SCM.SnapshotHealthCheck),
		}
	}
	return resp
}

// toHealthCheckResponse derives status from the failure count so a client
// cannot be served a status that contradicts the run of failures beside it.
func toHealthCheckResponse(c orchestrator.SnapshotHealthCheck) healthCheckResponse {
	status := "pass"
	if c.ConsecutiveFailures > 0 {
		status = "fail"
	}
	return healthCheckResponse{
		Status:              status,
		CheckedAt:           utcOrNil(c.CheckedAt),
		LastSuccessAt:       utcOrNil(c.LastSuccessAt),
		ConsecutiveFailures: c.ConsecutiveFailures,
		FailingSince:        utcOrNil(c.FailingSince),
		Error:               c.Error,
	}
}
