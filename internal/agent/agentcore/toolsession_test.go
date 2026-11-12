package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/agent/mcpconfig"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/agent/toolrelay"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/waittest"
)

const (
	toolChildScenario = "agentcore.toolchild"
	envBlockSecret    = "seeded-env-block-secret"
	operatorHeader    = "Bearer seeded-operator-header"
	testDialect       = registry.MCPDialectMCPServersJSON
	headerEnv         = mcpconfig.HeaderEnvPrefix + "1"
	operatorHTTP      = `"operator-http":{"type":"http","url":"https://ops.example/mcp","headers":{"Authorization":"` + operatorHeader + `"}}`
	operatorStdio     = `"operator-stdio":{"command":"/usr/bin/ops"}`
	announcedPort     = 45678
)

var discardLogger = slog.New(slog.DiscardHandler)

type toolChildParams struct {
	PIDFile string
	Silent  bool
}

func init() {
	scenarios[toolChildScenario] = agenttest.Typed(runToolChild)
}

func runToolChild(args []string, p toolChildParams) int {
	if !slices.Contains(args, "--http") {
		return 2
	}
	if p.PIDFile != "" {
		if err := os.WriteFile(p.PIDFile, fmt.Appendf(nil, "%d", os.Getpid()), 0o600); err != nil {
			return 2
		}
	}
	if p.Silent {
		return 3
	}
	fmt.Printf("{\"listening\":\"127.0.0.1:%d\"}\n", announcedPort)
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

func toolsEntry(t *testing.T, params toolChildParams) string {
	t.Helper()
	return fmt.Sprintf(`"sortie-tools":{"command":%q,"args":["mcp-server"],"env":{"TRACKER_PAT":%q}}`,
		agenttest.FakeRuntime(t, t.TempDir(), "sortie", toolChildScenario, params), envBlockSecret)
}

func writeConfig(t *testing.T, entries ...string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), ".sortie", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(`{"mcpServers":{`+strings.Join(entries, ",")+`}}`), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return path
}

func newStandIn(t *testing.T, includeDD bool, forward agenttest.SSHForward) string {
	t.Helper()

	binDir := t.TempDir()
	tools := []string{"sh"}
	if includeDD {
		tools = append(tools, "dd")
	}
	for _, tool := range tools {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not found on PATH: %v", tool, err)
		}
		if err := os.Symlink(path, filepath.Join(binDir, tool)); err != nil {
			t.Fatalf("Symlink(%s): %v", tool, err)
		}
	}
	return agenttest.NewSSHStandIn(t, t.TempDir(), agenttest.SSHStandIn{PATH: binDir, Forward: forward})
}

func readWire(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(got)
}

func recordSSH(t *testing.T, standIn string) (command string, wire func() (argv, stdin string)) {
	t.Helper()

	dir := t.TempDir()
	argvLog, stdinLog := filepath.Join(dir, "argv"), filepath.Join(dir, "stdin")
	command = agenttest.WriteScript(t, dir, "ssh", fmt.Sprintf(
		"printf '%%s\\n' \"$@\" >> '%s'\ntee -a '%s' | '%s' \"$@\"\n", argvLog, stdinLog, standIn))
	return command, func() (string, string) {
		return readWire(t, argvLog), readWire(t, stdinLog)
	}
}

func startSession(t *testing.T, params domain.StartSessionParams, target LaunchTarget, dialect registry.MCPDialect) *ToolSession {
	t.Helper()

	session, agentErr := ResolveToolDelivery(waittest.Context(t), params, target, dialect, discardLogger)
	if agentErr != nil {
		t.Fatalf("ResolveToolDelivery() = %v, want nil", agentErr)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("ToolSession.Close() = %v, want nil", err)
		}
	})
	return session
}

type remoteSession struct {
	*ToolSession
	target LaunchTarget
	wire   func() (argv, stdin string)
}

func resolveRemote(t *testing.T, forward agenttest.SSHForward, configPath string) remoteSession {
	t.Helper()

	command, wire := recordSSH(t, newStandIn(t, true, forward))
	target := LaunchTarget{Command: command, SSHHost: "user@stand-in-host", WorkspacePath: t.TempDir(), RemoteCommand: "true"}
	session := startSession(t, domain.StartSessionParams{MCPConfigPath: configPath}, target, testDialect)
	return remoteSession{ToolSession: session, target: target, wire: wire}
}

func fullConfig(t *testing.T) string {
	t.Helper()
	return writeConfig(t, toolsEntry(t, toolChildParams{}), operatorHTTP, operatorStdio)
}

type declaredServer struct {
	URL     string `json:"url"`
	Command string `json:"command"`
}

func declaredServers(t *testing.T, doc []byte) map[string]declaredServer {
	t.Helper()

	var parsed struct {
		MCPServers map[string]declaredServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("Unmarshal(%q): %v", doc, err)
	}
	return parsed.MCPServers
}

func allocated(port int) sshutil.ForwardOutcome { //nolint:unparam // the unix-only fork-per-turn test passes the relay's own host port; every call built on Windows passes 40000
	return sshutil.ForwardOutcome{Status: sshutil.ForwardAllocated, RemotePort: port}
}

func placeDocument(d mcpconfig.Delivery) sshutil.LatePreamble {
	return sshutil.LatePreamble{Args: []string{"--mcp", string(d.Document)}}
}

func pendingRelay(t *testing.T) *toolrelay.Relay {
	t.Helper()

	relay := toolrelay.Start(waittest.Context(t), toolrelay.StartParams{
		ToolServer: mcpconfig.StdioLaunch{Command: agenttest.FakeRuntime(t, t.TempDir(), "sortie", toolChildScenario, toolChildParams{})},
		Logger:     discardLogger,
	})
	t.Cleanup(func() {
		if err := relay.Close(); err != nil {
			t.Errorf("Relay.Close() = %v, want nil", err)
		}
	})
	return relay
}

func TestResolveToolDelivery_RemoteWithholdsOperatorStdio(t *testing.T) {
	t.Parallel()

	session := resolveRemote(t, agenttest.SSHForwardAllocated, fullConfig(t))

	servers := declaredServers(t, session.Delivery(allocated(40000)).Document)
	if got, want := slices.Sorted(maps.Keys(servers)), []string{"operator-http", "sortie-tools"}; !slices.Equal(got, want) {
		t.Errorf("Delivery() servers = %q, want %q", got, want)
	}
	if got, want := servers["sortie-tools"].URL, "http://127.0.0.1:40000/mcp"; got != want {
		t.Errorf("Delivery() sortie-tools url = %q, want %q", got, want)
	}
	if late := session.Forward(nil).Render(allocated(40000)); len(late.Env) != 0 || len(late.Args) != 0 {
		t.Errorf("Forward(nil).Render() = %v, want an empty late preamble", late)
	}
}

func TestResolveToolDelivery_RemoteRelayNeverAnnounced(t *testing.T) {
	t.Parallel()

	config := writeConfig(t, toolsEntry(t, toolChildParams{Silent: true}), operatorHTTP, operatorStdio)

	session := resolveRemote(t, agenttest.SSHForwardAllocated, config)

	if got := session.Forward(placeDocument); got != nil {
		t.Errorf("Forward() = %+v, want nil without an announced listener", got)
	}
	if got := declaredServers(t, session.Delivery(allocated(40000)).Document); len(got) != 0 {
		t.Errorf("Delivery() servers = %v, want none", got)
	}
	if !session.NoToolChannel() {
		t.Error("NoToolChannel() = false, want true for an unavailable relay")
	}
}

func TestResolveToolDelivery_RemoteProbeDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		forward agenttest.SSHForward
		wantGap toolrelay.GapReason
	}{
		{name: "allocation leaves the decision pending", forward: agenttest.SSHForwardAllocated},
		{name: "refusal is a gap", forward: agenttest.SSHForwardRefused, wantGap: toolrelay.GapForwardRefused},
		{name: "silence is a gap", forward: agenttest.SSHForwardSilent, wantGap: toolrelay.GapForwardUnconfirmed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := resolveRemote(t, tt.forward, fullConfig(t))

			decision := session.currentRelay().Decision()
			if decision.Gap != tt.wantGap || decision.Delivered {
				t.Errorf("Decision() = %+v, want gap %q and not delivered", decision, tt.wantGap)
			}
		})
	}
}

func TestResolveToolDelivery_CancelledDuringProbe(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the ssh stand-in spawns sh")
	}

	ready := filepath.Join(t.TempDir(), "probe.ready")
	command := agenttest.WriteScript(t, t.TempDir(), "ssh", fmt.Sprintf("echo up > '%s'\nexec cat\n", ready))
	target := LaunchTarget{Command: command, SSHHost: "user@stand-in-host", WorkspacePath: t.TempDir(), RemoteCommand: "true"}
	params := domain.StartSessionParams{MCPConfigPath: writeConfig(t, toolsEntry(t, toolChildParams{}))}
	ctx, cancel := context.WithCancel(waittest.Context(t))
	t.Cleanup(cancel)
	resolved := make(chan *domain.AgentError, 1)
	go func() {
		_, agentErr := ResolveToolDelivery(ctx, params, target, testDialect, discardLogger)
		resolved <- agentErr
	}()

	waittest.File(t, ready)
	cancel()

	agentErr := waittest.Receive(t, resolved, "ResolveToolDelivery after cancellation")
	if agentErr == nil || agentErr.Kind != domain.ErrTurnCancelled {
		t.Errorf("ResolveToolDelivery() error = %v, want kind %q", agentErr, domain.ErrTurnCancelled)
	}
}

func TestToolSession_Forward_LateEntriesPerSettledDecision(t *testing.T) {
	t.Parallel()

	const remotePort = 40000
	refused := sshutil.ForwardOutcome{Status: sshutil.ForwardRefused}
	tests := []struct {
		name       string
		settle     func(*ToolSession, *sshutil.ReverseForward)
		outcome    sshutil.ForwardOutcome
		wantHeader bool
		wantTools  bool
	}{
		{name: "allocated outcome carries the bearer and declares sortie-tools", outcome: allocated(remotePort), wantHeader: true, wantTools: true},
		{name: "refused outcome carries the header only", outcome: refused, wantHeader: true},
		{name: "runtime without HTTP withholds the endpoint and the bearer", settle: func(s *ToolSession, _ *sshutil.ReverseForward) { s.WithholdForRuntime() }, outcome: allocated(remotePort)},
		{name: "forward gap alone keeps both for a later allocated outcome", settle: func(_ *ToolSession, fwd *sshutil.ReverseForward) { fwd.Resolved(refused) }, outcome: allocated(remotePort), wantHeader: true, wantTools: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := resolveRemote(t, agenttest.SSHForwardAllocated, fullConfig(t))
			fwd := session.Forward(placeDocument)
			if tt.settle != nil {
				tt.settle(session.ToolSession, fwd)
			}

			late := fwd.Render(tt.outcome)

			env := make(map[string]string)
			for _, entry := range late.Env {
				env[entry.Name] = entry.Value
			}
			if got := env[headerEnv]; (got == operatorHeader) != tt.wantHeader {
				t.Errorf("Render(%+v) header entry = %q, want present = %v", tt.outcome, got, tt.wantHeader)
			}
			_, hasBearer := env[mcpconfig.BearerEnv]
			_, hasTools := declaredServers(t, []byte(late.Args[1]))["sortie-tools"]
			if hasBearer != tt.wantTools || hasTools != tt.wantTools {
				t.Errorf("Render(%+v) carries bearer = %v, declares sortie-tools = %v, want both %v", tt.outcome, hasBearer, hasTools, tt.wantTools)
			}
		})
	}
}

func TestToolSession_ServeLocal_SilentRelayServesNothingUsable(t *testing.T) {
	t.Parallel()

	params := domain.StartSessionParams{MCPConfigPath: writeConfig(t, toolsEntry(t, toolChildParams{Silent: true}))}
	session := startSession(t, params, LaunchTarget{}, testDialect)

	serving := session.ServeLocal(waittest.Context(t))

	tools := declaredServers(t, session.Delivery(sshutil.ForwardOutcome{}).Document)["sortie-tools"]
	if serving || tools.URL != "" || tools.Command != "" {
		t.Errorf("ServeLocal() = %v with sortie-tools %+v, want false and no url or command", serving, tools)
	}
}

func TestToolSession_Emit_OpeningNotice(t *testing.T) {
	t.Parallel()

	const (
		started   = domain.EventSessionStarted
		work      = domain.EventTokenUsage
		completed = domain.EventTurnCompleted
	)
	tests := []struct {
		name          string
		relay         func(*testing.T) *toolrelay.Relay
		refused       bool
		defaultNotice bool
		turns         [][]domain.AgentEventType
		want          []string
	}{
		{name: "no relay: right after the first started event", turns: [][]domain.AgentEventType{{started, work, completed}}, want: []string{"session_started", "notice gap=false", "token_usage", "turn_completed"}},
		{name: "final gap: right after the first started event", relay: pendingRelay, refused: true, turns: [][]domain.AgentEventType{{started, work, completed}}, want: []string{"session_started", "notice gap=true", "token_usage", "turn_completed"}},
		{name: "pending: TurnCompleted settles the gap before the notice, which precedes the terminal event", relay: pendingRelay, turns: [][]domain.AgentEventType{{started, work, completed}}, want: []string{"session_started", "token_usage", "notice gap=true", "turn_completed"}},
		{name: "no second notice on a later turn", relay: pendingRelay, refused: true, turns: [][]domain.AgentEventType{{started, completed}, {started, completed}}, want: []string{"session_started", "notice gap=true", "turn_completed", "session_started", "turn_completed"}},
		{name: "nil notice emits the gap notice for a gap", relay: pendingRelay, refused: true, defaultNotice: true, turns: [][]domain.AgentEventType{{started, completed}}, want: []string{"session_started", toolrelay.GapNotice, "turn_completed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := &ToolSession{}
			if tt.relay != nil {
				session.relay = tt.relay(t)
			}
			if tt.refused {
				session.relay.Observe(sshutil.ForwardOutcome{Status: sshutil.ForwardRefused})
			}
			notice := OpeningNotice(func(emit func(domain.AgentEvent), toolServersGap bool) {
				EmitNotification(emit, fmt.Sprintf("notice gap=%t", toolServersGap))
			})
			if tt.defaultNotice {
				notice = nil
			}
			var got []string
			record := func(event domain.AgentEvent) {
				if event.Type == domain.EventNotification {
					got = append(got, event.Message)
					return
				}
				got = append(got, string(event.Type))
			}

			for _, turn := range tt.turns {
				emit := session.Emit(record, notice)
				for _, eventType := range turn {
					emit(domain.AgentEvent{Type: eventType})
				}
			}

			if !slices.Equal(got, tt.want) {
				t.Errorf("Emit() forwarded %q, want %q", got, tt.want)
			}
		})
	}
}
