package mcpconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/workspacekit"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), workspacekit.SortieDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return path
}

func mustParseError(t *testing.T, path string) *Error {
	t.Helper()

	servers, err := Parse(path)
	if err == nil {
		t.Fatalf("Parse(%q) = %v, nil, want an error", path, servers)
	}
	var parseErr *Error
	if !errors.As(err, &parseErr) {
		t.Fatalf("Parse(%q) error type = %T, want *mcpconfig.Error", path, err)
	}
	return parseErr
}

func TestParse_StdioEntry(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `{"mcpServers":{"sortie-tools":{"type":"stdio","command":"/usr/local/bin/sortie","args":["mcp-server"],"env":{"SORTIE_ISSUE_ID":"abc-123"}}}}`)

	servers, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", path, err)
	}
	if len(servers) != 1 {
		t.Fatalf("Parse(%q) returned %d servers, want 1", path, len(servers))
	}

	got := servers[0]
	if got.Name != "sortie-tools" {
		t.Errorf("Server.Name = %q, want %q", got.Name, "sortie-tools")
	}
	if got.Transport != TransportStdio {
		t.Errorf("Server.Transport = %q, want %q", got.Transport, TransportStdio)
	}
	if got.Command != "/usr/local/bin/sortie" {
		t.Errorf("Server.Command = %q, want %q", got.Command, "/usr/local/bin/sortie")
	}
	if len(got.Args) != 1 || got.Args[0] != "mcp-server" {
		t.Errorf("Server.Args = %v, want [mcp-server]", got.Args)
	}
	if got.Env["SORTIE_ISSUE_ID"] != "abc-123" {
		t.Errorf("Server.Env[%q] = %q, want %q", "SORTIE_ISSUE_ID", got.Env["SORTIE_ISSUE_ID"], "abc-123")
	}
	if got.Enabled != nil {
		t.Errorf("Server.Enabled = %v, want nil", *got.Enabled)
	}
}

func TestParse_HTTPEntry(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `{"mcpServers":{"remote-tools":{"type":"http","url":"https://example.invalid/mcp","headers":{"Authorization":"Bearer token"}}}}`)

	servers, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", path, err)
	}
	if len(servers) != 1 {
		t.Fatalf("Parse(%q) returned %d servers, want 1", path, len(servers))
	}

	got := servers[0]
	if got.Name != "remote-tools" {
		t.Errorf("Server.Name = %q, want %q", got.Name, "remote-tools")
	}
	if got.Transport != TransportHTTP {
		t.Errorf("Server.Transport = %q, want %q", got.Transport, TransportHTTP)
	}
	if got.URL != "https://example.invalid/mcp" {
		t.Errorf("Server.URL = %q, want %q", got.URL, "https://example.invalid/mcp")
	}
	if got.Headers["Authorization"] != "Bearer token" {
		t.Errorf("Server.Headers[%q] = %q, want %q", "Authorization", got.Headers["Authorization"], "Bearer token")
	}
}

func TestParse_AbsentMCPServersKey(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `{"otherKey":"value"}`)

	servers, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v, want nil", path, err)
	}
	if servers != nil {
		t.Errorf("Parse(%q) = %v, want nil servers", path, servers)
	}
}

func TestParse_MalformedDocument(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `{not valid json`)

	parseErr := mustParseError(t, path)
	if parseErr.Kind != ErrorNotJSON {
		t.Errorf("Error.Kind = %q, want %q", parseErr.Kind, ErrorNotJSON)
	}
}

func TestParse_NullIsNotAnObject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "null document", content: `null`},
		{name: "null mcpServers", content: `{"mcpServers":null}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := writeConfig(t, tt.content)
			parseErr := mustParseError(t, path)
			if parseErr.Kind != ErrorNotJSON {
				t.Errorf("Error.Kind = %q, want %q", parseErr.Kind, ErrorNotJSON)
			}
		})
	}
}

func TestParse_EntryNotExpressible(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "neither command nor url",
			content: `{"mcpServers":{"broken":{"type":"stdio"}}}`,
		},
		{
			name:    "both command and url",
			content: `{"mcpServers":{"broken":{"command":"/bin/echo","url":"https://example.invalid"}}}`,
		},
		{
			name:    "stdio type carrying a url",
			content: `{"mcpServers":{"broken":{"type":"stdio","url":"https://example.invalid/mcp"}}}`,
		},
		{
			name:    "http type carrying a command",
			content: `{"mcpServers":{"broken":{"type":"http","command":"/bin/echo"}}}`,
		},
		{
			name:    "null command",
			content: `{"mcpServers":{"broken":{"command":null}}}`,
		},
		{
			name:    "null url",
			content: `{"mcpServers":{"broken":{"url":null}}}`,
		},
		{
			name:    "null enabled beside a command",
			content: `{"mcpServers":{"broken":{"command":"/bin/echo","enabled":null}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := writeConfig(t, tt.content)
			parseErr := mustParseError(t, path)
			if parseErr.Kind != ErrorEntryNotExpressible {
				t.Errorf("Error.Kind = %q, want %q", parseErr.Kind, ErrorEntryNotExpressible)
			}
			if parseErr.Server != "broken" {
				t.Errorf("Error.Server = %q, want %q", parseErr.Server, "broken")
			}
		})
	}
}

func TestParse_EnabledFalseRoundTrips(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `{"mcpServers":{"sortie-tools":{"command":"/bin/echo","enabled":false}}}`)

	servers, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", path, err)
	}
	if len(servers) != 1 {
		t.Fatalf("Parse(%q) returned %d servers, want 1", path, len(servers))
	}
	got := servers[0].Enabled
	if got == nil {
		t.Fatal("Server.Enabled = nil, want a pointer to false")
	}
	if *got != false {
		t.Errorf("Server.Enabled = %v, want false", *got)
	}
}

func TestParse_SortedByName(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `{"mcpServers":{"zeta":{"command":"/bin/z"},"alpha":{"command":"/bin/a"}}}`)

	servers, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", path, err)
	}
	if len(servers) != 2 {
		t.Fatalf("Parse(%q) returned %d servers, want 2", path, len(servers))
	}
	if servers[0].Name != "alpha" || servers[1].Name != "zeta" {
		t.Errorf("Parse(%q) order = [%q, %q], want [alpha, zeta]", path, servers[0].Name, servers[1].Name)
	}
}

func TestParse_UnreadablePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{"empty path", ""},
		{"missing file", filepath.Join(t.TempDir(), workspacekit.SortieDir, "does-not-exist.json")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parseErr := mustParseError(t, tt.path)
			if parseErr.Kind != ErrorUnreadable {
				t.Errorf("Error.Kind = %q, want %q", parseErr.Kind, ErrorUnreadable)
			}
		})
	}
}

func TestParse_PathOutsideSortieDirIsRejected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}

	parseErr := mustParseError(t, path)
	if parseErr.Kind != ErrorUnreadable {
		t.Errorf("Error.Kind = %q, want %q", parseErr.Kind, ErrorUnreadable)
	}
}

func TestParse_LinkedWorkspaceDirYieldsErrLink(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, workspacekit.SortieDir), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	mcpPath := filepath.Join(target, workspacekit.SortieDir, "mcp.json")
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires elevated privileges on Windows")
		}
		t.Fatalf("Symlink: %v", err)
	}

	path := filepath.Join(link, workspacekit.SortieDir, "mcp.json")
	parseErr := mustParseError(t, path)
	if parseErr.Kind != ErrorUnreadable {
		t.Errorf("Error.Kind = %q, want %q", parseErr.Kind, ErrorUnreadable)
	}
	if !errors.Is(parseErr, workspacekit.ErrLink) {
		t.Errorf("Error = %v, want wrapping workspacekit.ErrLink", parseErr)
	}
}

const (
	seededBearer = "seeded-bearer-secret"
	seededHeader = "seeded-header-secret"
	seededEnv    = "seeded-env-secret"

	extHTTPConfig = `{"mcpServers":{"ext":{"type":"http","url":"https://x/mcp","headers":{"B":"` + seededHeader + `"}}}}`
)

var toolEndpoint = &Endpoint{URL: "http://127.0.0.1:1/mcp", Bearer: seededBearer}

func mustLoad(t *testing.T, content string) Config {
	t.Helper()

	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func mustRender(t *testing.T, cfg Config, d registry.MCPDialect, in RenderInput) Delivery {
	t.Helper()

	got, err := cfg.Render(d, in)
	if err != nil {
		t.Fatalf("Render(%q) error = %v", d, err)
	}
	return got
}

func assertJSONEqual(t *testing.T, what string, got []byte, want string) {
	t.Helper()

	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s = %s, not JSON: %v", what, got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want for %s is not JSON: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

func TestConfig_Render_DialectForms(t *testing.T) {
	t.Parallel()

	const (
		jsonHTTPTools = `"sortie-tools":{"type":"http","url":"http://127.0.0.1:1/mcp","headers":{"Authorization":"Bearer ${SORTIE_TOOL_SERVER_BEARER}"}}`
		jsonExt       = `"ext":{"type":"http","url":"https://x/mcp","headers":{"B":"${SORTIE_MCP_HEADER_1}"}}`
		tomlTools     = `mcp_servers.sortie-tools={url="http://127.0.0.1:1/mcp", bearer_token_env_var="SORTIE_TOOL_SERVER_BEARER", default_tools_approval_mode="approve"}`
		tomlExt       = `mcp_servers.ext={url="https://x/mcp", env_http_headers={"B"="SORTIE_MCP_HEADER_1"}, default_tools_approval_mode="approve"}`
		memberExt     = `"ext":{"type":"remote","url":"https://x/mcp","headers":{"B":"{env:SORTIE_MCP_HEADER_1}"},"enabled":true}`
	)

	tests := []struct {
		name        string
		dialect     registry.MCPDialect
		config      string
		toolServer  *Endpoint
		wantDoc     string
		wantArgs    []string
		wantEntries []string
	}{
		{
			name: "json with tool server", dialect: registry.MCPDialectMCPServersJSON, config: extHTTPConfig, toolServer: toolEndpoint,
			wantDoc: `{"mcpServers":{` + jsonHTTPTools + `,` + jsonExt + `}}`,
		},
		{
			name: "always-load json marks the HTTP tool server", dialect: registry.MCPDialectMCPServersJSONAlwaysLoad, config: extHTTPConfig, toolServer: toolEndpoint,
			wantDoc: `{"mcpServers":{"sortie-tools":{"alwaysLoad":true,"type":"http","url":"http://127.0.0.1:1/mcp","headers":{"Authorization":"Bearer ${SORTIE_TOOL_SERVER_BEARER}"}},` + jsonExt + `}}`,
		},
		{
			name: "toml with tool server", dialect: registry.MCPDialectMCPServersTOML, config: extHTTPConfig, toolServer: toolEndpoint,
			wantArgs: []string{"-c", tomlTools, "-c", tomlExt},
		},
		{
			name: "member json with tool server", dialect: registry.MCPDialectMCPMemberJSON, config: extHTTPConfig, toolServer: toolEndpoint,
			wantDoc: `{"sortie-tools":{"type":"remote","url":"http://127.0.0.1:1/mcp","headers":{"Authorization":"Bearer {env:SORTIE_TOOL_SERVER_BEARER}"},"enabled":true},` + memberExt + `}`,
		},
		{
			name: "acp with tool server", dialect: registry.MCPDialectACPSession, config: extHTTPConfig, toolServer: toolEndpoint,
			wantEntries: []string{
				`{"type":"http","name":"sortie-tools","url":"http://127.0.0.1:1/mcp","headers":[{"name":"Authorization","value":"Bearer ` + seededBearer + `"}]}`,
				`{"type":"http","name":"ext","url":"https://x/mcp","headers":[{"name":"B","value":"` + seededHeader + `"}]}`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := mustRender(t, mustLoad(t, tt.config), tt.dialect, RenderInput{ToolServer: tt.toolServer})

			if tt.wantDoc != "" {
				assertJSONEqual(t, "Delivery.Document", got.Document, tt.wantDoc)
			} else if got.Document != nil {
				t.Errorf("Delivery.Document = %s, want nil", got.Document)
			}
			if !slices.Equal(got.Args, tt.wantArgs) {
				t.Errorf("Delivery.Args = %q, want %q", got.Args, tt.wantArgs)
			}
			if len(got.Entries) != len(tt.wantEntries) {
				t.Fatalf("Delivery.Entries = %s, want %d entries", got.Entries, len(tt.wantEntries))
			}
			for i, want := range tt.wantEntries {
				assertJSONEqual(t, fmt.Sprintf("Delivery.Entries[%d]", i), got.Entries[i], want)
			}
		})
	}
}

func TestConfig_Render_TOMLRefusesWhatItCannotExpress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		config       string
		wantKeyError string
	}{
		{"unmodeled key", `{"mcpServers":{"ext":{"type":"http","url":"https://x/mcp","timeout":5000}}}`, "timeout"},
		{"name that is not a bare key", `{"mcpServers":{"a.b":{"command":"/bin/x"}}}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := mustLoad(t, tt.config).Render(registry.MCPDialectMCPServersTOML, RenderInput{})

			var renderErr *Error
			switch {
			case err == nil || errors.Is(err, ErrUnknownDialect):
				t.Fatalf("Render(toml) error = %v, want a render error", err)
			case tt.wantKeyError == "":
			case !errors.As(err, &renderErr) || renderErr.Kind != ErrorUnmodeledKey || renderErr.Key != tt.wantKeyError:
				t.Errorf("Render(toml) error = %v, want Kind %q Key %q", err, ErrorUnmodeledKey, tt.wantKeyError)
			}
		})
	}
}

func TestConfig_Render_TOMLEnvValuesStayOffArgsUnlessCollided(t *testing.T) {
	t.Parallel()

	cfg := mustLoad(t, `{"mcpServers":{
"sortie-tools":{"command":"/bin/sortie","env":{"CLASH":"seeded-env-a","OPS":"`+seededEnv+`","HELD":"held-value"}},
"ops":{"command":"/bin/ops","env":{"CLASH":"seeded-env-b"}}}}`)
	var logs bytes.Buffer

	got := mustRender(t, cfg, registry.MCPDialectMCPServersTOML, RenderInput{BaseEnv: []string{"HELD=other-value"}, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})

	if want := []string{"OPS=" + seededEnv}; !slices.Equal(got.Env, want) {
		t.Errorf("Delivery.Env = %q, want %q", got.Env, want)
	}
	args := strings.Join(got.Args, "\n")
	if strings.Contains(args, seededEnv) {
		t.Errorf("Delivery.Args carry the by-name env value %q: %q", seededEnv, got.Args)
	}
	for _, literal := range []string{`"CLASH"="seeded-env-a"`, `"HELD"="held-value"`, `env_vars=["OPS"]`} {
		if !strings.Contains(args, literal) {
			t.Errorf("Delivery.Args = %q, want %s", got.Args, literal)
		}
	}
	if !strings.Contains(logs.String(), "CLASH") {
		t.Errorf("Render log output %q does not name the collided variable CLASH, want a warning", logs.String())
	}
	for _, value := range []string{"seeded-env-a", "seeded-env-b", "held-value", "other-value", seededEnv} {
		if strings.Contains(logs.String(), value) {
			t.Errorf("Render log output %q holds the value %q, want server and variable only", logs.String(), value)
		}
	}
}

func TestConfig_Render_SecretsTakeOnlyTheirStatedRoutes(t *testing.T) {
	t.Parallel()

	cfg := mustLoad(t, `{"mcpServers":{
"ops":{"command":"/bin/ops","env":{"OPS":"`+seededEnv+`"}},
"ext":{"type":"http","url":"https://x/mcp","headers":{"B":"`+seededHeader+`"}}}}`)

	tests := []struct {
		name          string
		dialect       registry.MCPDialect
		wantBearer    bool
		wantHeader    bool
		wantEnvInWire bool
	}{
		{"json", registry.MCPDialectMCPServersJSON, false, false, false},
		{"always-load json", registry.MCPDialectMCPServersJSONAlwaysLoad, false, false, false},
		{"toml", registry.MCPDialectMCPServersTOML, false, false, false},
		{"member json", registry.MCPDialectMCPMemberJSON, false, false, false},
		{"acp", registry.MCPDialectACPSession, true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := mustRender(t, cfg, tt.dialect, RenderInput{ToolServer: toolEndpoint})

			args := strings.Join(got.Args, "\n")
			entries := fmt.Sprintf("%s", got.Entries)
			carried := string(got.Document) + args + entries
			if has := strings.Contains(carried, seededBearer); has != tt.wantBearer {
				t.Errorf("Render(%q) carries the bearer = %t, want %t", tt.dialect, has, tt.wantBearer)
			}
			if has := strings.Contains(carried, seededHeader); has != tt.wantHeader {
				t.Errorf("Render(%q) carries the header value = %t, want %t", tt.dialect, has, tt.wantHeader)
			}
			if strings.Contains(args, seededEnv) {
				t.Errorf("Render(%q) Args carry the stdio env value: %q", tt.dialect, got.Args)
			}
			if has := strings.Contains(entries, seededEnv); has != tt.wantEnvInWire {
				t.Errorf("Render(%q) Entries carry the stdio env value = %t, want %t", tt.dialect, has, tt.wantEnvInWire)
			}
		})
	}
}
