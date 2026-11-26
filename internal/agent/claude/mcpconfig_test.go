package claude

import (
	"slices"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/mcpconfig"
)

func TestPlaceMCPConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		document string
		want     []string
	}{
		{"document becomes the inline flag value", `{"mcpServers":{}}`, []string{"--mcp-config", `{"mcpServers":{}}`}},
		{"no document places nothing", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			late := placeMCPConfig(mcpconfig.Delivery{Document: []byte(tt.document)})

			if !slices.Equal(late.Args, tt.want) || len(late.Env) != 0 {
				t.Errorf("placeMCPConfig(%q) = %+v, want args %q and no env", tt.document, late, tt.want)
			}
		})
	}
}
