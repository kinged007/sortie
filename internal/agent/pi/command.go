package pi

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sortie-ai/sortie/internal/typeutil"
)

type passthroughConfig struct {
	Model        string
	Thinking     string
	ProjectTrust projectTrust
	AllowedTools []string
	DeniedTools  []string
}

// projectTrust is the operator's decision about whether pi loads the
// workspace's own project-local files and packages.
type projectTrust string

const (
	// trustIgnore keeps project-local files and packages out of the run.
	// It is the default, because an unattended turn has no operator to
	// answer a trust prompt and must not silently adopt whatever the
	// workspace declares.
	trustIgnore projectTrust = "ignore"

	// trustApprove loads them.
	trustApprove projectTrust = "approve"
)

// validThinkingLevels are the --thinking levels pi 0.85.1 accepts, and
// the only values a pi block's thinking key may take.
var validThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// knownConfigKeys is every key the pi block of WORKFLOW.md accepts. A
// key outside it is refused rather than ignored: pi has no passthrough
// channel, so an unrecognized key would otherwise look configured while
// doing nothing.
var knownConfigKeys = map[string]struct{}{
	"model":         {},
	"thinking":      {},
	"project_trust": {},
	"allowed_tools": {},
	"denied_tools":  {},
}

// genericConfigKeys are the keys the agent block contributes to every
// adapter's config map, whichever kind it names. They are not pi block
// keys, so the unknown-key check exempts them: refusing them would
// reject every valid workflow rather than one misspelled option.
var genericConfigKeys = map[string]struct{}{
	"kind":             {},
	"command":          {},
	"turn_timeout_ms":  {},
	"read_timeout_ms":  {},
	"stall_timeout_ms": {},
	"stop_grace_ms":    {},
}

// configFault is one reason a pi passthrough is refused. Check is the
// diagnostic key, Message the text both the constructor and the
// registered validator report, so the two can never disagree.
type configFault struct {
	Check   string
	Message string
}

// fault builds one configFault under the adapter's diagnostic namespace.
func fault(check, format string, args ...any) *configFault {
	return &configFault{Check: "pi." + check, Message: fmt.Sprintf(format, args...)}
}

// parsePassthroughConfig extracts pi-specific settings from the raw
// config map, reporting every fault it finds rather than only the first,
// so one pass tells an operator everything that needs fixing.
func parsePassthroughConfig(config map[string]any) (passthroughConfig, []*configFault) {
	var faults []*configFault

	for _, key := range unknownConfigKeys(config) {
		faults = append(faults, fault(key+".unknown_key",
			"unknown pi config key %q; the pi block accepts %s",
			key, strings.Join(sortedConfigKeys(), ", ")))
	}

	pt := passthroughConfig{}

	model, modelFault := typeutil.StringField(config, "model")
	if modelFault != nil {
		faults = append(faults, fault(modelFault.Key+".wrong_type", "%s", modelFault.Error()))
	}
	pt.Model = model

	thinking, thinkingFault := typeutil.StringField(config, "thinking")
	switch {
	case thinkingFault != nil:
		faults = append(faults, fault(thinkingFault.Key+".wrong_type", "%s", thinkingFault.Error()))
	case thinking != "" && !slices.Contains(validThinkingLevels, thinking):
		faults = append(faults, fault("thinking.invalid_value",
			"thinking: %q is not a pi thinking level; valid values are %s",
			thinking, strings.Join(validThinkingLevels, ", ")))
	default:
		pt.Thinking = thinking
	}

	pt.ProjectTrust = trustIgnore
	trust, trustFault := typeutil.StringField(config, "project_trust")
	switch {
	case trustFault != nil:
		faults = append(faults, fault(trustFault.Key+".wrong_type", "%s", trustFault.Error()))
	case trust == "":
	case projectTrust(trust) == trustIgnore, projectTrust(trust) == trustApprove:
		pt.ProjectTrust = projectTrust(trust)
	default:
		faults = append(faults, fault("project_trust.invalid_value",
			"project_trust: %q is not a pi trust setting; valid values are %s, %s",
			trust, trustIgnore, trustApprove))
	}

	allowed, allowedFaults := toolList(config, "allowed_tools")
	faults = append(faults, allowedFaults...)
	pt.AllowedTools = allowed

	denied, deniedFaults := toolList(config, "denied_tools")
	faults = append(faults, deniedFaults...)
	pt.DeniedTools = denied

	if message := overlapMessage(pt.AllowedTools, pt.DeniedTools); message != "" {
		faults = append(faults, fault("allowed_tools.overlap", "%s", message))
	}

	if len(faults) > 0 {
		return passthroughConfig{}, faults
	}
	return pt, nil
}

// toolList reads one tool-list key. A value that is not a list of
// non-empty tool names is refused: typeutil.ExtractStringSlice drops
// what it cannot read, so accepting its output would hand pi a shorter
// list than the operator wrote.
func toolList(config map[string]any, key string) ([]string, []*configFault) {
	raw, present := config[key]
	if !present || raw == nil {
		return nil, nil
	}

	var items []any
	switch typed := raw.(type) {
	case []any:
		items = typed
	case []string:
		items = make([]any, len(typed))
		for i, name := range typed {
			items[i] = name
		}
	default:
		return nil, []*configFault{fault(key+".wrong_type",
			"%s: expected list, got %s", key, typeutil.DescribeYAMLType(raw))}
	}

	var names []string
	var faults []*configFault
	for i, item := range items {
		name, ok := item.(string)
		if !ok {
			faults = append(faults, fault(key+".malformed_list",
				"%s[%d]: expected a tool name, got %s", key, i, typeutil.DescribeYAMLType(item)))
			continue
		}
		if strings.TrimSpace(name) == "" {
			faults = append(faults, fault(key+".malformed_list", "%s[%d]: tool name is empty", key, i))
			continue
		}
		names = append(names, name)
	}
	return names, faults
}

// unknownConfigKeys returns the config keys pi does not accept, sorted.
func unknownConfigKeys(config map[string]any) []string {
	var unknown []string
	for key := range config {
		if _, ok := knownConfigKeys[key]; ok {
			continue
		}
		if _, ok := genericConfigKeys[key]; ok {
			continue
		}
		unknown = append(unknown, key)
	}
	slices.Sort(unknown)
	return unknown
}

// sortedConfigKeys returns knownConfigKeys' keys in a stable order, so
// the unknown-key diagnostic renders identically on every run.
func sortedConfigKeys() []string {
	keys := make([]string, 0, len(knownConfigKeys))
	for key := range knownConfigKeys {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// pi runs non-interactive with -p --mode json; the prompt is a positional
// arg after --, and cmd.Dir is already the workspace, so no --dir.
func buildRunArgs(state *sessionState, prompt string, pt passthroughConfig) []string {
	args := []string{"-p", "--mode", "json"}

	if state.sessionID != "" {
		args = append(args, "--session", state.sessionID)
	}
	if pt.Model != "" {
		args = append(args, "--model", pt.Model)
	}
	if pt.Thinking != "" {
		args = append(args, "--thinking", pt.Thinking)
	}
	if len(pt.AllowedTools) > 0 {
		args = append(args, "--tools", strings.Join(pt.AllowedTools, ","))
	}
	if len(pt.DeniedTools) > 0 {
		args = append(args, "--exclude-tools", strings.Join(pt.DeniedTools, ","))
	}
	if pt.ProjectTrust == trustApprove {
		args = append(args, "--approve")
	} else {
		args = append(args, "--no-approve")
	}

	args = append(args, "--", prompt)
	return args
}
