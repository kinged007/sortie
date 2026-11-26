// Package mcpserver exposes registered [domain.AgentTool] implementations
// as an MCP server built on the official Go SDK. [Server.Serve] speaks MCP
// over a pair of streams; [Server.ServeStreamableHTTP] speaks the Streamable
// HTTP transport behind a bearer. Construct a server with [NewServer].
package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sortie-ai/sortie/internal/domain"
)

const serverName = "sortie"

// Server serves a [domain.ToolRegistry] over MCP. Construct it with
// [NewServer].
type Server struct {
	sdk    *mcp.Server
	logger *slog.Logger

	execMu sync.Mutex // serializes tool Execute calls across transports
}

// NewServer builds a server that registers every tool in registry. The
// version string is reported to MCP clients as the server version. A nil
// logger selects [slog.Default].
func NewServer(registry *domain.ToolRegistry, logger *slog.Logger, version string) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		sdk:    mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil),
		logger: logger,
	}
	for _, tool := range registry.List() {
		s.sdk.AddTool(&mcp.Tool{
			Name:        tool.Name(),
			Description: tool.Description(),
			InputSchema: tool.InputSchema(),
		}, s.toolHandler(tool))
	}
	return s
}

// Serve speaks MCP over in and out until in reaches end of file or ctx ends,
// then returns nil. A line that is not JSON ends the session with the SDK's
// read error. The caller keeps in open until it has read every response it
// needs: a response still in flight at end of file can be lost.
func (s *Server) Serve(ctx context.Context, in io.ReadCloser, out io.WriteCloser) error {
	err := s.sdk.Run(ctx, &mcp.IOTransport{Reader: in, Writer: out})
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (s *Server) toolHandler(tool domain.AgentTool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		output, err := s.execute(ctx, tool, req.Params.Arguments)
		if err != nil {
			return textResult(err.Error(), true), nil
		}
		return textResult(string(output), false), nil
	}
}

func (s *Server) execute(ctx context.Context, tool domain.AgentTool, arguments json.RawMessage) (json.RawMessage, error) {
	s.execMu.Lock()
	defer s.execMu.Unlock()
	return tool.Execute(ctx, arguments)
}

func textResult(text string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		IsError: isError,
	}
}
