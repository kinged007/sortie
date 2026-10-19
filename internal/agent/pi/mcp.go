package pi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sortie-ai/sortie/internal/agent/mcpconfig"
	"github.com/sortie-ai/sortie/internal/workspacekit"
)

// buildTurnEnv returns the environment every turn's subprocess inherits,
// plus SORTIE_PI_MCP_SERVERS when the session carries Sortie MCP servers
// locally. The orchestrator passes MCPConfigPath only for a local launch
// carrying servers, so a remote session carries neither the bridge path
// nor this variable.
func buildTurnEnv(state *sessionState) []string {
	env := os.Environ()
	if state.mcpEnvValue != "" {
		env = append(env, "SORTIE_PI_MCP_SERVERS="+state.mcpEnvValue)
	}
	return env
}

const bridgeFileName = "pi-mcp-bridge.js"

const bridgeFileContent = `export default function(pi){
  const raw = process.env.SORTIE_PI_MCP_SERVERS;
  if(!raw) return;
  let doc;
  try{ doc = JSON.parse(raw); } catch{ return; }
  const servers = doc.mcpServers || {};
  for(const [name, cfg] of Object.entries(servers)){
    try{ pi.registerMcpServer(name, {...cfg, exposure: "direct"}); } catch{}
  }
}
`

// translateMCPServers parses mcpConfigPath into its servers, or returns
// nil for a remote launch, an empty path, or a configuration declaring no
// server. Remote launches carry no Sortie MCP servers because the
// translated delivery is a local extension file and an environment
// variable, neither of which a REMOTE_COMMAND launch withholds in a way
// the DeliversTools predicate can promise.
func translateMCPServers(mcpConfigPath string, remote bool) ([]mcpconfig.Server, error) {
	if mcpConfigPath == "" || remote {
		return nil, nil
	}
	servers, err := mcpconfig.Parse(mcpConfigPath)
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, nil
	}
	return servers, nil
}

// buildMCPEnvValue returns the JSON the bridge extension reads from
// SORTIE_PI_MCP_SERVERS. It mirrors the worker-generated mcp.json shape
// so its string still carries every server name, command, url and env
// name the Translated assertion looks for.
func buildMCPEnvValue(servers []mcpconfig.Server) (string, error) {
	if len(servers) == 0 {
		return "", nil
	}
	mcpServers := make(map[string]any, len(servers))
	for _, s := range servers {
		switch s.Transport {
		case mcpconfig.TransportStdio:
			entry := map[string]any{
				"command": s.Command,
			}
			if len(s.Args) > 0 {
				entry["args"] = s.Args
			}
			if len(s.Env) > 0 {
				entry["env"] = s.Env
			}
			if s.Enabled != nil {
				entry["enabled"] = *s.Enabled
			}
			mcpServers[s.Name] = entry
		case mcpconfig.TransportHTTP:
			entry := map[string]any{
				"url": s.URL,
			}
			if len(s.Headers) > 0 {
				entry["headers"] = s.Headers
			}
			if s.Enabled != nil {
				entry["enabled"] = *s.Enabled
			}
			mcpServers[s.Name] = entry
		default:
			return "", fmt.Errorf("mcp server %q: entry carries neither command nor url", s.Name)
		}
	}
	doc := map[string]any{"mcpServers": mcpServers}
	data, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal pi mcp env value: %w", err)
	}
	return string(data), nil
}

// ensureBridgeFile writes the static bridge extension to
// <workspacePath>/.sortie/pi-mcp-bridge.js when servers is non-empty,
// and returns its absolute path. It reuses an existing file whose
// content already matches, so a resumed session does not churn the
// workspace.
func ensureBridgeFile(workspacePath string, servers []mcpconfig.Server) (string, error) {
	if len(servers) == 0 {
		return "", nil
	}
	if workspacePath == "" {
		return "", fmt.Errorf("workspace path is empty")
	}
	dir, err := workspacekit.OpenSortieDir(workspacePath, true)
	if err != nil {
		return "", fmt.Errorf("open .sortie directory: %w", err)
	}
	defer dir.Close() //nolint:errcheck

	// Reuse if already correct, to avoid rewriting on every retry.
	if existing, err := os.ReadFile(filepath.Join(workspacePath, workspacekit.SortieDir, bridgeFileName)); err == nil {
		if string(existing) == bridgeFileContent {
			return filepath.Join(workspacePath, workspacekit.SortieDir, bridgeFileName), nil
		}
	}

	if err := workspacekit.ReplaceFile(dir, bridgeFileName, []byte(bridgeFileContent)); err != nil {
		return "", fmt.Errorf("write pi mcp bridge: %w", err)
	}
	return filepath.Join(workspacePath, workspacekit.SortieDir, bridgeFileName), nil
}
