package sentence

import (
	"regexp"
	"slices"
	"strings"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/persistence"
)

const (
	contextCanceled = "context canceled"
	contextDeadline = "context deadline exceeded"
)

var (
	agentTurnEnded = regexp.MustCompile(`(?s)^agent turn (\d+) ended: (.*)$`)
	agentTurn      = regexp.MustCompile(`(?s)^agent turn (\d+): (.*)$`)
	agentKindChain = regexp.MustCompile(`(?s)^agent: ([a-z_]+): (.*)$`)
	turnStage      = regexp.MustCompile(`(?s)^(prompt render|issue state refresh) \(turn (\d+)\): (.*)$`)
)

// agentErrorKinds are the adapter error kinds that have a run-error sentence.
var agentErrorKinds = []domain.AgentErrorKind{
	domain.ErrAgentNotFound, domain.ErrInvalidWorkspaceCwd, domain.ErrResponseTimeout,
	domain.ErrTurnTimeout, domain.ErrPortExit, domain.ErrResponseError, domain.ErrTurnFailed,
	domain.ErrTurnCancelled, domain.ErrTurnInputRequired, domain.ErrTurnIncomplete,
	domain.ErrTurnRefused, domain.ErrTurnOutcomeUnknown, domain.ErrTurnTokenLimit,
	domain.ErrTurnRequestLimit, domain.ErrCredentialUnverified,
}

// stagePrefixes maps the stage a worker named in front of an error to what
// the worker was doing.
var stagePrefixes = []struct{ prefix, doing string }{
	{"workspace preparation: ", "preparing the workspace"},
	{"mcp config generation: ", "setting up the agent's tool configuration"},
}

// RunError returns the text a person reads for a stored run or retry error.
// A withheld handoff, an agent turn error chain and a worker stage error read
// as one plain sentence, also when a retry entry wrapped them as a worker
// exit; any other error is returned unchanged. The stored value stays the key
// that run history queries match on.
func RunError(stored string) string {
	unwrapped := strings.TrimPrefix(stored, orchestrator.WorkerExitedErrorPrefix)
	if text, ok := withheldHandoffText(unwrapped); ok {
		return text
	}
	if text, ok := workerErrorText(unwrapped); ok {
		return text
	}
	return stored
}

func withheldHandoffText(stored string) (string, bool) {
	rest, withheld := strings.CutPrefix(stored, persistence.HandoffAbsenceErrorPrefix)
	if !withheld {
		return "", false
	}
	switch {
	case strings.HasPrefix(rest, persistence.HandoffVerdictAbsence):
		return render("run_error_absence", "text", nil), true
	case strings.HasPrefix(rest, persistence.HandoffVerdictUndetermined):
		return render("run_error_undetermined", "text", nil), true
	}
	return render("run_error_withheld", "text", map[string]any{"Detail": detailOf(rest)}), true
}

func workerErrorText(stored string) (string, bool) {
	if m := agentTurnEnded.FindStringSubmatch(stored); m != nil {
		return turnEndedText(turnWords(m[1]), m[2]), true
	}
	if m := agentTurn.FindStringSubmatch(stored); m != nil {
		return agentErrorText(turnWords(m[1]), false, m[2]), true
	}
	if rest, ok := strings.CutPrefix(stored, "agent session start: "); ok {
		return agentErrorText("the first turn", true, rest), true
	}
	if m := turnStage.FindStringSubmatch(stored); m != nil {
		doing := "building the prompt for turn " + m[2]
		if m[1] == "issue state refresh" {
			doing = "checking the issue's state after turn " + m[2]
		}
		return stageText(doing, m[3]), true
	}
	for _, stage := range stagePrefixes {
		if rest, ok := strings.CutPrefix(stored, stage.prefix); ok {
			return stageText(stage.doing, rest), true
		}
	}
	if rest, ok := strings.CutPrefix(stored, "worker panic: "); ok {
		return render("run_error_panic", "text", map[string]any{"Detail": detailOf(rest)}), true
	}
	switch stored {
	case contextCanceled:
		return render("run_error_stopped", "text", nil), true
	case contextDeadline:
		return render("run_error_out_of_time", "text", nil), true
	}
	return "", false
}

func turnWords(number string) string {
	return "turn " + number
}

// agentErrorText words the error chain an adapter returned for a turn or a
// session start. An error that is no adapter error reads as a generic agent
// error with the chain's text.
func agentErrorText(turn string, start bool, chain string) string {
	data := map[string]any{"Turn": turn, "Start": start, "Detail": detailOf(chain)}
	m := agentKindChain.FindStringSubmatch(chain)
	if m == nil {
		switch {
		case hasContextError(chain, contextCanceled):
			return render("run_error_turn_cancelled", "text", data)
		case hasContextError(chain, contextDeadline):
			return render("run_error_out_of_time", "text", nil)
		}
		return render("run_error_agent_other", "text", data)
	}
	data["Detail"] = detailOf(m[2])
	if !slices.Contains(agentErrorKinds, domain.AgentErrorKind(m[1])) {
		return render("run_error_agent_other", "text", data)
	}
	return render("run_error_"+m[1], "text", data)
}

func turnEndedText(turn, reason string) string {
	switch reason {
	case "turn_cancelled":
		return render("run_error_turn_cancelled", "text", map[string]any{"Turn": turn})
	case "turn_failed":
		return render("run_error_turn_failed", "text", map[string]any{"Turn": turn})
	}
	return render("run_error_turn_ended", "text", map[string]any{
		"Turn":   turn,
		"Detail": strings.ReplaceAll(reason, "_", " "),
	})
}

func stageText(doing, cause string) string {
	data := map[string]any{"Doing": doing, "Detail": detailOf(cause)}
	switch {
	case hasContextError(cause, contextCanceled):
		return render("run_error_stage_stopped", "text", data)
	case hasContextError(cause, contextDeadline):
		return render("run_error_stage_timeout", "text", data)
	}
	return render("run_error_stage", "text", data)
}

func hasContextError(chain, noise string) bool {
	return chain == noise || strings.HasSuffix(chain, ": "+noise)
}

// detailOf returns the text of an error chain worth showing a person: the
// chain without a closing context error, which only says why the chain ended,
// and without a closing period, which the sentence supplies.
func detailOf(chain string) string {
	for _, noise := range []string{contextCanceled, contextDeadline} {
		if chain == noise {
			return ""
		}
		chain = strings.TrimSuffix(chain, ": "+noise)
	}
	return strings.TrimSuffix(strings.TrimSpace(chain), ".")
}
