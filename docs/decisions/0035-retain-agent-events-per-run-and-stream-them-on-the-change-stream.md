---
status: accepted
date: 2026-10-15
decision-makers: Serghei Iakovlev
---

# Retain Agent Events per Run and Stream Them on the Change Stream

## Context and Problem Statement

Runtime state kept only the latest event of a running session: its type, its masked message, and its timestamp. The `recent_events` field of the issue detail response was always an empty list. An operator could see neither what an agent was doing while it ran nor what had happened before they looked, and a restart erased even the latest event of a finished run.

The dashboard holds one event stream per browser, multiplexes the views it shows onto that stream, and resumes from a cursor carried in the event `id`. The HTTP server is unauthenticated; operators place it behind an authenticating proxy or a VPN. Agent messages pass through the same secret masking as the latest message did. Persistence is one SQLite database reached through a single connection, and run exits are recorded on the orchestrator's event loop.

This record settles what unit events are retained and streamed by, how much is kept and for how long, which events and fields are kept, how a reconnecting client resumes, how a quiet run is indicated, whether per-session log files are exposed, and how a slow consumer is kept from delaying the event loop.

## Decision Drivers

1. **The event loop never waits on a client.** Agent events arrive on the orchestrator's single event loop. No stream, however slow, may delay that loop or the agent events behind it.
2. **Bounded memory and disk.** Both tiers must be bounded in a way an operator can reason about. Agents emit events at very different rates, and a bound that holds for one agent kind must hold for all of them.
3. **No silent loss.** When events are gone, because of eviction, retention, a crash, or a client that fell behind, the client is told so explicitly. A view that shows a gap as continuity is worse than one that shows nothing.
4. **One connection per browser.** Over HTTP/1.1 a browser allows six connections per origin across all tabs. Live events ride the existing change stream; a second route is not acceptable.
5. **Durability equals the run's own record.** Retained events must survive whatever the run's history row survives, and need not survive more.
6. **Go owns the meaning.** Operator-facing sentences and thresholds are computed on the server. The client compares times and renders text it receives.
7. **The normalized vocabulary is the contract.** Only normalized agent events and their fields reach the API. Raw runtime output and host paths do not.

## Considered Options

Unit of retention and streaming:

- One run (one dispatch), keyed by dispatch ID
- One agent session, keyed by session ID
- One issue
- One process-wide stream of every run
- A separate per-session streaming route

Retention:

- Two tiers: a bounded memory buffer per live run, written to SQLite once at worker exit, with age and size limits
- Memory only
- A SQLite write per event
- Persisting streaming text deltas
- A bound by age only, or by count only
- Unbounded retention

Reconnect after a dropped stream:

- Resume from a per-run cursor carried in the event `id`, with an explicit in-stream reset
- Send only new events
- Send a full snapshot
- Answer an expired cursor with HTTP 410
- Skip ahead silently
- Client refetches, then resubscribes

Stall indication:

- Time since the last event, compared with the operator's stall timeout
- An adaptive threshold derived from the run's usual gap between events
- A server heartbeat
- A phase indicator derived from message text

Slow consumers:

- Copy-on-wake from the run's buffer behind a capacity-1 flag, closing a stream that misses its write deadline
- A per-subscriber queue that drops and continues
- A blocking send
- Read history, then subscribe
- Unbounded per-subscriber queues

## Decision Outcome

Chosen option: **per-run retention keyed by dispatch ID, in two bounded tiers, streamed on the existing change stream with per-run cursors and explicit resets, a stall indication derived from the operator's stall timeout, and copy-on-wake delivery that never blocks the event loop**, because it is the only combination that keeps the event loop wait-free, bounds memory and disk, reports every loss, and adds no connection.

### Unit: one run

Events are retained and streamed per run, where a run is one dispatch. The dispatch ID is minted per dispatch and never reused, so it also acts as an epoch: a cursor from before a restart can never match a different run. Each run numbers its retained events with `seq`, starting at 1 and rising by one per retained event; eviction never renumbers.

`run_history` gains a `dispatch_id` column, and both running rows and history rows expose `dispatch_id`, so a client can name the run it shows.

### Retention: two tiers

- **Live tier.** Each running run keeps its newest 1,000 events in memory, and at most 256 KiB measured as the sum of encoded events, whichever limit is reached first. The oldest events are evicted; `seq` keeps rising.
- **Durable tier.** At worker exit the buffer is written to SQLite once, in the same transaction as the run's `run_history` row, and the memory buffer is then released. A failed write is logged at Warn and the events are dropped, with no retry.
- **Limits.** A finished run's events stay readable for at least 7 days after the run ends. Total stored size is capped at 64 MiB, pruned oldest first. Pruning runs on each write, so 7 days is a minimum, not an expiry.
- **Explicit absence.** A run whose events were pruned, lost in a crash, or recorded before the schema change answers "not retained" explicitly. A run that kept only its tail exposes `first_seq` greater than 1, so the client can say that earlier events are gone.

Durability matches the run's history row: a graceful shutdown keeps both, and a crash loses both.

### Events and fields

Every normalized event type is retained except:

- `notification` and `other_message` events with an empty message, which are liveness pings;
- `token_usage`, whose totals attach to turn-ending events instead;
- a `notification`, `other_message`, or `malformed` event identical in type and message to the newest retained event.

Events that are not retained still advance `last_event_at`.

A retained event carries `seq`, `at`, `event`, and `message`. The message is masked when stored and again when served, and is capped at 1,000 runes, masked before it is cut. `session_started` adds `session_id`. `tool_result` adds `tool_name`, `duration_ms`, and `tool_error`. The turn-ending types (`turn_completed`, `turn_failed`, `turn_cancelled`, `turn_ended_with_error`, `turn_input_required`) add `usage`: the run's totals after that event, `null` when usage is unmeasured or does not arrive, never zero. `text`, a human sentence describing the event, is rendered by the server when the event is served and is never stored. The agent process ID, rate limits, model, and API duration are not retained: the process ID and model say nothing about the run's progress, rate limits are opaque and their latest value is already in the snapshot, and API duration is an aggregate the run already reports.

The issue detail route's `recent_events` returns the newest 50 events of the running run, else of the newest run, else an empty list. That route is refetched up to once per second, so it carries 50 events, not the full buffer. The full list is served by a run-events route, which answers 404 for a run whose events are not retained.

### Reconnect: resume from cursor

A client subscribes on the change-stream route by query: `run=<dispatch_id>` or `run=<dispatch_id>:<seq>`, repeatable, at most 16 per connection; more is answered with 400. Without `seq`, every retained event is replayed; with `seq`, replay starts after it. The cursor is a query parameter because `EventSource` cannot set headers on its first connection.

Every frame `id` is the change revision followed by `;<dispatch_id>:<last seq sent>` for each subscribed run. On reconnect, `Last-Event-ID` overrides the query cursors. The revision part keeps the change stream's exact-equality rule.

Frames:

- `agent_event`: the run's dispatch ID and the event's fields.
- `run_ended`: sent after a run's last event; the subscription is dropped.
- `reset` with reason `cursor_expired`: the cursor fell behind retention; it is followed in the same stream by every retained event of that run.
- `reset` with reason `not_retained`: the run has no retained events; the subscription ends.

Change frames are unchanged: payload-free, coalesced, at most one per second. Agent-event frames are never coalesced and are sent only for subscribed runs.

### Stall indication

Running rows carry `last_event_at` (the time of any event, liveness pings included; `null` before the first event), `quiet_at`, and `stall_at`. The reference point is `last_event_at`, else `started_at`, the same reference the orchestrator's own stall stop uses. `quiet_at` is the reference plus half the stall timeout, and `stall_at` is the reference plus the stall timeout. With stall detection off (a timeout of zero or less), both are `null`.

The client shows a neutral marker once its clock, corrected by the response's `generated_at`, reaches `quiet_at`. It uses a local timer, because silence produces no signal to react to. The marker reads "no events for <silence>; Sortie stops it after <timeout>", the stall threshold after which the next poll tick stops the run. With detection off, there is no marker, only the time of the last event. The rule lives on the server; the client only compares times.

Gaps between events in 6,711 unattended agent transcripts have a per-session median of one to two seconds, so a threshold of a multiple of the usual gap with a floor reduces to the floor in practice, and thresholds adapted to a run's history flag the same share of runs as a fixed one. Only the absolute level matters, so it follows the operator's own setting. Half the timeout leaves two and a half minutes of warning at the default five-minute timeout.

### No per-session log files

The API exposes no per-session log file paths, and no `agent_session_logs` object exists. Sortie writes no per-session log files; a host path on an unauthenticated API discloses the host's layout and invites a file-serving surface; and raw runtime transcripts bypass the normalized vocabulary.

### Slow consumers never delay the event loop

The event loop appends to the run's buffer under the buffer's own lock, never held across I/O, then wakes subscribers through a capacity-1 flag with a non-blocking send, the mechanism the change feed already uses.

Each stream goroutine registers its flag, copies the events after its cursor (from memory, or from the stored copy when the run has ended), writes them as one batch under the existing two-second per-frame write deadline, waits on its flag, and repeats. A stream that fell behind retention receives a reset. A stream that misses its write deadline is closed, and the client reconnects with its cursor. There is no per-subscriber queue.

Registering before the first read, combined with cursor-based reads, removes the race between subscribing and replaying. At worker exit the stored copy is written before memory is released, so a stream switching from memory to the stored copy finds the events in one place or the other.

### Relation to earlier decisions

This record fills in the session-event stream that `0034-build-the-dashboard-as-an-embedded-react-application.md` requires (one stream per browser, cursors carried in the event `id`, an explicit reset): the sequence number is per run, the retained events after a reset are replayed in the same stream, and the heartbeat carries the full event `id` with every run cursor.

### Considered Options in Detail

| Option | Why not chosen |
|---|---|
| Per session ID | A resumed session merges several runs under one ID, and a run has no session ID before `session_started`, so its earliest events would have no key. |
| Per issue | Interleaves the events of successive runs into one sequence. |
| One process-wide stream | Sends every run's payload to every client, whether or not it shows that run. |
| A separate per-session route | A second connection per view; a few views exhaust the browser's per-origin connection budget. |
| Memory only | A restart loses the events of finished runs, the loss that persisting to SQLite exists to prevent. |
| A SQLite write per event | The database is reached through a single connection and run records are written on the event loop, so every event would contend with every other write and could delay the loop. |
| Persisting streaming text deltas | Deltas outnumber completed messages by orders of magnitude, so stored size would scale with output length instead of with the agent's actions, and they would evict real content from the bounded buffer. |
| Age-only or count-only bound | Neither bounds disk: an age bound admits any rate, and a count bound admits any event size. |
| Unbounded retention | Disk grows without limit. |
| Storing liveness pings | They evict real content from the bounded buffer and carry nothing but a timestamp, which `last_event_at` already records. |
| Full message text | One large message would consume most of the buffer's byte bound. |
| 200 events on the detail route | The route is refetched up to once per second; the full list belongs on its own route. |
| New events only on reconnect | Events emitted while the client was away are lost without notice. |
| Full snapshot on reconnect | Resends everything the client already holds on every reconnect. |
| HTTP 410 for an expired cursor | `EventSource` closes for good on any non-200 response, so the browser would stop reconnecting. |
| Silent skip-ahead | The client never learns that events are missing. |
| Client refetch, then resubscribe | A second step that races the stream: events between the fetch and the subscription are lost or duplicated. |
| Adaptive "usual gap" threshold | With a one-to-two-second median gap it is inert, and its marker would appear to say something about the run that it does not. |
| Heartbeat | Proves only that the connection is open; agents are silent while they think. |
| Phase indicator | Would parse message text, which the normalized vocabulary does not define. |
| Per-subscriber queue with drop-and-continue | Leaves silent gaps. |
| Blocking send | One stuck client stalls the publisher and the event loop behind it. |
| Read history, then subscribe | Events appended between the read and the subscription are lost. |
| Unbounded per-subscriber queues | A client that stops reading grows memory without limit. |

## Consequences

### Positive

- An operator sees what an agent is doing as it happens and, for 7 days, what a finished run did, across restarts that were graceful.
- Every loss is visible: eviction through `first_seq`, retention and crashes through "not retained", a lagging client through `reset`.
- A slow or stuck client can delay neither the event loop nor any other client.
- The browser keeps one connection, however many runs it shows.
- A quiet run is flagged against the operator's own stall timeout, by the same reference point the orchestrator uses to stop it.

### Negative

- **A crash loses the live tier.** Events of runs that were running when the process died are gone, along with their history rows.
- **A failed write at exit drops that run's events** with only a Warn log.
- **Events dropped before retention are invisible.** When the orchestrator's inbound agent-event channel is full, an event is dropped before it reaches the buffer, and no `seq` gap marks it.
- **The sizes are judgments.** 1,000 events, 256 KiB, 50 events on the detail route, and 16 subscriptions per connection are sizing choices, not measurements.
- **The stall evidence comes from one dataset** of agent transcripts, not from Sortie's own runs.
- **An agent that emits one notification per text chunk** fills the buffer with fragments until it emits empty liveness notifications per chunk and one text notification per completed message.
- **The change stream's frame `id` grows** with the number of subscribed runs, and the stream route gains query parsing, new frame types, and a replay path.
