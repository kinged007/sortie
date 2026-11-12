package codex

import (
	"github.com/sortie-ai/sortie/internal/agent/mcpconfig"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/typeutil"
)

// passthroughConfig holds Codex-specific settings extracted from the
// "codex" sub-object in WORKFLOW.md. All fields are optional with
// zero-value meaning "not configured."
type passthroughConfig struct {
	Model             string
	Effort            string
	ApprovalPolicy    string
	ThreadSandbox     string
	TurnSandboxPolicy map[string]any
	Personality       string
}

// parsePassthroughConfig extracts Codex-specific settings from the raw
// config map. A missing key uses its zero-value default. A key present
// with a non-string value for a string field reports a fault rather
// than defaulting.
func parsePassthroughConfig(config map[string]any) (passthroughConfig, *typeutil.TypeFault) {
	model, fault := registry.ModelSetting(config)
	if fault != nil {
		return passthroughConfig{}, fault
	}
	effort, fault := registry.EffortSetting(config)
	if fault != nil {
		return passthroughConfig{}, fault
	}
	approvalPolicy, fault := typeutil.StringField(config, "approval_policy")
	if fault != nil {
		return passthroughConfig{}, fault
	}
	threadSandbox, fault := typeutil.StringField(config, "thread_sandbox")
	if fault != nil {
		return passthroughConfig{}, fault
	}
	personality, fault := typeutil.StringField(config, "personality")
	if fault != nil {
		return passthroughConfig{}, fault
	}

	return passthroughConfig{
		Model:             model,
		Effort:            effort,
		ApprovalPolicy:    approvalPolicy,
		ThreadSandbox:     threadSandbox,
		TurnSandboxPolicy: typeutil.MapFrom(config, "turn_sandbox_policy"),
		Personality:       personality,
	}, nil
}

// placeToolServers carries a remote launch's tool-server arguments as late
// arguments.
func placeToolServers(d mcpconfig.Delivery) sshutil.LatePreamble {
	return sshutil.LatePreamble{Args: d.Args}
}
