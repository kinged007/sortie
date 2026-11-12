package mcpconfig

import (
	"encoding/json"
	"fmt"
)

// memberDialect renders the object that fills the "mcp" member of a
// runtime's inline JSON configuration.
type memberDialect struct{}

type memberEntry struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     bool              `json:"enabled"`
}

func (memberDialect) headerRef(env string) string { return "{env:" + env + "}" }

func (memberDialect) format(entries []entry) (Delivery, error) {
	if err := rejectUnmodeled(entries); err != nil {
		return Delivery{}, err
	}
	members := make(map[string]memberEntry, len(entries))
	for _, e := range entries {
		switch e.Transport {
		case TransportStdio:
			members[e.Name] = memberEntry{
				Type:        "local",
				Command:     append([]string{e.Command}, e.Args...),
				Environment: stdioEnvMap(e.Env),
				Enabled:     true,
			}
		case TransportHTTP:
			headers := make(map[string]string, len(e.Headers))
			for _, header := range e.Headers {
				headers[header.Name] = header.Ref
			}
			members[e.Name] = memberEntry{
				Type:    "remote",
				URL:     e.URL,
				Headers: headers,
				Enabled: true,
			}
		default:
			return Delivery{}, fmt.Errorf("server %q: entry carries neither command nor url", e.Name)
		}
	}
	doc, err := json.Marshal(members)
	if err != nil {
		return Delivery{}, fmt.Errorf("marshal mcp member: %w", err)
	}
	return Delivery{Document: doc}, nil
}

func stdioEnvMap(env []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	values := make(map[string]string, len(env))
	for _, kv := range env {
		name, value := splitEnv(kv)
		values[name] = value
	}
	return values
}
