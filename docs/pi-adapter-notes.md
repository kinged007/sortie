# Pi adapter notes

Working notes for anyone changing Sortie's Pi adapter in `internal/agent/pi`.

Last updated: 2026-10-23

## Where to get the volatile facts

Nothing here pins a flag list, an event vocabulary, or a model name. Read the flags off `pi --help` on the version you target. Take providers, authentication, sessions, and project trust from the documentation the installed release ships, in its `docs` directory. The JSON stream is described in its `json.md`. The adapter's `command.go` defines what Sortie sends and `parse.go` defines what it reads. The event and delta types `parse.go` accepts are a closed set: a type outside it is reported as a malformed line, so re-derive the set from a live run after a pi upgrade instead of widening it on the assumption that a new type is harmless.

## The surface we drive

One subprocess per turn: `pi -p --mode json`, with the prompt on standard input and the issue workspace as the process working directory. `-p` is the non-interactive mode and `--mode json` writes one JSON object per stdout line. No directory flag is sent.

The prompt is not an argument because pi treats a message argument that begins with `@` as a file to include, and a long prompt can exceed the argument limit. Over SSH the prompt follows the environment preamble on standard input.

A credential verification session adds `--no-session`, so it leaves no session file in pi's session store.

Two shapes decide how a line is read:

- `message_update` is delta-only. The CLI strips the cumulative `message` and the `partial` member of its `assistantMessageEvent`, so only the delta's own fields carry information. The authoritative record of a response is the `message` on `message_end`.
- `turn_end` repeats `message_end` for the same assistant response and adds the tool results. `agent_end` carries the whole message list, so reading it for text or usage would count a response several times.

## Turn lifecycle

Turns run on `agentcore.ForkPerTurnSession`, the skeleton the other fork-per-turn kinds use. Launch, output reading, the reap, the stop, and the order in which a turn's endings win belong to the skeleton, so a lifecycle change goes there and reaches every kind. This package supplies what is pi's own, through the skeleton's hooks:

- `BuildArgs` returns the run arguments. `PromptOnStdin` is set.
- `FirstEventTimeout` is `agentcore.ReadTimeout` of the agent configuration: `agent.read_timeout_ms`, 5 seconds unless the workflow sets another value. A process that prints nothing parseable within it is stopped and the turn fails with `response_timeout`. The deadline ends at the first accepted line, so it catches a process that never talks, not a stall during a turn. Stalls belong to the orchestrator's `stall_timeout_ms`.
- `ParseLine` maps each line to events. A line that is not a JSON object with a `type` is returned as an error and the skeleton reports it as malformed. A payload that does not decode, an unknown event type, and an unknown delta type are reported as malformed by the adapter. `agent_end` without a retry pending returns a non-nil result, which marks the run as ended.
- `SettleUsage` adds the turn's responses to the session total and reports it.
- `OnFinalize` builds the turn evidence from the last response's stop reason.
- `GetUsage` and `GetSessionID` read the session state.

pi emits `message_update`, `tool_execution_update`, `turn_start`, `compaction_*`, and `auto_retry_*` events during a turn. Each is reported as an empty notification so the orchestrator's stall timer sees activity during long output or a long tool run.

## Session identity and resume

The session id comes from the `session` header that opens the stream. `StartSession` holds the orchestrator's resume id when there is one, and the header supplies the id when there is not. Every later turn passes `--session <id>`. `session_started` is emitted once per Sortie session, so a continuation turn does not look like a new session.

The header is checked. A different id on a session that already has one ends the turn with `response_error`.

The header's `cwd` is checked on a local launch: it must be the same directory as the turn's workspace. pi can move a session to the directory the stored session was created in, so a header naming another directory means the turn is not working in the issue workspace. The comparison uses the file system's own identity of the two directories rather than their text, because pi reports the physical path of a workspace reached through a symbolic link, such as `/var` on macOS, which is a link to `/private/var`.

On a remote launch the workspace path names the remote host's file system, which Sortie cannot inspect, so only the session id is checked.

## Permissions

pi's project trust decides whether pi loads the resources a working folder supplies: its `.pi` settings, MCP servers, extensions, skills, prompt templates, and system prompt files. An unattended turn cannot answer pi's trust prompt, so the adapter always passes one of pi's two flags. `approve: true` in the `pi` block sends `--approve`. An absent key or `false` sends `--no-approve`, so a repository cannot make the agent load those resources unless the operator asks for it.

Trust has limits that pi's own `docs/security.md` states. It does not limit what tool calls can access or change, and it does not stop content in the workspace from steering the model: context files such as `AGENTS.md` and `CLAUDE.md` load whatever the trust decision is. pi has no approval prompt per tool call and runs with the permissions of the account that started it, so isolation comes from how that account, host, or container is set up.

The JSON stream has no approval request event, so the adapter does no permission classification. A tool call that fails appears as a `tool_execution_end` with `isError` set.

## Usage accounting

The kind declares incremental usage arrival and per-model attribution. Each assistant response is one provider request and carries one `usage` figure on its `message_end`. A response that ended in a failed retry attempt, with stop reason `error`, is a request too and is reported. `turn_end` carries the same figure and is read only when its turn had no `message_end`, so a response is never counted twice. A response read at `message_end` counts before the tools it requested run, so a turn stopped during a tool keeps the usage of the responses that finished.

- `reasoning` is a subset of `output` and is not added again.
- `cacheRead` and `cacheWrite` are part of the input pi billed. Each is added into the input total and reported as its own subset counter.
- The model of a response is `provider/model`, the form `--model` takes. The session's responses are summed per model, so a session that ran on two models reports each model's own share.
- A usage object counts as a measurement even when every count in it is zero. A response without a usage object is not a measurement.

Each response with a usage object is reported as one `token_usage` event, as soon as its `message_end` (or the `turn_end` fallback) is read. The event carries the session's running total with that response added, the per-model split of that total, and the response's model. pi reports no session total of its own, so the total is the adapter's sum of this process's responses. A turn that makes N responses reports N events, which the orchestrator counts as N API requests.

A turn is settled once pi ends its run with `agent_end` and no retry pending. A turn that served output and ends without that event, for example because it was stopped, is reported as spend not fully accounted for.

Compaction is an extra model call that the adapter does not count. The usage a session reports leaves out the summarization calls pi's auto-compaction makes.

## SSH

A remote session runs the same `pi -p --mode json ...` command on the remote host, built with the shared helper the other fork-per-turn kinds use, with the same session flag. The launch carries the variables `worker.ssh_pass_env` lists. The kind declares no credential variables of its own. Carried values and the prompt travel on the connection's standard input, so the remote host needs `dd` once a variable is carried.

## Failure detection

`pi --mode json` applies no exit-code check to a failed model response, so a response that stopped with `error` or `aborted` still exits 0. The stop reason of the last assistant response is the evidence. pi retries a failed response inside the same process, with `agent_end` carrying `willRetry` true, so only the last response decides the turn.

When the run ends with `agent_end` and no retry pending, the last response's stop reason sets the turn's outcome:

| Stop reason | Outcome |
| --- | --- |
| `stop` | Success |
| `length` | `turn_token_limit` |
| `error`, `aborted` | `turn_failed`, with the response's `errorMessage` as the message, or the stop reason when there is none |
| anything else | `turn_outcome_unknown` |

A run that ended with no assistant response at all, and a run that never reached `agent_end`, fall to the shared exit-based rows. An exit 0 turn with no assistant output and no tool activity fails as a zero-work turn.

Failure modes worth recognizing:

- **Exit 0 with a failed turn.** A rejected model name or an authentication error can end a response with stop reason `error` while the process exits 0. The turn fails as `turn_failed`.
- **Early exit.** A process that starts and exits before it wrote a readable line is reported as an early exit with its exit status and the end of its stderr, so a wrong `agent.command` argument or an option pi rejects shows pi's own usage text. A command that cannot be found at all starts no process and is reported as not found.
- **No first event.** A process that stays alive without printing a parseable line fails with `response_timeout` and the message `timed out waiting for first event`. A credential or provider prompt waiting on a terminal is the usual cause.
- **SSH connection failure.** On a remote launch, exit status 255 before the run ended is a failed connection, not a pi failure.
- **Wrong workspace or session.** The header checks above end the turn with `response_error`.

## Sortie's own tools

The kind declares `MCPInjectionUnsupported`. pi loads MCP servers from its own `mcp.json` and from extensions, and the adapter does not pass it the generated `.sortie/mcp.json`. Sortie's tools are neither advertised to nor callable by a pi session, local or remote. The servers an `mcp_config` key names are not delivered either. The preflight reports this as the warnings `agent.kind.no_tool_channel` and `agent.mcp_config`.
