package toolrelay

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/agent/mcpconfig"
)

const childScenario = "toolrelay.child"

const (
	childConnect      = "connect"
	childRefuseAccept = "refuse_accept"
	childExit         = "exit"
)

type childParams struct {
	Announce   string
	PIDFile    string
	BearerFile string
}

func runChild(args []string, p childParams) int {
	if !slices.Contains(args, httpFlag) {
		return 2
	}
	if p.PIDFile != "" {
		if err := os.WriteFile(p.PIDFile, fmt.Appendf(nil, "%d", os.Getpid()), 0o600); err != nil {
			return 2
		}
	}
	if p.BearerFile != "" {
		if err := os.WriteFile(p.BearerFile, []byte(os.Getenv(mcpconfig.BearerEnv)), 0o600); err != nil {
			return 2
		}
	}
	if p.Announce == "" {
		return 3
	}
	fmt.Printf("{\"listening\":%q}\n", p.Announce)

	lines := bufio.NewScanner(os.Stdin)
	for lines.Scan() {
		switch lines.Text() {
		case childConnect:
			fmt.Println(`{"connected":true}`)
		case childRefuseAccept:
			fmt.Println(`{"refused":"accept","accept":"text/plain"}`)
		case childExit:
			return 3
		}
	}
	if lines.Err() != nil {
		return 3
	}
	return 0
}

func TestMain(m *testing.M) {
	agenttest.Main(m, map[string]agenttest.Scenario{
		childScenario: agenttest.Typed(runChild),
	})
}
