package pi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sortie-ai/sortie/internal/workspacekit"
)

// mcpServersEnv names the variable that holds the path of the derived MCP
// configuration file the bridge extension reads its servers from.
const mcpServersEnv = "SORTIE_PI_MCP_SERVERS"

// bridgeFailurePrefix starts every line the bridge extension writes to
// standard error when it cannot register a server.
const bridgeFailurePrefix = "sortie-pi-mcp-bridge:"

// buildTurnEnv returns the environment every turn's subprocess inherits,
// minus an inherited mcpServersEnv, plus mcpServersEnv when the session
// carries tool servers on a local launch. The skeleton adds the header
// values their references name. A remote session carries neither.
func buildTurnEnv(state *sessionState) []string {
	env := withoutServerList(os.Environ())
	if state.mcpConfigPath != "" {
		env = append(env, mcpServersEnv+"="+state.mcpConfigPath)
	}
	return env
}

func withoutServerList(base []string) []string {
	env := make([]string, 0, len(base)+1)
	for _, entry := range base {
		if key, _, found := strings.Cut(entry, "="); found && key == mcpServersEnv {
			continue
		}
		env = append(env, entry)
	}
	return env
}

const bridgeFileName = "pi-mcp-bridge.js"

const derivedConfigName = "pi-mcp.json"

const bridgeFileContent = `import { readFileSync } from "node:fs";

export default function(pi){
  const fail = (what, e) => console.error("sortie-pi-mcp-bridge: " + what + ": " + (e && e.message ? e.message : e));
  const path = process.env.SORTIE_PI_MCP_SERVERS;
  if(!path) return;
  let doc;
  try{ doc = JSON.parse(readFileSync(path, "utf8")); } catch(e){ fail("cannot read " + path, e); return; }
  const servers = doc.mcpServers || {};
  const expand = (v) => typeof v === "string" ? v.replace(/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/g, (_, n) => process.env[n] || "") : v;
  for(const [name, cfg] of Object.entries(servers)){
    const headers = cfg.headers ? Object.fromEntries(Object.entries(cfg.headers).map(([k, v]) => [k, expand(v)])) : undefined;
    try{ pi.registerMcpServer(name, {...cfg, ...(headers ? {headers} : {}), exposure: "direct"}); } catch(e){ fail("cannot register server " + name, e); }
  }
}
`

// writeDerivedConfig writes the rendered tool-server document to
// <workspacePath>/.sortie/pi-mcp.json and the static bridge extension to
// <workspacePath>/.sortie/pi-mcp-bridge.js, and returns their paths.
func writeDerivedConfig(workspacePath string, document []byte) (configPath, bridgePath string, err error) {
	if err := workspacekit.WriteSortieFile(workspacePath, derivedConfigName, document); err != nil {
		return "", "", err
	}
	if err := workspacekit.WriteSortieFile(workspacePath, bridgeFileName, []byte(bridgeFileContent)); err != nil {
		return "", "", fmt.Errorf("write pi mcp bridge: %w", err)
	}
	dir := filepath.Join(workspacePath, workspacekit.SortieDir)
	return filepath.Join(dir, derivedConfigName), filepath.Join(dir, bridgeFileName), nil
}
