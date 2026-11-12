package claude

import (
	"path/filepath"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/mcpconfig"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/workspacekit"
)

const mcpConfigName = "claude-mcp.json"

// writeDerivedConfig returns the path a local launch hands to --mcp-config.
func writeDerivedConfig(workspacePath string, tools *agentcore.ToolSession) (string, error) {
	doc := tools.Delivery(sshutil.ForwardOutcome{}).Document
	if err := workspacekit.WriteSortieFile(workspacePath, mcpConfigName, doc); err != nil {
		return "", err
	}
	return filepath.Join(workspacePath, workspacekit.SortieDir, mcpConfigName), nil
}

// placeMCPConfig passes the delivery inline: a host path names nothing on the
// remote host.
func placeMCPConfig(d mcpconfig.Delivery) sshutil.LatePreamble {
	if len(d.Document) == 0 {
		return sshutil.LatePreamble{}
	}
	return sshutil.LatePreamble{Args: []string{"--mcp-config", string(d.Document)}}
}
