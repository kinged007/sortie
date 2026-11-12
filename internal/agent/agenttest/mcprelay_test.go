package agenttest_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
)

const (
	mcpEchoServerScenario = "mcp-echo-server"
	relayedEnvName        = "RELAY_TEST_DISPATCH_ID"
	relayedEnvValue       = "dispatch-7f3a91c2"
	bothMedia             = "application/json, text/event-stream"
)

type mcpEchoParams struct {
	Env string
}

func init() {
	scenarios[mcpEchoServerScenario] = agenttest.Typed(func(args []string, params mcpEchoParams) int {
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			var req struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(in.Bytes(), &req) != nil {
				return 2
			}
			encodedArgs, err := json.Marshal(args)
			if err != nil {
				return 2
			}
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"args\":%s,\"env\":%q}}\n", req.ID, encodedArgs, os.Getenv(params.Env))
		}
		if in.Err() != nil {
			return 2
		}
		return 0
	})
}

type recordingTB struct {
	testing.TB
	errors int
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(string, ...any) { r.errors++ }

func (r *recordingTB) Fatalf(string, ...any) { r.errors++ }

func writeMCPRecord(t *testing.T, lines ...string) agenttest.RecordingMCPRelay {
	t.Helper()

	path := filepath.Join(t.TempDir(), "traffic.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return agenttest.RecordingMCPRelay{RecordPath: path}
}

func TestRecordingMCPRelay_AssertToolCallSucceeded(t *testing.T) {
	t.Parallel()

	const (
		statusCall    = `{"pid":10,"dir":"to_server","msg":{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"sortie_status","arguments":{}}}}`
		statusOK      = `{"pid":10,"dir":"to_client","msg":{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{}"}]}}}`
		statusErr     = `{"pid":10,"dir":"to_client","msg":{"jsonrpc":"2.0","id":3,"error":{"code":-32602,"message":"bad"}}}`
		otherID       = `{"pid":10,"dir":"to_client","msg":{"jsonrpc":"2.0","id":4,"result":{}}}`
		otherPID      = `{"pid":11,"dir":"to_client","msg":{"jsonrpc":"2.0","id":3,"result":{}}}`
		serverRequest = `{"pid":10,"dir":"to_client","msg":{"jsonrpc":"2.0","id":3,"method":"ping"}}`
		otherToolCall = `{"pid":10,"dir":"to_server","msg":{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"other_tool"}}}`
		listCall      = `{"pid":10,"dir":"to_server","msg":{"jsonrpc":"2.0","id":3,"method":"tools/list"}}`
		envHeld       = `{"pid":10,"dir":"env","env":{"SORTIE_DISPATCH_ID":"abc"}}`
		envDiffers    = `{"pid":10,"dir":"env","env":{"SORTIE_DISPATCH_ID":"xyz"}}`
		envOtherPID   = `{"pid":11,"dir":"env","env":{"SORTIE_DISPATCH_ID":"abc"}}`
	)

	identity := map[string]string{"SORTIE_DISPATCH_ID": "abc"}
	pairedByRequest := []string{
		`{"pid":10,"dir":"to_server","req":1,"msg":{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"sortie_status"}}}`,
		`{"pid":10,"dir":"to_server","req":2,"msg":{"jsonrpc":"2.0","id":5,"method":"tools/list"}}`,
		`{"pid":10,"dir":"to_client","req":2,"msg":{"jsonrpc":"2.0","id":5,"result":{"tools":[]}}}`,
		`{"pid":10,"dir":"to_client","req":1,"msg":{"jsonrpc":"2.0","id":5,"error":{"code":-32603,"message":"boom"}}}`,
	}

	tests := []struct {
		name     string
		lines    []string
		missing  bool
		env      map[string]string
		wantFail bool
	}{
		{name: "legacy era over stdio", lines: eraTraffic("legacy", false)},
		{name: "modern era over stdio", lines: eraTraffic("modern", false)},
		{name: "modern era over http", lines: eraTraffic("modern", true)},
		{name: "http error response pairs with its own request", lines: pairedByRequest, wantFail: true},
		{name: "record file missing", missing: true, wantFail: true},
		{name: "call answered without error", lines: []string{statusCall, statusOK}},
		{name: "call answered after an unrelated message", lines: []string{statusCall, otherID, statusOK}},
		{name: "call answered with an error", lines: []string{statusCall, statusErr}, wantFail: true},
		{name: "call never answered", lines: []string{statusCall}, wantFail: true},
		{name: "response id differs", lines: []string{statusCall, otherID}, wantFail: true},
		{name: "response from another server process", lines: []string{statusCall, otherPID}, wantFail: true},
		{name: "server request reuses the id", lines: []string{statusCall, serverRequest}, wantFail: true},
		{name: "a different tool was called", lines: []string{otherToolCall, statusOK}, wantFail: true},
		{name: "no tools/call at all", lines: []string{listCall, statusOK}, wantFail: true},
		{name: "answering relay holds the identity", lines: []string{envHeld, statusCall, statusOK}, env: identity},
		{name: "answering relay holds a differing value", lines: []string{envDiffers, statusCall, statusOK}, env: identity, wantFail: true},
		{name: "only another relay holds the identity", lines: []string{envOtherPID, statusCall, statusOK}, env: identity, wantFail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			relay := agenttest.RecordingMCPRelay{RecordPath: filepath.Join(t.TempDir(), "absent.jsonl")}
			if !tt.missing {
				relay = writeMCPRecord(t, tt.lines...)
			}
			rec := &recordingTB{TB: t}

			relay.AssertToolCallSucceeded(rec, "sortie_status", tt.env)

			if failed := rec.errors > 0; failed != tt.wantFail {
				t.Errorf("AssertToolCallSucceeded(%q) failed = %v, want %v", "sortie_status", failed, tt.wantFail)
			}
		})
	}
}

func eraTraffic(era string, overHTTP bool) []string {
	meta := ""
	if era == "modern" {
		meta = `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}`
	}
	record := func(dir string, req int, msg string) string {
		if !overHTTP {
			return fmt.Sprintf(`{"pid":10,"dir":%q,"msg":%s}`, dir, msg)
		}
		return fmt.Sprintf(`{"pid":10,"dir":%q,"req":%d,"accept":%q,"msg":%s}`, dir, req, bothMedia, msg)
	}
	open := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`
	if era == "modern" {
		open = `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	}
	return []string{
		record("to_server", 1, open),
		record("to_client", 1, `{"jsonrpc":"2.0","id":1,"result":{}}`),
		record("to_server", 2, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{`+strings.TrimPrefix(meta, ",")+`}}`),
		record("to_client", 2, `{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`),
		record("to_server", 3, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"sortie_status"`+meta+`}}`),
		record("to_client", 3, `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{}"}]}}`),
	}
}

func TestRecordingMCPRelay_AssertAcceptNamesBothMedia(t *testing.T) {
	t.Parallel()

	postRecord := func(accept string) string {
		return fmt.Sprintf(`{"pid":10,"dir":"to_server","req":1,"accept":%q,"msg":{"jsonrpc":"2.0","id":1,"method":"initialize"}}`, accept)
	}
	postWithoutAccept := `{"pid":10,"dir":"to_server","req":1,"accept":"","msg":{"jsonrpc":"2.0","id":1,"method":"initialize"}}`
	stdioRequest := `{"pid":10,"dir":"to_server","msg":{"jsonrpc":"2.0","id":1,"method":"initialize"}}`

	tests := []struct {
		name     string
		lines    []string
		wantFail bool
	}{
		{name: "both named", lines: []string{postRecord("application/json, text/event-stream")}},
		{name: "json only", lines: []string{postRecord("application/json")}, wantFail: true},
		{name: "header absent", lines: []string{postWithoutAccept}, wantFail: true},
		{name: "stdio traffic only", lines: []string{stdioRequest}, wantFail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			relay := writeMCPRecord(t, tt.lines...)
			rec := &recordingTB{TB: t}

			relay.AssertAcceptNamesBothMedia(rec, "claude-code")

			if failed := rec.errors > 0; failed != tt.wantFail {
				t.Errorf("AssertAcceptNamesBothMedia(%q) failed = %v, want %v", tt.name, failed, tt.wantFail)
			}
		})
	}
}

func TestMCPRelayScenario_RelaysAndRecords(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	server := agenttest.FakeRuntime(t, dir, "echo-server", mcpEchoServerScenario, mcpEchoParams{Env: relayedEnvName})
	relay := agenttest.NewRecordingMCPRelay(t, dir, server, relayedEnvName)

	cmd := exec.CommandContext(t.Context(), relay.Command, "mcp-server", "--workflow", "WORKFLOW.md") //nolint:gosec // G204: the command is this test's own fake runtime.
	cmd.Env = append(os.Environ(), relayedEnvName+"="+relayedEnvValue)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start(%q): %v", relay.Command, err)
	}

	request := `{"jsonrpc":"2.0","id":"call-7","method":"tools/call","params":{"name":"sortie_status"}}`
	if _, err := fmt.Fprintln(stdin, request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	gotLine, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	wantLine := `{"jsonrpc":"2.0","id":"call-7","result":{"args":["mcp-server","--workflow","WORKFLOW.md"],"env":"` + relayedEnvValue + `"}}` + "\n"
	if gotLine != wantLine {
		t.Errorf("relayed response = %q, want %q", gotLine, wantLine)
	}

	relay.AssertToolCallSucceeded(t, "sortie_status", map[string]string{relayedEnvName: relayedEnvValue})
}
