package mcpconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/sortie-ai/sortie/internal/registry"
)

// ErrUnknownDialect reports that [Config.Render] was asked for a dialect it
// does not render.
var ErrUnknownDialect = errors.New("unknown MCP configuration syntax")

// RenderInput carries what [Config.Render] needs beyond the server model.
type RenderInput struct {
	// ToolServer is Sortie's HTTP tool server. Nil renders no HTTP
	// [ToolServerName] entry.
	ToolServer *Endpoint

	// BaseEnv is the runtime's environment before [Delivery.Env] is added.
	BaseEnv []string

	// Logger receives render warnings. Nil resolves to [slog.Default].
	Logger *slog.Logger
}

// Delivery is one dialect's finished rendering. Members a dialect does not
// produce stay nil.
type Delivery struct {
	// Document is the mcp_servers_json dialects' whole document, or
	// mcp_member_json's "mcp" object.
	Document []byte

	// Args are mcp_servers_toml's "-c" argument pairs.
	Args []string

	// Env holds the stdio env values mcp_servers_toml keeps off the argument
	// list, as "<name>=<value>".
	Env []string

	// Entries holds acp_session's one McpServer object per server.
	Entries []json.RawMessage
}

type entryHeader struct {
	Name  string
	Env   string
	Ref   string // the dialect's reference to Env
	Value string // the literal value, for acp_session only
}

type entry struct {
	Name      string
	URL       string
	Command   string
	Transport Transport
	Raw       json.RawMessage // header values replaced by their Ref
	Unmodeled []string        // declared keys outside the modeled members
	Headers   []entryHeader
	Args      []string
	Env       []string // stdio "<name>=<value>", sorted by name
	EnvByName []string // names of Env the runtime reads from its environment
}

type dialect interface {
	headerRef(env string) string
	format(entries []entry) (Delivery, error)
}

var dialects = map[registry.MCPDialect]dialect{
	registry.MCPDialectMCPServersJSON:           jsonDialect{},
	registry.MCPDialectMCPServersJSONAlwaysLoad: jsonDialect{alwaysLoad: true},
	registry.MCPDialectMCPServersTOML:           tomlDialect{},
	registry.MCPDialectMCPMemberJSON:            memberDialect{},
	registry.MCPDialectACPSession:               acpDialect{},
}

// Render formats the session's servers in dialect d, Sortie's HTTP entry first
// when in.ToolServer is set.
//
// For mcp_servers_toml a stdio env value travels in [Delivery.Env] instead of
// the argument list, unless in.BaseEnv or another server holds the same name
// with a different value; Render then keeps it literal and logs the server and
// variable at warn, never the value.
func (c Config) Render(d registry.MCPDialect, in RenderInput) (Delivery, error) {
	target, ok := dialects[d]
	if !ok {
		return Delivery{}, fmt.Errorf("%w: %q", ErrUnknownDialect, d)
	}
	entries, err := c.walk(d, target, in)
	if err != nil {
		return Delivery{}, err
	}
	return target.format(entries)
}

func (c Config) walk(d registry.MCPDialect, target dialect, in RenderInput) ([]entry, error) {
	logger := in.Logger
	if logger == nil {
		logger = slog.Default()
	}
	var literal map[string]bool
	if d == registry.MCPDialectMCPServersTOML {
		literal = literalEnvNames(c.servers, in.BaseEnv)
	}

	entries := make([]entry, 0, len(c.servers)+1)
	if in.ToolServer != nil {
		entries = append(entries, entry{
			Name:      ToolServerName,
			URL:       in.ToolServer.URL,
			Transport: TransportHTTP,
			Headers: []entryHeader{{
				Name:  "Authorization",
				Env:   BearerEnv,
				Ref:   "Bearer " + target.headerRef(BearerEnv),
				Value: "Bearer " + in.ToolServer.Bearer,
			}},
		})
	}

	for _, server := range c.servers {
		e := entry{
			Name:      server.Name,
			URL:       server.URL,
			Command:   server.Command,
			Transport: server.Transport,
			Args:      server.Args,
			Unmodeled: server.Unmodeled,
		}
		for _, ref := range c.refs {
			if ref.Server == server.Name {
				e.Headers = append(e.Headers, entryHeader{
					Name:  ref.Header,
					Env:   ref.Env,
					Ref:   target.headerRef(ref.Env),
					Value: ref.Value,
				})
			}
		}
		raw, err := withHeaderRefs(server.Raw, e.Headers)
		if err != nil {
			return nil, fmt.Errorf("server %q: %w", server.Name, err)
		}
		e.Raw = raw

		for _, name := range slices.Sorted(maps.Keys(server.Env)) {
			e.Env = append(e.Env, name+"="+server.Env[name])
			if literal == nil {
				continue
			}
			if literal[name] {
				logger.Warn("tool server env value left on the command line",
					slog.String("server", server.Name),
					slog.String("variable", name))
				continue
			}
			e.EnvByName = append(e.EnvByName, name)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// rejectUnmodeled fails rather than dropping a key: the runtime would launch
// without a member the operator declared.
func rejectUnmodeled(entries []entry) error {
	for _, e := range entries {
		if len(e.Unmodeled) > 0 {
			return &Error{Kind: ErrorUnmodeledKey, Server: e.Name, Key: e.Unmodeled[0]}
		}
	}
	return nil
}

func withHeaderRefs(raw json.RawMessage, headers []entryHeader) (json.RawMessage, error) {
	if len(raw) == 0 || len(headers) == 0 {
		return raw, nil
	}
	var member map[string]json.RawMessage
	if err := json.Unmarshal(raw, &member); err != nil {
		return nil, err
	}
	referenced := make(map[string]string, len(headers))
	for _, header := range headers {
		referenced[header.Name] = header.Ref
	}
	encoded, err := json.Marshal(referenced)
	if err != nil {
		return nil, err
	}
	member["headers"] = encoded
	return json.Marshal(member)
}

// literalEnvNames returns the stdio env names that cannot travel through the
// runtime's environment because their value conflicts.
func literalEnvNames(servers []Server, base []string) map[string]bool {
	held := make(map[string]string, len(base))
	for _, kv := range base {
		if name, value, ok := strings.Cut(kv, "="); ok {
			held[name] = value
		}
	}

	literal := make(map[string]bool)
	declared := make(map[string]string)
	for _, server := range servers {
		for name, value := range server.Env {
			if baseValue, ok := held[name]; ok && baseValue != value {
				literal[name] = true
			}
			if first, ok := declared[name]; ok && first != value {
				literal[name] = true
			}
			declared[name] = value
		}
	}
	return literal
}

func splitEnv(kv string) (name, value string) {
	name, value, _ = strings.Cut(kv, "=")
	return name, value
}
