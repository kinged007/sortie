package mcpconfig

import (
	"encoding/json"
	"fmt"
)

// acpDialect renders one Agent Client Protocol McpServer object per server.
// The protocol has no reference form for a header value, so every header
// travels literally inside the session-creation request.
type acpDialect struct{}

type acpNameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type acpHTTPServer struct {
	Type    string         `json:"type"`
	Name    string         `json:"name"`
	URL     string         `json:"url"`
	Headers []acpNameValue `json:"headers"`
}

type acpStdioServer struct {
	Name    string         `json:"name"`
	Command string         `json:"command"`
	Args    []string       `json:"args"`
	Env     []acpNameValue `json:"env"`
}

func (acpDialect) headerRef(string) string { return "" }

func (acpDialect) format(entries []entry) (Delivery, error) {
	if err := rejectUnmodeled(entries); err != nil {
		return Delivery{}, err
	}
	var delivery Delivery
	for _, e := range entries {
		var server any
		switch e.Transport {
		case TransportStdio:
			stdio := acpStdioServer{Name: e.Name, Command: e.Command, Args: e.Args, Env: []acpNameValue{}}
			if stdio.Args == nil {
				stdio.Args = []string{}
			}
			for _, kv := range e.Env {
				name, value := splitEnv(kv)
				stdio.Env = append(stdio.Env, acpNameValue{Name: name, Value: value})
			}
			server = stdio
		case TransportHTTP:
			remote := acpHTTPServer{Type: "http", Name: e.Name, URL: e.URL, Headers: []acpNameValue{}}
			for _, header := range e.Headers {
				remote.Headers = append(remote.Headers, acpNameValue{Name: header.Name, Value: header.Value})
			}
			server = remote
		default:
			return Delivery{}, fmt.Errorf("server %q: entry carries neither command nor url", e.Name)
		}
		encoded, err := json.Marshal(server)
		if err != nil {
			return Delivery{}, fmt.Errorf("marshal server %q: %w", e.Name, err)
		}
		delivery.Entries = append(delivery.Entries, encoded)
	}
	return delivery, nil
}
