// Package mcpconfig parses the worker-generated MCP configuration file
// into a runtime-neutral server model and renders it into the syntax each
// agent runtime parses. [Load] reads the file into a [Config];
// [Config.Render] formats it through the dialect a kind declares in its
// registry metadata and returns the [Delivery] the kind places.
package mcpconfig

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/sortie-ai/sortie/internal/workspacekit"
)

const (
	// ToolServerName is the generated entry that declares Sortie's own tool
	// server.
	ToolServerName = "sortie-tools"

	// HeaderEnvPrefix prefixes the variables that carry operator HTTP header
	// values by reference.
	HeaderEnvPrefix = "SORTIE_MCP_HEADER_"

	// BearerEnv is the variable that carries the bearer of Sortie's HTTP tool
	// server by reference.
	BearerEnv = "SORTIE_TOOL_SERVER_BEARER" //nolint:gosec // G101: an environment variable name, not a credential
)

// Transport identifies how a server communicates with the agent
// runtime.
type Transport string

const (
	// TransportStdio identifies a server launched as a child process
	// communicating over stdio.
	TransportStdio Transport = "stdio"

	// TransportHTTP identifies a server reached over streamable HTTP.
	TransportHTTP Transport = "http"
)

// Server is one entry of the generated configuration's mcpServers
// object, normalized so an adapter can render it into the
// configuration form its runtime parses.
type Server struct {
	// Name is the object key the entry was declared under, e.g.
	// "sortie-tools".
	Name string

	// Transport reports whether Command or URL carries the server's
	// launch target.
	Transport Transport

	// Command is the server's executable path. Set only when
	// Transport is TransportStdio.
	Command string

	// Args are the command's arguments. Set only when Transport is
	// TransportStdio.
	Args []string

	// Env holds the server's environment variables. Set only when
	// Transport is TransportStdio.
	Env map[string]string

	// URL is the server's endpoint. Set only when Transport is
	// TransportHTTP.
	URL string

	// Headers holds request headers sent to the server. Set only when
	// Transport is TransportHTTP.
	Headers map[string]string

	// Enabled is the operator's enable flag. Nil when the entry omits
	// it.
	Enabled *bool

	// Raw is the entry's JSON object as declared.
	Raw json.RawMessage

	// Unmodeled lists, sorted, the entry's keys outside the modeled members.
	// Only the mcp_servers_json dialects can carry them to a runtime.
	Unmodeled []string
}

// HeaderRef carries one HTTP header value by reference, so a rendered
// declaration names Env instead of holding Value.
type HeaderRef struct {
	Server string
	Header string

	// Env is the variable that carries Value.
	Env   string
	Value string
}

// Declarable returns servers without the disabled ones, keeping order.
func Declarable(servers []Server) []Server {
	return slices.DeleteFunc(slices.Clone(servers), func(s Server) bool {
		return s.Enabled != nil && !*s.Enabled
	})
}

// HeaderRefs returns one reference per HTTP header, numbered in (server,
// header) byte order whatever the order of servers.
func HeaderRefs(servers []Server) []HeaderRef {
	var refs []HeaderRef
	for _, server := range servers {
		if server.Transport != TransportHTTP {
			continue
		}
		for header, value := range server.Headers {
			refs = append(refs, HeaderRef{Server: server.Name, Header: header, Value: value})
		}
	}
	slices.SortFunc(refs, func(a, b HeaderRef) int {
		return cmp.Or(cmp.Compare(a.Server, b.Server), cmp.Compare(a.Header, b.Header))
	})
	for i := range refs {
		refs[i].Env = HeaderEnvPrefix + strconv.Itoa(i+1)
	}
	return refs
}

type StdioLaunch struct {
	Command string
	Args    []string
	Env     map[string]string
}

// Endpoint is Sortie's HTTP tool server as a runtime reaches it.
type Endpoint struct {
	URL    string
	Bearer string
}

// Config is one session's declarable servers with their header references. It
// is immutable; derivation methods return new values.
type Config struct {
	servers []Server
	refs    []HeaderRef
}

// Load reads the generated configuration at path, dropping disabled servers.
func Load(path string) (Config, error) {
	servers, err := Parse(path)
	if err != nil {
		return Config{}, err
	}
	return newConfig(Declarable(servers)), nil
}

func newConfig(servers []Server) Config {
	return Config{servers: servers, refs: HeaderRefs(servers)}
}

// ToolServer returns the stdio [ToolServerName] entry, or false when absent.
func (c Config) ToolServer() (StdioLaunch, bool) {
	for _, server := range c.servers {
		if isToolServer(server) {
			return StdioLaunch{Command: server.Command, Args: server.Args, Env: server.Env}, true
		}
	}
	return StdioLaunch{}, false
}

// WithoutToolServer returns c without its stdio [ToolServerName] entry.
func (c Config) WithoutToolServer() Config {
	return newConfig(slices.DeleteFunc(slices.Clone(c.servers), isToolServer))
}

// WithoutTransport returns c without every server of transport t and the
// names of the removed servers other than [ToolServerName].
func (c Config) WithoutTransport(t Transport) (Config, []string) {
	var removed []string
	kept := slices.DeleteFunc(slices.Clone(c.servers), func(s Server) bool {
		if s.Transport != t {
			return false
		}
		if s.Name != ToolServerName {
			removed = append(removed, s.Name)
		}
		return true
	})
	return newConfig(kept), removed
}

// HeaderEnv returns one "<Env>=<Value>" entry per header reference.
func (c Config) HeaderEnv() []string {
	env := make([]string, 0, len(c.refs))
	for _, ref := range c.refs {
		env = append(env, ref.Env+"="+ref.Value)
	}
	return env
}

func isToolServer(s Server) bool {
	return s.Name == ToolServerName && s.Transport == TransportStdio
}

// ErrorKind classifies why [Parse] failed.
type ErrorKind string

const (
	// ErrorUnreadable reports that the file at the failing [Error]'s
	// Path could not be read.
	ErrorUnreadable ErrorKind = "unreadable"

	// ErrorNotJSON reports that the file's content is not a JSON
	// object, that its mcpServers member is present but not an
	// object, or that an entry within it is not an object.
	ErrorNotJSON ErrorKind = "not_json"

	// ErrorEntryNotExpressible reports that an entry carries neither
	// "command" nor "url", or carries both, so its transport cannot be
	// inferred.
	ErrorEntryNotExpressible ErrorKind = "entry_not_expressible"

	// ErrorUnmodeledKey reports a dialect that cannot carry a key this
	// package does not model.
	ErrorUnmodeledKey ErrorKind = "unmodeled_key"
)

// modeledKeys are the keys [Parse] reads into [Server] fields; other keys
// stay in [Server.Raw] so runtimes that read the JSON themselves still see
// them. Measure a candidate key against every translating runtime before
// adding it.
var modeledKeys = []string{"type", "command", "args", "env", "url", "headers", "enabled"}

// Error reports why [Parse] or [Config.Render] failed; Path is empty for a
// render failure.
type Error struct {
	Kind   ErrorKind
	Path   string
	Server string
	Key    string
	Err    error
}

// Error implements the error interface.
func (e *Error) Error() string {
	prefix := "mcpconfig"
	if e.Path != "" {
		prefix += " " + e.Path
	}
	switch {
	case e.Server != "" && e.Key != "":
		return fmt.Sprintf("%s: server %q: key %q: %s", prefix, e.Server, e.Key, e.detail())
	case e.Server != "":
		return fmt.Sprintf("%s: server %q: %s", prefix, e.Server, e.detail())
	default:
		return fmt.Sprintf("%s: %s", prefix, e.detail())
	}
}

func (e *Error) detail() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	switch e.Kind {
	case ErrorEntryNotExpressible:
		return "entry carries neither command nor url, or carries both"
	case ErrorUnmodeledKey:
		return "the runtime's MCP configuration syntax cannot carry a key this entry declares"
	default:
		return string(e.Kind)
	}
}

// Unwrap returns the underlying error, when Parse wrapped one.
func (e *Error) Unwrap() error { return e.Err }

// Parse reads the worker-generated MCP configuration at path and
// returns its servers in stable order by name.
//
// path must be non-empty and readable. The document must be a JSON
// object; mcpServers must be absent or an object; each entry must be
// an object. An unmodeled entry key is kept in [Server.Raw] rather than
// rejected. Parse performs one file read and no writes, network calls, or
// environment reads.
func Parse(path string) ([]Server, error) {
	if path == "" {
		return nil, &Error{Kind: ErrorUnreadable, Path: path, Err: fmt.Errorf("path is empty")}
	}
	if filepath.Base(filepath.Dir(path)) != workspacekit.SortieDir {
		return nil, &Error{Kind: ErrorUnreadable, Path: path, Err: fmt.Errorf("path is not inside a %s directory", workspacekit.SortieDir)}
	}

	f, err := workspacekit.OpenSortieFile(filepath.Dir(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		return nil, &Error{Kind: ErrorUnreadable, Path: path, Err: err}
	}
	defer f.Close() //nolint:errcheck // read-only file; close error is not actionable after data is read

	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, &Error{Kind: ErrorUnreadable, Path: path, Err: err}
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, &Error{Kind: ErrorNotJSON, Path: path, Err: err}
	}
	// A JSON null unmarshals into a nil map without an error, so the
	// null document and the null member both have to be rejected
	// explicitly. Reading either as "no servers" would launch a
	// translating adapter with none of the tools its declaration
	// promises.
	if doc == nil {
		return nil, &Error{Kind: ErrorNotJSON, Path: path, Err: fmt.Errorf("document is null, want an object")}
	}

	serversRaw, ok := doc["mcpServers"]
	if !ok {
		return nil, nil
	}

	var entries map[string]json.RawMessage
	if err := json.Unmarshal(serversRaw, &entries); err != nil {
		return nil, &Error{Kind: ErrorNotJSON, Path: path, Err: fmt.Errorf("mcpServers is not an object: %w", err)}
	}
	if entries == nil {
		return nil, &Error{Kind: ErrorNotJSON, Path: path, Err: fmt.Errorf("mcpServers is null, want an object")}
	}

	names := slices.Sorted(maps.Keys(entries))
	servers := make([]Server, 0, len(names))
	for _, name := range names {
		server, err := parseEntry(path, name, entries[name])
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	return servers, nil
}

// urlTransportTypes are the type names that describe a URL-addressed
// server. The set is deliberately partial: a name outside it, and
// outside the stdio name, is left alone rather than judged against a
// vocabulary this package does not own.
var urlTransportTypes = []string{"http", "sse", "streamable-http"}

// checkTransportAgreement rejects an entry whose declared type
// contradicts the transport its own fields imply. The type is optional
// and its absence is not an error; accepting a present one without
// reading it would let a contradictory entry through under a key the
// parser claims to model.
func checkTransportAgreement(path, name string, fields map[string]json.RawMessage, hasCommand bool) error {
	raw, ok := fields["type"]
	if !ok {
		return nil
	}

	var declared string
	if err := json.Unmarshal(raw, &declared); err != nil {
		return &Error{Kind: ErrorNotJSON, Path: path, Server: name, Key: "type", Err: err}
	}

	contradicts := (declared == "stdio" && !hasCommand) ||
		(slices.Contains(urlTransportTypes, declared) && hasCommand)
	if contradicts {
		return &Error{Kind: ErrorEntryNotExpressible, Path: path, Server: name, Key: "type"}
	}
	return nil
}

// parseEntry normalizes one mcpServers object member into a [Server].
func parseEntry(path, name string, raw json.RawMessage) (Server, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Server{}, &Error{Kind: ErrorNotJSON, Path: path, Server: name, Err: err}
	}

	var unmodeled []string
	for key, raw := range fields {
		if !slices.Contains(modeledKeys, key) {
			unmodeled = append(unmodeled, key)
			continue
		}
		// A null decodes into the zero value of every modeled field's
		// type without an error, so a null command would survive its
		// own presence check and render a server with no command at
		// all. Treat a declared-but-null field as unexpressible rather
		// than as its zero value.
		if string(bytes.TrimSpace(raw)) == "null" {
			return Server{}, &Error{Kind: ErrorEntryNotExpressible, Path: path, Server: name, Key: key}
		}
	}

	_, hasCommand := fields["command"]
	_, hasURL := fields["url"]
	if hasCommand == hasURL {
		return Server{}, &Error{Kind: ErrorEntryNotExpressible, Path: path, Server: name}
	}

	if err := checkTransportAgreement(path, name, fields, hasCommand); err != nil {
		return Server{}, err
	}

	slices.Sort(unmodeled)
	server := Server{Name: name, Raw: slices.Clone(raw), Unmodeled: unmodeled}

	if enabledRaw, ok := fields["enabled"]; ok {
		var enabled bool
		if err := json.Unmarshal(enabledRaw, &enabled); err != nil {
			return Server{}, &Error{Kind: ErrorNotJSON, Path: path, Server: name, Key: "enabled", Err: err}
		}
		server.Enabled = &enabled
	}

	if hasCommand {
		server.Transport = TransportStdio
		if err := json.Unmarshal(fields["command"], &server.Command); err != nil {
			return Server{}, &Error{Kind: ErrorNotJSON, Path: path, Server: name, Key: "command", Err: err}
		}
		if argsRaw, ok := fields["args"]; ok {
			if err := json.Unmarshal(argsRaw, &server.Args); err != nil {
				return Server{}, &Error{Kind: ErrorNotJSON, Path: path, Server: name, Key: "args", Err: err}
			}
		}
		if envRaw, ok := fields["env"]; ok {
			if err := json.Unmarshal(envRaw, &server.Env); err != nil {
				return Server{}, &Error{Kind: ErrorNotJSON, Path: path, Server: name, Key: "env", Err: err}
			}
		}
		return server, nil
	}

	server.Transport = TransportHTTP
	if err := json.Unmarshal(fields["url"], &server.URL); err != nil {
		return Server{}, &Error{Kind: ErrorNotJSON, Path: path, Server: name, Key: "url", Err: err}
	}
	if headersRaw, ok := fields["headers"]; ok {
		if err := json.Unmarshal(headersRaw, &server.Headers); err != nil {
			return Server{}, &Error{Kind: ErrorNotJSON, Path: path, Server: name, Key: "headers", Err: err}
		}
	}
	return server, nil
}
