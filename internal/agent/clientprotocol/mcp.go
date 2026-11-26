package clientprotocol

import (
	"encoding/json"
	"fmt"
)

// decodeServers returns a non-nil list so the wire carries an empty array
// rather than null.
func decodeServers(entries []json.RawMessage) ([]mcpServer, error) {
	servers := make([]mcpServer, len(entries))
	for i, entry := range entries {
		if err := json.Unmarshal(entry, &servers[i]); err != nil {
			return nil, fmt.Errorf("decode tool server entry %d: %w", i, err)
		}
	}
	return servers, nil
}
