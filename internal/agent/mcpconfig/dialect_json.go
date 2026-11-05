package mcpconfig

import (
	"encoding/json"
	"fmt"
)

// jsonDialect renders the {"mcpServers":{...}} document. Each declared
// server keeps every member of its Raw object.
type jsonDialect struct {
	alwaysLoad bool
}

func (jsonDialect) headerRef(env string) string { return "${" + env + "}" }

func (d jsonDialect) format(entries []entry) (Delivery, error) {
	members := make(map[string]map[string]json.RawMessage, len(entries))
	for _, e := range entries {
		member, err := jsonMember(e)
		if err != nil {
			return Delivery{}, fmt.Errorf("server %q: %w", e.Name, err)
		}
		if d.alwaysLoad && e.Name == ToolServerName {
			member["alwaysLoad"] = json.RawMessage("true")
		}
		members[e.Name] = member
	}
	doc, err := json.Marshal(map[string]any{"mcpServers": members})
	if err != nil {
		return Delivery{}, fmt.Errorf("marshal mcpServers document: %w", err)
	}
	return Delivery{Document: doc}, nil
}

func jsonMember(e entry) (map[string]json.RawMessage, error) {
	if len(e.Raw) > 0 {
		var member map[string]json.RawMessage
		if err := json.Unmarshal(e.Raw, &member); err != nil {
			return nil, err
		}
		return member, nil
	}
	headers := make(map[string]string, len(e.Headers))
	for _, header := range e.Headers {
		headers[header.Name] = header.Ref
	}
	built := map[string]any{"type": string(e.Transport), "url": e.URL, "headers": headers}
	member := make(map[string]json.RawMessage, len(built)+1)
	for key, value := range built {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		member[key] = encoded
	}
	return member, nil
}
