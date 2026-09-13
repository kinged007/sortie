package pi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sortie-ai/sortie/internal/domain"
)

// pi event schema for `pi -p --mode json`: one JSON object per stdout
// line. Framing events (session/agent_start/turn_start/message_start)
// carry no content; message_update carries streaming deltas;
// tool_execution_end carries tool results; turn_end/message_end carry the
// final message with its token usage. There is no error event type: a
// failed turn exits nonzero and writes diagnostics to stderr, which the
// turn engine already handles via its exit-code path.

type parsedLine struct {
	Event     *rawRunEvent
	PlainText string
}

type rawRunEvent struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp,omitempty"`
	ID        string          `json:"id,omitempty"`
	CWD       string          `json:"cwd,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`

	AssistantMessageEvent json.RawMessage `json:"assistantMessageEvent,omitempty"`
	ToolCallID            string          `json:"toolCallId,omitempty"`
	ToolName              string          `json:"toolName,omitempty"`
	Args                  json.RawMessage `json:"args,omitempty"`
	Result                json.RawMessage `json:"result,omitempty"`
}

type rawUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Reasoning  int64 `json:"reasoning"`
}

type rawMessage struct {
	Role    string       `json:"role"`
	Content []rawContent `json:"content"`
	Usage   *rawUsage    `json:"usage,omitempty"`
	API     string       `json:"api,omitempty"`
	Model   string       `json:"model,omitempty"`
}

type rawContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

type rawAssistantDelta struct {
	Type         string `json:"type"`
	ContentIndex int    `json:"contentIndex"`
	Delta        string `json:"delta,omitempty"`
	Content      string `json:"content,omitempty"`
	ToolName     string `json:"toolName,omitempty"`
}

type rawToolResult struct {
	Content []rawContent `json:"content"`
	IsError bool         `json:"isError"`
}

func parseRunEvent(line []byte) (rawRunEvent, error) {
	var event rawRunEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return rawRunEvent{}, fmt.Errorf("parse run event: %w", err)
	}
	return event, nil
}

// SessionRef returns the session id carried by session events, or "".
func (e *rawRunEvent) SessionRef() string {
	if e.Type == "session" {
		return e.ID
	}
	return ""
}

// UsageRun returns the run-cumulative usage and producing model carried
// by turn_end/message_end events. The third return is false when the
// event carries no usage figure. User-role messages name no production
// model, so their figure reports an empty model.
func (e *rawRunEvent) UsageRun() (domain.TokenUsage, string, bool) {
	if e.Type != "turn_end" && e.Type != "message_end" {
		return domain.TokenUsage{}, "", false
	}
	var msg rawMessage
	if err := json.Unmarshal(e.Message, &msg); err != nil || msg.Usage == nil {
		return domain.TokenUsage{}, "", false
	}
	u := msg.Usage
	model := ""
	if msg.Role == "assistant" {
		model = msg.Model
	}
	return domain.TokenUsage{
		InputTokens:     u.Input + u.CacheRead + u.CacheWrite,
		OutputTokens:    u.Output + u.Reasoning,
		CacheReadTokens: u.CacheRead,
	}, model, true
}

// TextDelta unpacks a message_update event: assistant text delta, or the
// tool name for toolcall deltas (toolDone on toolcall_end).
func (e *rawRunEvent) TextDelta() (text, tool string, toolDone, isErr bool) {
	if e.Type != "message_update" {
		return "", "", false, false
	}
	var delta rawAssistantDelta
	if err := json.Unmarshal(e.AssistantMessageEvent, &delta); err != nil {
		return "", "", false, false
	}
	switch {
	case strings.HasPrefix(delta.Type, "text_"):
		if delta.Type == "text_end" {
			return delta.Content, "", false, false
		}
		return delta.Delta, "", false, false
	case strings.HasPrefix(delta.Type, "thinking_"):
		return "", "", false, false
	case strings.HasPrefix(delta.Type, "toolcall_"):
		if delta.Type == "toolcall_end" {
			var call struct {
				ToolCall struct {
					Name string `json:"name"`
				} `json:"toolCall"`
			}
			if err := json.Unmarshal(e.AssistantMessageEvent, &call); err != nil {
				return "", "", false, false
			}
			return "", call.ToolCall.Name, true, false
		}
		return "", delta.ToolName, false, false
	default:
		return "", "", false, false
	}
}

// ToolResult unpacks a tool_execution_end event into name + error flag.
func (e *rawRunEvent) ToolResult() (name string, isErr bool) {
	if e.Type != "tool_execution_end" {
		return "", false
	}
	var res rawToolResult
	if err := json.Unmarshal(e.Result, &res); err != nil {
		return e.ToolName, false
	}
	return e.ToolName, res.IsError
}

// CompletedText returns the assistant text of a turn_end/message_end event.
func (e *rawRunEvent) CompletedText() string {
	if e.Type != "turn_end" && e.Type != "message_end" {
		return ""
	}
	var msg rawMessage
	if err := json.Unmarshal(e.Message, &msg); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range msg.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}
