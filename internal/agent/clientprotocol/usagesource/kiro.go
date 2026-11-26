package usagesource

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/registry"
)

const kiroAgentName = "kiro"

const kiroExecutableName = "kiro-cli"

const kiroModelFlag = "--model"

const kiroVerificationAgent = "sortie_verify"

// kiroVerificationAgentConfig is an agent with no tools, no resources, and no
// tool-server configuration. It sets no model, so the launch's own --model
// stands.
const kiroVerificationAgentConfig = `{"name":"sortie_verify","description":"Sortie credential verification","prompt":"Answer the user.","tools":[],"resources":[],"mcpServers":{},"includeMcpJson":false}`

func init() {
	VerificationAdditions.Register(kiroAgentName, func() VerificationAddition { return &kiroVerification{} })
}

// kiroVerification selects an agent with no tools for a verification session.
// The runtime reads agent definitions from a directory named by an environment
// variable, so the definition is written here, on the launching host, and
// removed with the session.
type kiroVerification struct {
	dir string
}

// Claim refuses a launch without --model: selecting an agent replaces the
// operator's default agent, which could carry the model the launch would
// otherwise use.
func (v *kiroVerification) Claim(target agentcore.LaunchTarget) (VerificationSwitches, bool) {
	if !namesRuntime(target, kiroExecutableName) || !kiroSetsModel(target) {
		return VerificationSwitches{}, false
	}

	dir, err := os.MkdirTemp("", "sortie-verification-")
	if err != nil {
		return VerificationSwitches{}, false
	}
	if err := os.WriteFile(filepath.Join(dir, kiroVerificationAgent+".json"), []byte(kiroVerificationAgentConfig), 0o600); err != nil {
		_ = os.RemoveAll(dir) //nolint:errcheck // best-effort; the claim is refused either way
		return VerificationSwitches{}, false
	}

	v.dir = dir
	return VerificationSwitches{
		Env:  []string{"KIRO_AGENT_CONFIG_DIR=" + dir},
		Args: []string{"--agent", kiroVerificationAgent},
	}, true
}

// Context keeps instruction files and the system prompt because the runtime
// reads them whatever agent is selected.
func (v *kiroVerification) Context() registry.VerificationContext {
	return registry.DeclareVerificationContext(
		registry.VerificationKeepsProjectInstructions,
		registry.VerificationKeepsUserInstructions,
		registry.VerificationKeepsSystemPrompt,
	)
}

// Close retries a removal that failed on its next call.
func (v *kiroVerification) Close() {
	if v.dir == "" {
		return
	}
	if err := os.RemoveAll(v.dir); err != nil {
		return
	}
	v.dir = ""
}

// kiroSetsModel reports whether the command line carries --model with a value,
// the only spelling the runtime accepts.
func kiroSetsModel(target agentcore.LaunchTarget) bool {
	tokens := append([]string{target.Command}, target.Args...)
	for i, token := range tokens {
		if value, ok := strings.CutPrefix(token, kiroModelFlag+"="); ok && value != "" {
			return true
		}
		if token == kiroModelFlag && i+1 < len(tokens) {
			return true
		}
	}
	return false
}
