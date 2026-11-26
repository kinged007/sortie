package mcpconfig

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

// tomlDialect renders one "-c mcp_servers.<name>=<inline table>" argument
// pair per server, so an operator's own [mcp_servers] entries merge rather
// than being replaced.
type tomlDialect struct{}

func (tomlDialect) headerRef(env string) string { return env }

func (tomlDialect) format(entries []entry) (Delivery, error) {
	if err := rejectUnmodeled(entries); err != nil {
		return Delivery{}, err
	}
	var delivery Delivery
	delivered := make(map[string]string)
	for _, e := range entries {
		// The runtime splits the dotted path itself before any TOML parser
		// sees it, so a quoted segment is rejected outright.
		if !isTOMLBareKeySegment(e.Name) {
			return Delivery{}, fmt.Errorf("server %q: name is not a TOML bare key", e.Name)
		}
		table, err := tomlTable(e)
		if err != nil {
			return Delivery{}, fmt.Errorf("server %q: %w", e.Name, err)
		}
		delivery.Args = append(delivery.Args, "-c", "mcp_servers."+e.Name+"="+table)

		for _, kv := range e.Env {
			if name, _ := splitEnv(kv); slices.Contains(e.EnvByName, name) {
				delivered[name] = kv
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(delivered)) {
		delivery.Env = append(delivery.Env, delivered[name])
	}
	return delivery, nil
}

// tomlTable quotes keys that carry an operator-controlled name (an env
// variable or a header), the only form safe outside the bare-key set.
func tomlTable(e entry) (string, error) {
	var fields []string

	switch e.Transport {
	case TransportStdio:
		command, err := tomlString(e.Command)
		if err != nil {
			return "", fmt.Errorf("command: %w", err)
		}
		fields = append(fields, "command="+command)

		if len(e.Args) > 0 {
			args, err := tomlStrings(e.Args)
			if err != nil {
				return "", fmt.Errorf("args: %w", err)
			}
			fields = append(fields, "args=["+args+"]")
		}

		var literal, byName []string
		for _, kv := range e.Env {
			name, value := splitEnv(kv)
			if slices.Contains(e.EnvByName, name) {
				byName = append(byName, name)
				continue
			}
			key, err := tomlString(name)
			if err != nil {
				return "", fmt.Errorf("env: %w", err)
			}
			quoted, err := tomlString(value)
			if err != nil {
				return "", fmt.Errorf("env: %w", err)
			}
			literal = append(literal, key+"="+quoted)
		}
		if len(literal) > 0 {
			fields = append(fields, "env={"+strings.Join(literal, ", ")+"}")
		}
		if len(byName) > 0 {
			names, err := tomlStrings(byName)
			if err != nil {
				return "", fmt.Errorf("env_vars: %w", err)
			}
			fields = append(fields, "env_vars=["+names+"]")
		}

	case TransportHTTP:
		url, err := tomlString(e.URL)
		if err != nil {
			return "", fmt.Errorf("url: %w", err)
		}
		fields = append(fields, "url="+url)

		var bearer string
		var headers []string
		for _, header := range e.Headers {
			variable, err := tomlString(header.Env)
			if err != nil {
				return "", fmt.Errorf("headers: %w", err)
			}
			if header.Env == BearerEnv {
				bearer = variable
				continue
			}
			key, err := tomlString(header.Name)
			if err != nil {
				return "", fmt.Errorf("headers: %w", err)
			}
			headers = append(headers, key+"="+variable)
		}
		if bearer != "" {
			fields = append(fields, "bearer_token_env_var="+bearer)
		}
		if len(headers) > 0 {
			fields = append(fields, "env_http_headers={"+strings.Join(headers, ", ")+"}")
		}

	default:
		return "", fmt.Errorf("entry carries neither command nor url")
	}

	// The runtime asks for approval before calling a tool that declares no
	// annotations, and the thread's never-ask approval policy turns that ask
	// into a refusal, so without this no tool on the server could ever run.
	fields = append(fields, `default_tools_approval_mode="approve"`)

	return "{" + strings.Join(fields, ", ") + "}", nil
}

func tomlStrings(values []string) (string, error) {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		v, err := tomlString(value)
		if err != nil {
			return "", err
		}
		quoted = append(quoted, v)
	}
	return strings.Join(quoted, ", "), nil
}

func isTOMLBareKeySegment(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// tomlString fails when s is not valid UTF-8.
func tomlString(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("value is not valid UTF-8")
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}
