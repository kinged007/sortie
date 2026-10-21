package pi

import (
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/typeutil"
)

type passthroughConfig struct {
	Model   string
	Approve bool
}

// parsePassthroughConfig extracts pi-specific settings from the raw config
// map. A key it does not read is accepted and ignored.
func parsePassthroughConfig(config map[string]any) (passthroughConfig, *typeutil.TypeFault) {
	model, fault := registry.ModelSetting(config)
	if fault != nil {
		return passthroughConfig{}, fault
	}

	return passthroughConfig{
		Model:   model,
		Approve: typeutil.BoolFrom(config, "approve", false),
	}, nil
}

// buildRunArgs returns the arguments of one turn. The prompt is read from
// standard input, so a prompt beginning with "@", which pi treats as a file
// to include, and a prompt longer than the argument limit never reach the
// command line. A credential verification session passes --no-session so it
// leaves no session file behind.
func buildRunArgs(state *sessionState, pt passthroughConfig) []string {
	args := []string{"-p", "--mode", "json"}

	if state.credentialVerification {
		args = append(args, "--no-session")
	} else if state.sessionID != "" {
		args = append(args, "--session", state.sessionID)
	}
	if pt.Model != "" {
		args = append(args, "--model", pt.Model)
	}
	if pt.Approve {
		args = append(args, "--approve")
	} else {
		args = append(args, "--no-approve")
	}

	return args
}
