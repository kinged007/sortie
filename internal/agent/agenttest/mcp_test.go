package agenttest

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/mcpconfig"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/registry"
)

type mcpFakeReporter struct {
	errors   []string
	messages []string
}

func (f *mcpFakeReporter) Helper() {}

func (f *mcpFakeReporter) Errorf(format string, args ...any) {
	f.errors = append(f.errors, format)
	f.messages = append(f.messages, fmt.Sprintf(format, args...))
}

const (
	derivedConfig        = `{"mcpServers":{"sortie-tools":{"alwaysLoad":true,"command":"/usr/bin/sortie","args":["mcp-server"]},"operator-http":{"type":"http","url":"https://ops.example/mcp","headers":{"Authorization":"${SORTIE_MCP_HEADER_1}"}},"operator-stdio":{"command":"/usr/bin/ops","env":{"OPS_TOKEN":"seeded-env-value"}}}}`
	derivedLiteralHeader = `{"mcpServers":{"sortie-tools":{"command":"/usr/bin/sortie","args":["mcp-server"]},"operator-http":{"type":"http","url":"https://ops.example/mcp","headers":{"Authorization":"Bearer seeded-header-value"}},"operator-stdio":{"command":"/usr/bin/ops","env":{"OPS_TOKEN":"seeded-env-value"}}}}`
	derivedWithDisabled  = `{"mcpServers":{"sortie-tools":{"command":"/usr/bin/sortie","args":["mcp-server"]},"operator-http":{"type":"http","url":"https://ops.example/mcp","headers":{"Authorization":"${SORTIE_MCP_HEADER_1}"}},"operator-stdio":{"command":"/usr/bin/ops","env":{"OPS_TOKEN":"seeded-env-value"}},"disabled-server":{"command":"/usr/bin/off","enabled":false}}}`

	headerCarrier = "SORTIE_MCP_HEADER_1=Bearer seeded-header-value"
	envCarrier    = "OPS_TOKEN=seeded-env-value"
	acpBody       = `{"servers":[{"name":"sortie-tools","command":"/usr/bin/sortie"},{"name":"operator-http","url":"https://ops.example/mcp","headers":[{"name":"Authorization","value":"Bearer seeded-header-value"}]},{"name":"operator-stdio","command":"/usr/bin/ops","env":[{"name":"OPS_TOKEN","value":"seeded-env-value"}]}]}`
)

func codexSurface() MCPLaunchSurface {
	return MCPLaunchSurface{
		Args: []string{
			"-c", `mcp_servers.sortie-tools={command="/usr/bin/sortie", args=["mcp-server"]}`,
			"-c", `mcp_servers.operator-http={url="https://ops.example/mcp", env_http_headers={"Authorization"="SORTIE_MCP_HEADER_1"}}`,
			"-c", `mcp_servers.operator-stdio={command="/usr/bin/ops", env_vars=["OPS_TOKEN"]}`,
		},
		Env: []string{headerCarrier, envCarrier},
	}
}

func TestAssertMCPInjection_ServerRules(t *testing.T) {
	t.Parallel()

	generated := WriteSeededMCPConfig(t, t.TempDir())
	sibling := func(name, content string) string {
		path := filepath.Join(filepath.Dir(generated), name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
		return path
	}
	derived := sibling("derived.json", derivedConfig)
	derivedArgs := func(file string) MCPLaunchSurface {
		return MCPLaunchSurface{Args: []string{"--mcp-config", file}, Env: []string{headerCarrier}, File: file}
	}

	withCodex := func(edit func(*MCPLaunchSurface)) MCPLaunchSurface {
		s := codexSurface()
		edit(&s)
		return s
	}

	tests := []struct {
		name       string
		dialect    registry.MCPDialect
		surface    MCPLaunchSurface
		wantSubstr string
	}{
		{name: "none, neighbouring file only", dialect: registry.MCPDialectNone, surface: MCPLaunchSurface{Args: []string{generated + ".tmp"}}},
		{name: "toml, header and env by reference", dialect: registry.MCPDialectMCPServersTOML, surface: codexSurface()},
		{name: "acp, header literal on the wire", dialect: registry.MCPDialectACPSession, surface: MCPLaunchSurface{Wire: []string{acpBody}}},
		{name: "always-load json, derived file", dialect: registry.MCPDialectMCPServersJSONAlwaysLoad, surface: derivedArgs(derived)},
		{
			name:    "member json, header by reference",
			dialect: registry.MCPDialectMCPMemberJSON,
			surface: MCPLaunchSurface{
				Env: []string{headerCarrier, envCarrier, `OPENCODE_CONFIG_CONTENT={"mcp":{"sortie-tools":{"command":["/usr/bin/sortie","mcp-server"]},"operator-http":{"url":"https://ops.example/mcp","headers":{"Authorization":"{env:SORTIE_MCP_HEADER_1}"}},"operator-stdio":{"command":["/usr/bin/ops"],"environment":{"OPS_TOKEN":"seeded-env-value"}}}}`},
			},
		},
		{
			name:       "none, the surface carries the path",
			dialect:    registry.MCPDialectNone,
			surface:    MCPLaunchSurface{Args: []string{"--mcp-config", generated}},
			wantSubstr: "not to carry mcpConfigPath",
		},
		{
			name:       "json, no derived file",
			dialect:    registry.MCPDialectMCPServersJSON,
			surface:    MCPLaunchSurface{Args: []string{"--mcp-config", generated}},
			wantSubstr: "derived file",
		},
		{
			name:       "toml, one declared server missing",
			dialect:    registry.MCPDialectMCPServersTOML,
			surface:    withCodex(func(s *MCPLaunchSurface) { s.Args = s.Args[:2] }),
			wantSubstr: "not found on the launch surface",
		},
		{
			name:       "acp, stdio command absent from the wire",
			dialect:    registry.MCPDialectACPSession,
			surface:    MCPLaunchSurface{Wire: []string{strings.ReplaceAll(acpBody, "/usr/bin/ops", "/usr/bin/other")}},
			wantSubstr: "not found on the launch surface",
		},
		{
			name:       "always-load json, derived file lacks alwaysLoad",
			dialect:    registry.MCPDialectMCPServersJSONAlwaysLoad,
			surface:    derivedArgs(sibling("noload.json", strings.Replace(derivedConfig, `"alwaysLoad":true,`, "", 1))),
			wantSubstr: "alwaysLoad",
		},
		{
			name:       "toml, header reference not named",
			dialect:    registry.MCPDialectMCPServersTOML,
			surface:    withCodex(func(s *MCPLaunchSurface) { s.Args[3] = `mcp_servers.operator-http={url="https://ops.example/mcp"}` }),
			wantSubstr: "reference",
		},
		{
			name:       "toml, stdio env value missing from the process environment",
			dialect:    registry.MCPDialectMCPServersTOML,
			surface:    withCodex(func(s *MCPLaunchSurface) { s.Env = []string{headerCarrier} }),
			wantSubstr: "not delivered",
		},
		{
			name:       "toml, disabled server named in Args",
			dialect:    registry.MCPDialectMCPServersTOML,
			surface:    withCodex(func(s *MCPLaunchSurface) { s.Args = append(s.Args, "--skip=disabled-server") }),
			wantSubstr: "disabled server",
		},
		{
			name:       "toml, header value in Args",
			dialect:    registry.MCPDialectMCPServersTOML,
			surface:    withCodex(func(s *MCPLaunchSurface) { s.Args = append(s.Args, "Bearer seeded-header-value") }),
			wantSubstr: "outside its delivery channel",
		},
		{
			name:       "toml, header reference variable missing from Env",
			dialect:    registry.MCPDialectMCPServersTOML,
			surface:    withCodex(func(s *MCPLaunchSurface) { s.Env = []string{envCarrier} }),
			wantSubstr: "not delivered through",
		},
		{
			name:       "toml, stdio env value in Args",
			dialect:    registry.MCPDialectMCPServersTOML,
			surface:    withCodex(func(s *MCPLaunchSurface) { s.Args = append(s.Args, `env={OPS_TOKEN="seeded-env-value"}`) }),
			wantSubstr: "found in Args",
		},
		{
			name:       "toml, stdio env value not named in env_vars",
			dialect:    registry.MCPDialectMCPServersTOML,
			surface:    withCodex(func(s *MCPLaunchSurface) { s.Args[5] = `mcp_servers.operator-stdio={command="/usr/bin/ops"}` }),
			wantSubstr: "not named in env_vars",
		},
		{
			name:       "acp, header absent from the wire",
			dialect:    registry.MCPDialectACPSession,
			surface:    MCPLaunchSurface{Wire: []string{strings.ReplaceAll(acpBody, "Bearer seeded-header-value", "${SORTIE_MCP_HEADER_1}")}},
			wantSubstr: "not delivered through",
		},
		{
			name:       "derived file keeps the literal header",
			dialect:    registry.MCPDialectMCPServersJSON,
			surface:    derivedArgs(sibling("literal.json", derivedLiteralHeader)),
			wantSubstr: "outside its delivery channel",
		},
		{
			name:       "derived file declares the disabled server",
			dialect:    registry.MCPDialectMCPServersJSON,
			surface:    derivedArgs(sibling("disabled.json", derivedWithDisabled)),
			wantSubstr: "disabled server",
		},
		{
			name:       "generated file still carried beside the derived one",
			dialect:    registry.MCPDialectMCPServersJSONAlwaysLoad,
			surface:    func() MCPLaunchSurface { s := derivedArgs(derived); s.Args = append(s.Args, generated); return s }(),
			wantSubstr: "not mcpConfigPath",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reporter := &mcpFakeReporter{}
			assertMCPInjection(reporter, tt.dialect, generated, tt.surface)

			if tt.wantSubstr == "" {
				if len(reporter.errors) != 0 {
					t.Errorf("assertMCPInjection(%s) errors = %v, want none", tt.name, reporter.errors)
				}
				return
			}
			if !slices.ContainsFunc(reporter.errors, func(format string) bool { return strings.Contains(format, tt.wantSubstr) }) {
				t.Errorf("assertMCPInjection(%s) errors = %v, want one containing %q", tt.name, reporter.errors, tt.wantSubstr)
			}
		})
	}
}

func TestAssertMalformedMCPConfigRejected(t *testing.T) {
	t.Parallel()

	AssertMalformedMCPConfigRejected(t, func(string) error {
		return &domain.AgentError{Kind: domain.ErrResponseError, Message: "parse MCP config: bad"}
	})

	reporter := &mcpFakeReporter{}
	assertMalformedMCPConfigRejected(reporter, t.TempDir(), func(string) error { return nil })

	if len(reporter.errors) == 0 {
		t.Error("assertMalformedMCPConfigRejected(start succeeds) recorded no failures, want at least one")
	}
}

func TestAssertRemoteMCPSurface(t *testing.T) {
	t.Parallel()

	const bearer = "0123abcd"
	const header = "Bearer seeded-header-value"

	path := WriteSeededMCPConfig(t, t.TempDir())
	servers, err := mcpconfig.Parse(path)
	if err != nil {
		t.Fatalf("mcpconfig.Parse(%q): %v", path, err)
	}
	refs := mcpconfig.HeaderRefs(mcpconfig.Declarable(servers))
	if len(refs) != 1 {
		t.Fatalf("HeaderRefs = %v, want the one seeded header", refs)
	}
	headerEnv := refs[0].Env

	declaration := `{"mcpServers":{"sortie-tools":{"type":"http","url":"http://127.0.0.1:41001/mcp","headers":{"Authorization":"Bearer ${SORTIE_TOOL_SERVER_BEARER}"}}}}`
	cliStdin := fmt.Sprintf("unset _sortie_env && export SORTIE_TOOL_SERVER_BEARER='%s' %s='%s' && set -- '--mcp-config' '%s' && _sortie_complete=1", bearer, headerEnv, header, declaration)
	sessionNew := fmt.Sprintf(`{"method":"session/new","params":{"mcpServers":[{"type":"http","name":"sortie-tools","headers":[{"name":"Authorization","value":"Bearer %s"}]},{"type":"http","name":"operator-http","headers":[{"name":"Authorization","value":"%s"}]}]}}`, bearer, header)

	cli := func() RemoteLaunchSurface {
		return RemoteLaunchSurface{Argv: []string{"-o", "BatchMode=yes", "host", "cd -- '/ws' && claude"}, Stdin: cliStdin}
	}
	acp := func() RemoteLaunchSurface {
		return RemoteLaunchSurface{
			Argv:       []string{"-o", "BatchMode=yes", "host", "cd -- '/ws' && agent --acp"},
			Stdin:      "unset _sortie_env && set -- && _sortie_complete=1" + sessionNew + "\n",
			SessionNew: []string{sessionNew},
		}
	}

	tests := []struct {
		name       string
		surface    RemoteLaunchSurface
		bearer     string
		wantSubstr string
	}{
		{"cli kind", cli(), bearer, ""},
		{"cli kind without a sortie entry", RemoteLaunchSurface{Argv: cli().Argv, Stdin: strings.Replace(cliStdin, "SORTIE_TOOL_SERVER_BEARER='"+bearer+"' ", "", 1)}, "", ""},
		{"acp kind", acp(), bearer, ""},
		{"configuration path in argv", RemoteLaunchSurface{Argv: append(cli().Argv, "@"+path), Stdin: cliStdin}, bearer, "configuration path on ssh argv"},
		{"configuration path on stdin", RemoteLaunchSurface{Argv: cli().Argv, Stdin: cliStdin + " @" + path}, bearer, "configuration path on ssh stdin"},
		{"stdio command on stdin", RemoteLaunchSurface{Argv: cli().Argv, Stdin: cliStdin + " /usr/bin/ops"}, bearer, "stdio server command on ssh stdin"},
		{"stdio server name in session/new", RemoteLaunchSurface{Argv: acp().Argv, Stdin: strings.Replace(acp().Stdin, sessionNew, sessionNew+`"operator-stdio"`, 1), SessionNew: []string{sessionNew + `"operator-stdio"`}}, bearer, "stdio server name on session/new"},
		{"disabled server on stdin", RemoteLaunchSurface{Argv: cli().Argv, Stdin: cliStdin + " disabled-server"}, bearer, "disabled server name on ssh stdin"},
		{"stdio env value in argv", RemoteLaunchSurface{Argv: append(cli().Argv, "OPS_TOKEN=seeded-env-value"), Stdin: cliStdin}, bearer, "stdio env value on ssh argv"},
		{"stdio env value on stdin", RemoteLaunchSurface{Argv: cli().Argv, Stdin: cliStdin + " export OPS_TOKEN='seeded-env-value'"}, bearer, "stdio env value on ssh stdin"},
		{"bearer in argv", RemoteLaunchSurface{Argv: append(cli().Argv, "--bearer", bearer), Stdin: cliStdin}, bearer, "value for SORTIE_TOOL_SERVER_BEARER found in ssh argv"},
		{"header value in argv", RemoteLaunchSurface{Argv: append(cli().Argv, header), Stdin: cliStdin}, bearer, "value for " + headerEnv + " found in ssh argv"},
		{"header value outside its carrier", RemoteLaunchSurface{Argv: cli().Argv, Stdin: cliStdin + " '" + header + "'"}, bearer, "value for " + headerEnv + " found on ssh stdin outside its late carrier"},
		{"bearer in the declaration", RemoteLaunchSurface{Argv: cli().Argv, Stdin: strings.Replace(cliStdin, "${SORTIE_TOOL_SERVER_BEARER}", bearer, 1)}, bearer, "value for SORTIE_TOOL_SERVER_BEARER found on ssh stdin outside its late carrier"},
		{"bearer never carried", RemoteLaunchSurface{Argv: cli().Argv, Stdin: strings.Replace(cliStdin, "SORTIE_TOOL_SERVER_BEARER='"+bearer+"' ", "", 1)}, bearer, "value for SORTIE_TOOL_SERVER_BEARER not delivered as a late carrier"},
		{"acp header value as a late carrier", RemoteLaunchSurface{Argv: acp().Argv, Stdin: "export " + headerEnv + "='" + header + "' && " + acp().Stdin, SessionNew: acp().SessionNew}, bearer, "value for " + headerEnv + " found on ssh stdin outside session/new"},
		{"acp bearer missing from session/new", RemoteLaunchSurface{Argv: acp().Argv, Stdin: acp().Stdin, SessionNew: []string{strings.Replace(sessionNew, "Bearer "+bearer, "Bearer other", 1)}}, bearer, "value for SORTIE_TOOL_SERVER_BEARER not delivered inside session/new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reporter := &mcpFakeReporter{}
			assertRemoteMCPSurface(reporter, path, tt.bearer, tt.surface)

			if tt.wantSubstr == "" {
				if len(reporter.messages) != 0 {
					t.Errorf("assertRemoteMCPSurface(%s) errors = %v, want none", tt.name, reporter.messages)
				}
				return
			}
			if !slices.ContainsFunc(reporter.messages, func(msg string) bool { return strings.Contains(msg, tt.wantSubstr) }) {
				t.Errorf("assertRemoteMCPSurface(%s) errors = %v, want one containing %q", tt.name, reporter.messages, tt.wantSubstr)
			}
		})
	}
}

func TestAssertForwardRenderingsFit(t *testing.T) {
	t.Parallel()

	renderBy := func(extra func(sshutil.ForwardOutcome) []string) func(sshutil.ForwardOutcome) sshutil.LatePreamble {
		return func(outcome sshutil.ForwardOutcome) sshutil.LatePreamble {
			late := sshutil.LatePreamble{Args: []string{"--mcp-config", "{}"}}
			if outcome.Status == sshutil.ForwardAllocated {
				late.Args = append(late.Args, fmt.Sprintf("http://127.0.0.1:%d/mcp", outcome.RemotePort))
			}
			late.Args = append(late.Args, extra(outcome)...)
			return late
		}
	}
	none := func(sshutil.ForwardOutcome) []string { return nil }

	tests := []struct {
		name    string
		render  func(sshutil.ForwardOutcome) sshutil.LatePreamble
		wantErr bool
	}{
		{"every class fits", renderBy(none), false},
		{"refused is wider than allocated", renderBy(func(outcome sshutil.ForwardOutcome) []string {
			if outcome.Status == sshutil.ForwardRefused {
				return []string{"a note that only a refused outcome renders"}
			}
			return nil
		}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reporter := &mcpFakeReporter{}
			assertForwardRenderingsFit(reporter, &sshutil.ReverseForward{HostPort: 41001, Render: tt.render})

			if got := len(reporter.errors) != 0; got != tt.wantErr {
				t.Errorf("assertForwardRenderingsFit(%s) failed = %t (%v), want %t", tt.name, got, reporter.messages, tt.wantErr)
			}
		})
	}
}
