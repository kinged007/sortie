package agenttest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/mcpconfig"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/workspacekit"
)

const seededMCPConfig = `{"mcpServers":{
"sortie-tools":{"command":"/usr/bin/sortie","args":["mcp-server"]},
"operator-http":{"type":"http","url":"https://ops.example/mcp","headers":{"Authorization":"Bearer seeded-header-value"}},
"operator-stdio":{"command":"/usr/bin/ops","env":{"OPS_TOKEN":"seeded-env-value"}},
"disabled-server":{"command":"/usr/bin/off","enabled":false}}}`

// WriteSeededMCPConfig writes a generated MCP configuration with every server
// kind [AssertMCPInjection] checks into workspace/.sortie/mcp.json and returns
// its path.
func WriteSeededMCPConfig(t *testing.T, workspace string) string {
	t.Helper()

	sortieDir, err := workspacekit.OpenSortieDir(workspace, true)
	if err != nil {
		t.Fatalf("open %s: %v", workspace, err)
	}
	defer sortieDir.Close() //nolint:errcheck // a directory handle holds no buffered data, and ReplaceFile has already reported the write result
	if err := workspacekit.ReplaceFile(sortieDir, "mcp.json", []byte(seededMCPConfig)); err != nil {
		t.Fatalf("write MCP config in %s: %v", workspace, err)
	}
	return filepath.Join(workspace, workspacekit.SortieDir, "mcp.json")
}

// MCPLaunchSurface is every channel by which an adapter can hand the
// generated MCP config path to the agent process for one session.
type MCPLaunchSurface struct {
	Args []string
	Env  []string

	// Wire carries the message bodies the adapter wrote to the agent's
	// standard input. A value counts as delivered when a body carries
	// it verbatim, or as encoding/json renders it inside a JSON string
	// with the surrounding quotes removed; either rendering counts
	// only when it ends exactly where the enclosing JSON string ends.
	// A nil Wire contributes no hit.
	Wire []string

	// File is the derived file an mcp_servers_json kind hands its runtime in
	// place of the generated configuration, in the same directory.
	File string
}

// AssertMCPInjection fails t unless dialect, the kind's registered
// [registry.MCPDialect], agrees with surface, the launch surface the adapter
// produced for a local session carrying mcpConfigPath. A disabled server
// reaches no surface, a header value reaches the runtime only through its
// reference, and a stdio env value reaches no argument. A kind declaring
// [registry.MCPDialectNone] must not carry the path.
//
// An element carries the path when it is the path exactly, or when the
// path is its tail behind a single composition marker: an adapter may
// pass the path prefixed with an '@', and an environment entry carries
// it as KEY=<path>. An element that continues past the path names a
// different file, so it is not a match; the workspace holds a
// <path>.tmp beside the generated file during generation, and an
// adapter handed that one has delivered nothing.
func AssertMCPInjection(t *testing.T, dialect registry.MCPDialect, mcpConfigPath string, surface MCPLaunchSurface) {
	t.Helper()
	assertMCPInjection(t, dialect, mcpConfigPath, surface)
}

// mcpInjectionReporter is the minimal reporting surface
// assertMCPInjection needs; [*testing.T] satisfies it. Splitting the
// check out from [AssertMCPInjection] lets a package-internal test
// drive the same failure-detection logic against a lightweight
// double, since a *testing.T's own failure state cannot itself be
// inspected without failing the enclosing test.
type mcpInjectionReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

func assertMCPInjection(t mcpInjectionReporter, dialect registry.MCPDialect, mcpConfigPath string, surface MCPLaunchSurface) {
	t.Helper()

	if mcpConfigPath == "" {
		t.Errorf("mcpConfigPath is empty, want a non-empty path to look for")
		return
	}

	switch dialect {
	case registry.MCPDialectMCPServersJSON, registry.MCPDialectMCPServersJSONAlwaysLoad:
		assertDerivedConfig(t, dialect, mcpConfigPath, surface)
	case registry.MCPDialectMCPServersTOML, registry.MCPDialectMCPMemberJSON, registry.MCPDialectACPSession:
		assertRenderedServers(t, dialect, mcpConfigPath, surface)
	case registry.MCPDialectNone:
		if surfaceCarriesPath(surface, mcpConfigPath) {
			t.Errorf("dialect = %q, want the surface not to carry mcpConfigPath", dialect)
		}
	case registry.MCPDialectUndeclared:
		t.Errorf("dialect = %q, want a kind that declares an MCP dialect", dialect)
	default:
		t.Errorf("dialect = %q, want a declared MCP dialect", dialect)
	}
}

// assertRenderedServers matches by substring, unlike carriesMCPConfigPath: a
// rendering embeds each server inside one larger value (a TOML inline table, a
// JSON document).
func assertRenderedServers(t mcpInjectionReporter, dialect registry.MCPDialect, mcpConfigPath string, surface MCPLaunchSurface) {
	t.Helper()

	servers, err := mcpconfig.Parse(mcpConfigPath)
	if err != nil {
		t.Errorf("mcpconfig.Parse(%q) error = %v, want a real generated file for dialect %q", mcpConfigPath, err, dialect)
		return
	}

	assertServerRules(t, dialect, servers, surface, "")

	for _, server := range mcpconfig.Declarable(servers) {
		if !surfaceContains(surface, server.Name) {
			t.Errorf("dialect = %q, server %q not found on the launch surface", dialect, server.Name)
		}
		if server.Transport == mcpconfig.TransportStdio && !surfaceContains(surface, server.Command) {
			t.Errorf("dialect = %q, server %q command %q not found on the launch surface", dialect, server.Name, server.Command)
		}
		if server.Transport == mcpconfig.TransportHTTP && !surfaceContains(surface, server.URL) {
			t.Errorf("dialect = %q, server %q url %q not found on the launch surface", dialect, server.Name, server.URL)
		}
		if server.Transport == mcpconfig.TransportStdio {
			for name := range server.Env {
				if !surfaceContains(surface, name) {
					t.Errorf("dialect = %q, server %q env variable %q not found on the launch surface", dialect, server.Name, name)
				}
			}
		}
	}
}

// surfaceContains reports whether value appears as a substring of any
// argument or environment element on surface, or on a Wire element
// either verbatim or as encoding/json renders it inside a JSON string
// with the surrounding quotes removed. Returns false for an empty
// value so a stdio server with no command never passes vacuously.
func surfaceContains(surface MCPLaunchSurface, value string) bool {
	if value == "" {
		return false
	}
	return containsIn(surface.Args, value) || containsIn(surface.Env, value) || wireContains(surface.Wire, value)
}

func containsIn(elems []string, value string) bool {
	return slices.ContainsFunc(elems, func(elem string) bool { return strings.Contains(elem, value) })
}

func wireContains(wire []string, value string) bool {
	return containsIn(wire, value) || containsIn(wire, jsonStringInterior(value))
}

func surfaceCarriesPath(surface MCPLaunchSurface, path string) bool {
	return slices.ContainsFunc(surface.Args, func(arg string) bool {
		return carriesMCPConfigPath(arg, path)
	}) || slices.ContainsFunc(surface.Env, func(env string) bool {
		return carriesMCPConfigPath(env, path)
	}) || slices.ContainsFunc(surface.Wire, func(wire string) bool {
		return carriesMCPConfigPathOnWire(wire, path)
	})
}

func assertDerivedConfig(t mcpInjectionReporter, dialect registry.MCPDialect, mcpConfigPath string, surface MCPLaunchSurface) {
	t.Helper()

	if surface.File == "" {
		t.Errorf("dialect = %q, want the surface to hand the runtime a derived file, got File = %q", dialect, surface.File)
		return
	}
	if !surfaceCarriesPath(surface, surface.File) || surfaceCarriesPath(surface, mcpConfigPath) {
		t.Errorf("dialect = %q, want the surface to carry the derived file %q and not mcpConfigPath", dialect, surface.File)
	}
	if filepath.Dir(surface.File) != filepath.Dir(mcpConfigPath) {
		t.Errorf("derived file %q is not in the directory of mcpConfigPath %q", surface.File, mcpConfigPath)
		return
	}

	servers, err := mcpconfig.Parse(mcpConfigPath)
	if err != nil {
		t.Errorf("mcpconfig.Parse(%q) error = %v, want a real generated file", mcpConfigPath, err)
		return
	}
	derived, err := os.ReadFile(surface.File)
	if err != nil {
		t.Errorf("ReadFile(%q) error = %v, want the derived file", surface.File, err)
		return
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(derived, &doc); err != nil {
		t.Errorf("derived file %q is not a JSON object: %v", surface.File, err)
		return
	}

	declarable := mcpconfig.Declarable(servers)
	refs := mcpconfig.HeaderRefs(declarable)
	want := map[string]map[string]any{}
	for _, server := range declarable {
		var entry map[string]any
		if err := json.Unmarshal(server.Raw, &entry); err != nil {
			t.Errorf("server %q raw entry is not a JSON object: %v", server.Name, err)
			return
		}
		if headers, ok := entry["headers"].(map[string]any); ok {
			for _, ref := range refs {
				if ref.Server == server.Name {
					headers[ref.Header] = "${" + ref.Env + "}"
				}
			}
		}
		want[server.Name] = entry
	}
	wantAlwaysLoad := dialect == registry.MCPDialectMCPServersJSONAlwaysLoad
	if entry, ok := doc.MCPServers[mcpconfig.ToolServerName]; ok && (entry["alwaysLoad"] == true) != wantAlwaysLoad {
		t.Errorf("derived file %q %s alwaysLoad = %v, want %t for dialect %q", surface.File, mcpconfig.ToolServerName, entry["alwaysLoad"], wantAlwaysLoad, dialect)
	}
	for _, entry := range doc.MCPServers {
		delete(entry, "alwaysLoad")
	}
	if !reflect.DeepEqual(doc.MCPServers, want) {
		t.Errorf("derived file %q servers = %v, want %v", surface.File, doc.MCPServers, want)
	}

	assertServerRules(t, dialect, servers, surface, string(derived))
}

func headerReference(dialect registry.MCPDialect, env string) string {
	switch dialect {
	case registry.MCPDialectMCPServersTOML:
		return `="` + env + `"`
	case registry.MCPDialectMCPMemberJSON:
		return "{env:" + env + "}"
	default:
		return ""
	}
}

func assertServerRules(t mcpInjectionReporter, dialect registry.MCPDialect, servers []mcpconfig.Server, surface MCPLaunchSurface, derived string) {
	t.Helper()

	for _, server := range servers {
		if server.Enabled != nil && !*server.Enabled && (surfaceContains(surface, server.Name) || strings.Contains(derived, server.Name)) {
			t.Errorf("disabled server %q found on the launch surface, want it nowhere", server.Name)
		}
	}

	declarable := mcpconfig.Declarable(servers)
	onWire := dialect == registry.MCPDialectACPSession
	for _, ref := range mcpconfig.HeaderRefs(declarable) {
		carrier := ref.Env + "=" + ref.Value
		leaked := containsIn(surface.Args, ref.Value) || strings.Contains(derived, ref.Value) ||
			slices.ContainsFunc(surface.Env, func(env string) bool {
				return strings.Contains(env, ref.Value) && (onWire || env != carrier)
			})
		if leaked {
			t.Errorf("header %q of server %q found outside its delivery channel", ref.Header, ref.Server)
		}
		channel, delivered := ref.Env, slices.Contains(surface.Env, carrier)
		if onWire {
			channel, delivered = "Wire", wireContains(surface.Wire, ref.Value)
		}
		if !delivered {
			t.Errorf("header %q of server %q not delivered through %s", ref.Header, ref.Server, channel)
		}
		if fragment := headerReference(dialect, ref.Env); fragment != "" && !surfaceContains(surface, fragment) {
			t.Errorf("header %q of server %q: reference %q not found on the launch surface", ref.Header, ref.Server, fragment)
		}
	}

	for _, server := range declarable {
		for name, value := range server.Env {
			if value == "" {
				continue
			}
			if containsIn(surface.Args, value) {
				t.Errorf("env variable %q of server %q found in Args, want it off the argument list", name, server.Name)
			}
			if !surfaceContains(surface, value) && !strings.Contains(derived, value) {
				t.Errorf("env variable %q of server %q not delivered by the launch surface", name, server.Name)
			}
			if dialect != registry.MCPDialectMCPServersTOML {
				continue
			}
			if !slices.Contains(surface.Env, name+"="+value) {
				t.Errorf("env variable %q of server %q not delivered as a process environment entry", name, server.Name)
			}
			if !slices.ContainsFunc(surface.Args, func(arg string) bool {
				return strings.Contains(arg, "env_vars") && strings.Contains(arg, name)
			}) {
				t.Errorf("env variable %q of server %q not named in env_vars", name, server.Name)
			}
		}
	}
}

const remoteBearerEnv = "SORTIE_TOOL_SERVER_BEARER" //nolint:gosec // G101: an environment variable name, not a credential

// RemoteLaunchSurface is what a test captured from one remote launch.
type RemoteLaunchSurface struct {
	// Argv is the ssh argument vector.
	Argv []string

	// Stdin is every byte written to ssh's standard input.
	Stdin string

	// SessionNew holds the session-creation request bodies of an
	// agent-client-protocol launch, each a substring of Stdin. Nil marks a CLI
	// kind, whose secrets travel as late carrier assignments instead.
	SessionNew []string
}

// AssertRemoteMCPSurface fails t unless surface, captured from a remote launch
// of the configuration at mcpConfigPath with tool server bearer bearer, keeps
// host paths, disabled servers and stdio commands and env values off the
// launch, and delivers the bearer and header values only as late carrier
// assignments (CLI kinds) or inside session-creation requests
// (agent-client-protocol). An empty bearer skips the bearer checks.
func AssertRemoteMCPSurface(t *testing.T, mcpConfigPath, bearer string, surface RemoteLaunchSurface) {
	t.Helper()
	assertRemoteMCPSurface(t, mcpConfigPath, bearer, surface)
}

type remoteSecret struct {
	env   string
	value string
}

func assertRemoteMCPSurface(t mcpInjectionReporter, mcpConfigPath, bearer string, surface RemoteLaunchSurface) {
	t.Helper()

	servers, err := mcpconfig.Parse(mcpConfigPath)
	if err != nil {
		t.Errorf("mcpconfig.Parse(%q) error = %v, want a real generated file", mcpConfigPath, err)
		return
	}

	absent := map[string]string{mcpConfigPath: "configuration path"}
	for _, server := range servers {
		if server.Enabled != nil && !*server.Enabled {
			absent[server.Name] = "disabled server name"
		}
	}
	declarable := mcpconfig.Declarable(servers)
	for _, server := range declarable {
		if server.Transport != mcpconfig.TransportStdio {
			continue
		}
		if server.Name != mcpconfig.ToolServerName {
			absent[server.Name] = "stdio server name"
		}
		absent[server.Command] = "stdio server command"
		for _, value := range server.Env {
			if value != "" {
				absent[value] = "stdio env value"
			}
		}
	}
	for value, what := range absent {
		if value == "" {
			continue
		}
		if channel := remoteChannelCarrying(surface, value); channel != "" {
			t.Errorf("remote surface carries a %s on %s, want it absent", what, channel)
		}
	}

	var secrets []remoteSecret
	if bearer != "" {
		secrets = append(secrets, remoteSecret{env: remoteBearerEnv, value: bearer})
	}
	for _, ref := range mcpconfig.HeaderRefs(declarable) {
		secrets = append(secrets, remoteSecret{env: ref.Env, value: ref.Value})
	}
	for _, secret := range secrets {
		assertRemoteSecretRoute(t, secret, surface)
	}
}

func remoteChannelCarrying(surface RemoteLaunchSurface, value string) string {
	switch {
	case containsIn(surface.Argv, value):
		return "ssh argv"
	case strings.Contains(stdinOutsideSessionNew(surface), value):
		return "ssh stdin"
	case wireContains(surface.SessionNew, value):
		return "session/new"
	default:
		return ""
	}
}

func stdinOutsideSessionNew(surface RemoteLaunchSurface) string {
	rest := surface.Stdin
	for _, body := range surface.SessionNew {
		rest = strings.ReplaceAll(rest, body, "")
	}
	return rest
}

func assertRemoteSecretRoute(t mcpInjectionReporter, secret remoteSecret, surface RemoteLaunchSurface) {
	t.Helper()

	if containsIn(surface.Argv, secret.value) {
		t.Errorf("value for %s found in ssh argv, want it off the argument list", secret.env)
	}

	if surface.SessionNew != nil {
		if strings.Contains(stdinOutsideSessionNew(surface), secret.value) {
			t.Errorf("value for %s found on ssh stdin outside session/new, want it only inside", secret.env)
		}
		if !wireContains(surface.SessionNew, secret.value) {
			t.Errorf("value for %s not delivered inside session/new", secret.env)
		}
		return
	}

	delivered := false
	for start := 0; ; {
		idx := strings.Index(surface.Stdin[start:], secret.value)
		if idx == -1 {
			break
		}
		idx += start
		before := surface.Stdin[:idx]
		if !strings.HasSuffix(before, secret.env+"='") && !strings.HasSuffix(before, secret.env+"=") {
			t.Errorf("value for %s found on ssh stdin outside its late carrier assignment", secret.env)
		}
		delivered = true
		start = idx + 1
	}
	if !delivered {
		t.Errorf("value for %s not delivered as a late carrier assignment on ssh stdin", secret.env)
	}
}

var forwardOutcomeStderr = []struct {
	name   string
	stderr func(hostPort int) string
}{
	{"allocated with a one-digit port", func(hostPort int) string {
		return fmt.Sprintf("Allocated port 7 for remote forward to 127.0.0.1:%d\n", hostPort)
	}},
	{"allocated at 65535", func(hostPort int) string {
		return fmt.Sprintf("Allocated port 65535 for remote forward to 127.0.0.1:%d\n", hostPort)
	}},
	{"refused", func(int) string { return "Warning: remote port forwarding failed for listen port 0\n" }},
	{"unconfirmed", func(int) string { return "" }},
}

// AssertForwardRenderingsFit fails t unless forward.Render returns a preamble
// that fits the reserved length for every outcome class. forward.Resolved is
// not called and forward.HostPort must be a valid port.
func AssertForwardRenderingsFit(t *testing.T, forward *sshutil.ReverseForward) {
	t.Helper()
	assertForwardRenderingsFit(t, forward)
}

func assertForwardRenderingsFit(t mcpInjectionReporter, forward *sshutil.ReverseForward) {
	t.Helper()

	for _, class := range forwardOutcomeStderr {
		spec := *forward
		spec.Resolved = nil
		launch := sshutil.BuildSSHLaunch("host", "/ws", "agent", nil, sshutil.SSHOptions{Forward: &spec})

		if _, err := io.Copy(io.Discard, launch.WatchStderr(strings.NewReader(class.stderr(spec.HostPort)))); err != nil {
			t.Errorf("reading stderr for %s: %v", class.name, err)
			continue
		}
		if _, err := io.ReadAll(launch.StdinReader()); err != nil {
			t.Errorf("rendering for %s does not fit the widest outcome: %v", class.name, err)
		}
	}
}

// AssertMalformedMCPConfigRejected fails t unless startSession, given the path
// of a malformed configuration, returns a [domain.AgentError] of kind
// [domain.ErrResponseError] whose message starts with "parse MCP config:".
func AssertMalformedMCPConfigRejected(t *testing.T, startSession func(mcpConfigPath string) error) {
	t.Helper()
	assertMalformedMCPConfigRejected(t, t.TempDir(), startSession)
}

func assertMalformedMCPConfigRejected(t mcpInjectionReporter, workspace string, startSession func(mcpConfigPath string) error) {
	t.Helper()

	dir := filepath.Join(workspace, workspacekit.SortieDir)
	path := filepath.Join(dir, "mcp.json")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Errorf("MkdirAll(%q): %v", dir, err)
		return
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Errorf("WriteFile(%q): %v", path, err)
		return
	}

	err := startSession(path)

	var agentErr *domain.AgentError
	if !errors.As(err, &agentErr) {
		t.Errorf("StartSession(malformed config) error = %v, want a *domain.AgentError", err)
		return
	}
	if agentErr.Kind != domain.ErrResponseError {
		t.Errorf("StartSession(malformed config) Kind = %q, want %q", agentErr.Kind, domain.ErrResponseError)
	}
	if !strings.HasPrefix(agentErr.Message, "parse MCP config:") {
		t.Errorf("StartSession(malformed config) Message = %q, want prefix %q", agentErr.Message, "parse MCP config:")
	}
}

// carriesMCPConfigPath reports whether one launch-surface element hands
// path to the agent process. The path must be the whole element, or its
// tail behind one composition marker, so an element naming a neighbouring
// file such as <path>.tmp is not read as a delivery.
func carriesMCPConfigPath(elem, path string) bool {
	if elem == path {
		return true
	}
	prefix, ok := strings.CutSuffix(elem, path)
	if !ok || prefix == "" {
		return false
	}
	switch prefix[len(prefix)-1] {
	case '@', '=':
		return true
	default:
		return false
	}
}

// carriesMCPConfigPathOnWire reports whether one wire element hands
// path to the agent process inside a JSON message body. Two renderings
// of path count: the value verbatim, and the value as encoding/json
// renders it inside a JSON string with the surrounding quotes removed.
// Either rendering counts only when it ends exactly where the enclosing
// JSON string ends, so a body naming a neighbouring file such as
// <path>.tmp is not read as a delivery.
func carriesMCPConfigPathOnWire(elem, path string) bool {
	if path == "" {
		return false
	}
	return wireRenderingEndsAtStringClose(elem, path) ||
		wireRenderingEndsAtStringClose(elem, jsonStringInterior(path))
}

// jsonStringInterior returns the bytes encoding/json places between the
// quotes of the JSON string it renders for s.
func jsonStringInterior(s string) string {
	// json.Marshal cannot fail for a string, so the error is discarded.
	encoded, _ := json.Marshal(s)
	return strings.TrimSuffix(strings.TrimPrefix(string(encoded), `"`), `"`)
}

// wireRenderingEndsAtStringClose reports whether elem carries rendering
// at a position immediately followed by the closing quote of a JSON
// string.
func wireRenderingEndsAtStringClose(elem, rendering string) bool {
	if rendering == "" {
		return false
	}
	for start := 0; ; {
		idx := strings.Index(elem[start:], rendering)
		if idx == -1 {
			return false
		}
		idx += start
		end := idx + len(rendering)
		if end < len(elem) && elem[end] == '"' {
			return true
		}
		start = idx + 1
	}
}
