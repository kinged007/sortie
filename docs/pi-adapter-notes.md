# Pi adapter notes

Working notes for anyone changing Sortie's Pi adapter in `internal/agent/pi`: what `pi -p --mode json` actually writes to stdout, why the exit code is not a failure signal, how session identity and resume work, and the tool channel this runtime does not have.

## Where to get the volatile facts

Nothing here pins a flag list or a model name. Read the argument surface off `pi --help` on the version you are targeting, and take the provider and authentication surface from the documentation the installed release ships. The adapter's `command.go` defines what Sortie sends and `parse.go` defines what it reads; the event vocabulary checked there is the 0.85.1 schema, so re-derive it from a live run after any version bump rather than extending the accepted set on the assumption that a new type is harmless.

## The surface Sortie drives

The adapter launches one subprocess per turn:

```
pi -p --mode json [--session <id>] [--model <name>] [--thinking <level>] \
   [--tools a,b] [--exclude-tools c,d] [--approve | --no-approve] -- <prompt>
```

`-p` is the non-interactive prompt mode, `--mode json` is the machine-readable stream, and the prompt is a positional argument after `--`, so a prompt beginning with a dash cannot be read as a flag. The process working directory is already the issue workspace, so no directory flag is sent, and the workspace is not named on the command line. The adapter's own flags, not the workflow's `agent.command`, decide the binary: `agent.command` overrides the program and may carry leading arguments, and the adapter's arguments follow them.

stdout carries one JSON object per line, framed by a session header before anything else:

```
{"type":"session","version":3,"id":"<uuid>","timestamp":"...","cwd":"<dir>"}
{"type":"agent_start"}
{"type":"turn_start"}
{"type":"message_start","message":{...}}
{"type":"message_update","assistantMessageEvent":{...}}
{"type":"message_end","message":{...}}
{"type":"tool_execution_start","toolCallId":"...","toolName":"...","args":{...}}
{"type":"tool_execution_update","toolCallId":"...","partialResult":{...}}
{"type":"tool_execution_end","toolCallId":"...","toolName":"...","result":{...},"isError":false}
{"type":"turn_end","message":{...},"toolResults":[...]}
{"type":"agent_end","messages":[...],"willRetry":false}
```

Those are the families the adapter reads. The full 0.85.1 set also contains `agent_settled`, `bash_execution_update`, `queue_update`, `entry_appended`, `session_info_changed`, `thinking_level_changed`, `compaction_start`, `compaction_end`, `auto_retry_start`, `auto_retry_end`, `summarization_retry_scheduled`, `summarization_retry_attempt_start`, and `summarization_retry_finished`. Most of them are framing with nothing for the orchestrator to record, and several only appear in an interactive session. Two are not: `tool_execution_end` and `compaction_end`, both covered below.

A line that is not a JSON object carrying a `type`, and a `type` outside that set, are both reported as a malformed line rather than skipped. That is deliberate. A future event the adapter silently dropped could carry the only text, the only usage, or the only failure the turn produced, and a stream that has moved past what this adapter understands is worth an operator's attention rather than a quiet empty turn. The same holds for the `message_update` delta types (`text_start`, `text_delta`, `text_end`, `thinking_start`, `thinking_delta`, `thinking_end`, `toolcall_start`, `toolcall_delta`, `toolcall_end`, `start`, `done`, `error`): a variant outside the set is a fault.

## The two shapes that decide how a line is read

**`message_update` is delta-only.** The CLI strips both the event's own cumulative `message` and the `partial` field inside its `assistantMessageEvent`, so only the delta event's fields carry information. Do not build a message from the deltas: they arrive in pieces, and the authoritative record is the `message` on `message_end`.

**`turn_end` repeats `message_end`.** Both name the same assistant response for one pi turn, and `turn_end` additionally carries the tool results. The adapter treats `message_end` as authoritative for the response's text and stop reason, and `turn_end` as the fallback for a stream that delivered only one of the two; the figure the turn reports about usage is taken from whichever of the two arrives, and a stream carrying both does not count the response twice. `agent_end` repeats neither: it carries the whole message list, so reading it for text or usage would report the same response several times over. That is a reason to ignore it, not a reason to read it differently.

## Exit codes and failure

`pi --mode json` applies no exit-code check to a failed model response. A response that stopped with `stopReason` `error` or `aborted` still exits 0, so the exit code cannot decide whether a turn succeeded. The stop reason on the assistant message is the only evidence: the adapter records the first assistant response that stopped either way, and the finalize path turns it into a terminal failure with error kind `response_error`, taking the response's `errorMessage` as the message and falling back to naming the stop reason when the runtime carried none. A bounded excerpt of the turn's stderr is appended when there is one, and the collected stderr lines are logged as warnings.

The first assistant response to fail is kept rather than the last, because a later response in the same turn may succeed and mask the first fault. A turn that produced no assistant output at all takes the shared zero-work row and is reported as a failure rather than a silent success, which is also where a stream that never produced a JSON event and exited 0 lands.

The first-JSON deadline defaults to thirty seconds and takes the workflow's read timeout when one is configured. It is armed only until the first JSON event arrives, so it guards a subprocess that never starts talking; a mid-turn stall is the orchestrator's `stall_timeout_ms`. Once the subprocess has been reaped, the remaining stdout is drained for a bounded grace so a descendant holding the output handle cannot withhold the turn's result.

The session header is checked, not merely recorded. Its `cwd` must be the turn's own workspace, and on a resumed session its id must be the id Sortie already holds. Either mismatch kills the process and fails the turn with `response_error` naming both values, because a stream that reports a different workspace or a different session is not the session this turn was supposed to continue.

## Session identity and resume

The session id comes from the header, not from Sortie. `StartSession` stores the orchestrator's resume id when there is one and the header supplies one when there is not, and a `session_started` event is emitted exactly once per Sortie session, even across a resume, so a continuation turn does not look like a new session to the dashboard.

Once an id is known, every later turn of that session passes `--session <id>`. The first turn of a new session carries no such flag. A resumed session therefore re-sends its own id in every header, which is what makes the mismatch check above meaningful rather than a comparison against a value the adapter itself chose.

## Native flags

Every key the `pi` block accepts maps to a flag, and a key outside the accepted set is refused rather than ignored: pi has no passthrough channel, so an unrecognized key would look configured while doing nothing.

- `model` becomes `--model`.
- `thinking` becomes `--thinking`. The accepted values are the levels pi itself accepts: `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`. Any other value is refused, so a level a later release adds fails construction until someone establishes what it does.
- `allowed_tools` becomes `--tools` with the names joined by commas. `denied_tools` becomes `--exclude-tools` the same way. A tool list must be a list of non-empty names; anything else is a type fault rather than a silently shortened list, and a name in both lists is refused because the two would contradict each other.
- `project_trust` decides between `--approve` and `--no-approve`. The default is `ignore`, which sends `--no-approve` and keeps the workspace's own project-local files and packages out of the run. An unattended turn has no operator to answer a trust prompt, so the permissive value has to be asked for by name rather than inherited from a developer's shell.

## Usage accounting

The registered kind declares turn-end arrival and per-model attribution. Each pi model response carries one `usage` figure on the `turn_end` (or `message_end`) message, and the turn's own figure is the sum of every such response plus every compaction call. Two normalizations matter:

- `reasoning` is a subset of `output`, not an addition to it, so it is never summed a second time. A thinking block is also excluded from the text the turn reports, for the same reason: the runtime already counts its tokens inside the response's output usage.
- `cacheRead` is part of the input the provider billed, so it is added into the input total, and it is also reported as its own subset counter. `cacheWrite` is likewise folded into the input total.

The model the turn reports is the model of the last response whose usage was counted, which is the right answer for a turn that ran on more than one model. A run-cumulative total is kept per Sortie session across every turn of that session, including the compaction calls, and a turn that produced no usage at all reports no figure rather than a zero that would lower an already-reported run.

Compaction is an extra model call, so it is an extra charge. A `compaction_end` carrying a result with a `usage` figure contributes that figure to the turn; an aborted or failed compaction carries no result, contributes nothing, and a non-aborted failure additionally surfaces as a notification naming the runtime's error message. Pi's own auto-compaction is therefore a real cost driver, and a workflow that expects the whole session's tokens to be the sum of its assistant responses will undercount by exactly the summarization calls.

## SSH

The adapter builds the same ssh argument vector the other fork-per-turn kinds use, from the shared helper, with the same per-turn arguments: a remote session runs the same `pi -p --mode json ... -- <prompt>` command on the remote host, in the remote workspace, with the same session flag. Nothing is delivered over SSH that a local launch withholds, because nothing is delivered on a local launch either: the only thing the local launch could have added is the MCP configuration, and this adapter has none to add. `AgentPID` is left unset for a remote turn, since the pid belongs to the ssh process rather than to pi.

## Sortie's own tools

pi has no MCP transport. The kind registers `MCPInjectionUnsupported` and delivers nothing: the generated `.sortie/mcp.json` is never read, Sortie's tools are never advertised to a pi session, and nothing in the session can call them. A workflow that needs Sortie's own tools must not run on this kind; the tool set the agent works with is whatever pi itself provides, narrowed by `allowed_tools` and `denied_tools`.

`mcp_config` is not a key of the `pi` block. Writing it is an unknown-key fault, reported by `sortie validate` under `pi.mcp_config.unknown_key` and refused at construction, rather than a warning about a value that goes nowhere. Two preflight warnings still describe the situation: `agent.kind.no_tool_channel` fires for every workflow on this kind, because there is no local-launch channel to advertise one through, and `agent.mcp_config` fires as well when a `mcp_config` key is configured for it. Both are warnings; the run proceeds.

## Verifying a change

The package's own tests drive a fake `pi` written as a shell script, so argument construction, every event family, compaction accounting, session resume, workspace and session-id mismatch, cancellation, and each exit path are covered hermetically. There is no live-runtime suite in this package; a change to the event vocabulary is therefore only as trustworthy as the fixtures, so a version bump should re-run the turn against the real binary and diff the stream against the recorded fixtures before the accepted sets are widened.
