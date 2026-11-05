package agentcore

import (
	"encoding/json"

	"github.com/sortie-ai/sortie/internal/domain"
)

// ToolCall is one tool call as an adapter maps it. It carries only what the
// runtime sent; cleaning, masking, and bounding happen downstream.
type ToolCall struct {
	// Name is the runtime's name for the tool, or "" when it names none.
	Name string

	Kind domain.ToolKind

	// Argument is the call's main argument as sent, or "" when none.
	Argument string
}

// ToolMapping gives the kind of one of a runtime's tools and the member of its
// JSON object input that holds the main argument.
type ToolMapping struct {
	Kind domain.ToolKind
	Key  string
}

// MapToolCall returns the call of tool name with input, per an adapter's
// table. It reads no input member other than the one the table names. A name
// absent from the table gives a call that holds only the name; an input that
// is not an object, or whose key member is absent, null, or not a string,
// gives the table's kind with no argument.
func MapToolCall(table map[string]ToolMapping, name string, input json.RawMessage) ToolCall {
	mapping, ok := table[name]
	if !ok {
		return ToolCall{Name: name}
	}
	call := ToolCall{Name: name, Kind: mapping.Kind}

	var members map[string]json.RawMessage
	if json.Unmarshal(input, &members) != nil {
		return call
	}
	var argument string
	if json.Unmarshal(members[mapping.Key], &argument) != nil {
		return call
	}
	call.Argument = argument
	return call
}

// QualifiedToolName returns tool qualified by its MCP server or namespace as
// "{qualifier}.{tool}", or tool alone when qualifier is empty.
func QualifiedToolName(qualifier, tool string) string {
	if qualifier == "" {
		return tool
	}
	return qualifier + "." + tool
}
