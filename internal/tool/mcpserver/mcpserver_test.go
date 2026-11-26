package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"testing"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/waittest"
)

const modernVersion = "2026-07-28"

var clientInfo = map[string]any{"name": "test", "version": "1"}

type stubTool struct {
	name string
	err  error
}

func (s *stubTool) Name() string                 { return s.name }
func (s *stubTool) Description() string          { return "stub " + s.name }
func (s *stubTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *stubTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	return args, s.err
}

func testRegistry() *domain.ToolRegistry {
	reg := domain.NewToolRegistry()
	reg.Register(&stubTool{name: "echo"})
	reg.Register(&stubTool{name: "fail", err: errors.New("tool exploded")})
	return reg
}

type sendFunc func(t *testing.T, method string, params map[string]any) json.RawMessage

type session struct {
	name    string
	version string
	send    sendFunc
}

var sessions = []session{
	{name: "legacy initialize", version: "2025-11-25"},
	{name: "modern server/discover", version: modernVersion},
}

func (s session) call(t *testing.T, method string, params map[string]any) json.RawMessage {
	t.Helper()
	withMeta := map[string]any{}
	maps.Copy(withMeta, params)
	if s.version == modernVersion {
		withMeta["_meta"] = map[string]any{
			"io.modelcontextprotocol/protocolVersion":    s.version,
			"io.modelcontextprotocol/clientInfo":         clientInfo,
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}
	}
	return s.send(t, method, withMeta)
}

func (s session) open(t *testing.T) {
	t.Helper()
	if s.version == modernVersion {
		s.call(t, "server/discover", nil)
		return
	}
	s.call(t, "initialize", map[string]any{"protocolVersion": s.version, "capabilities": map[string]any{}, "clientInfo": clientInfo})
}

func (s session) callTool(t *testing.T, name string, args map[string]any) (text string, isError bool) {
	t.Helper()
	got := decode[struct {
		Content []struct{ Text string }
		IsError bool
	}](t, s.call(t, "tools/call", map[string]any{"name": name, "arguments": args}))
	if len(got.Content) != 1 {
		t.Fatalf("tools/call %s content = %v, want one block", name, got.Content)
	}
	return got.Content[0].Text, got.IsError
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return v
}

func rpcRequest(id int, method string, params map[string]any) []byte {
	request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return append(request, '\n')
}

func rpcResult(t *testing.T, method string, message []byte) json.RawMessage {
	t.Helper()
	resp := decode[struct{ Result, Error json.RawMessage }](t, message)
	if resp.Error != nil {
		t.Fatalf("%s error = %s, want a result", method, resp.Error)
	}
	return resp.Result
}

func startStdio(t *testing.T) sendFunc {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	lines := make(chan []byte, 16)
	go func() {
		done <- NewServer(testRegistry(), nil, "1.0.0").Serve(waittest.Context(t), inR, outW)
		_ = outW.Close()
	}()
	go func() {
		for scanner := bufio.NewScanner(outR); scanner.Scan(); {
			lines <- bytes.Clone(scanner.Bytes())
		}
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		if err := waittest.Receive(t, done, "Serve to return"); err != nil {
			t.Errorf("Serve() = %v, want nil on end of input", err)
		}
	})

	id := 0
	return func(t *testing.T, method string, params map[string]any) json.RawMessage {
		t.Helper()
		id++
		_, _ = inW.Write(rpcRequest(id, method, params))
		return rpcResult(t, method, waittest.Receive(t, lines, "response to "+method))
	}
}

func TestServe_ToolsOverStdio(t *testing.T) {
	t.Parallel()

	for _, s := range sessions {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()

			s.send = startStdio(t)
			s.open(t)

			listed := decode[struct{ Tools []struct{ Name string } }](t, s.call(t, "tools/list", nil))
			if got := fmt.Sprint(listed.Tools); got != "[{echo} {fail}]" {
				t.Errorf("tools/list tools = %s, want [{echo} {fail}]", got)
			}

			if text, isError := s.callTool(t, "echo", map[string]any{"input": "hello"}); text != `{"input":"hello"}` || isError {
				t.Errorf("tools/call echo = (%q, isError %t), want (%q, false)", text, isError, `{"input":"hello"}`)
			}
			if text, isError := s.callTool(t, "fail", nil); text != "tool exploded" || !isError {
				t.Errorf("tools/call fail = (%q, isError %t), want (%q, true)", text, isError, "tool exploded")
			}
		})
	}
}
