## 8. Polling, Scheduling, and Reconciliation

### 8.1 Poll Loop

At startup, Sortie validates config, performs startup cleanup, schedules an immediate tick, and then repeats every `polling.interval_ms`.

The effective poll interval should be updated when workflow config changes are re-applied.

Tick sequence:

1. Run dispatch preflight validation, which triggers a defensive config reload so every later step in the tick sees the same fresh configuration snapshot.
2. Apply the reloaded configuration to runtime state, then ping the database; the result feeds the health block's database check (Section 13.3).
3. Reconcile running issues.
4. Run the periodic workspace sweep when its tick counter is due (Section 8.7).
5. Fetch candidate issues from tracker using active states.
6. Sort issues by dispatch priority.
7. Rebuild the exhausted-issue set from the session and token ceilings (§14.2).
8. Evaluate the release rule for every parked issue (§14.2): observe the tick's candidates directly for a state change or a confirmed parking label's removal, then read the tracker state of the parked issues the candidate fetch did not return, through one batched, comment-free call. A park that holds no read of its issue, because it was recorded or restored without one, takes the read of this step: the candidate's when the candidates list the issue, otherwise one single-issue read when the batched state read finds it. That read is made once per park unless it fails, in which case the next tick tries again. A park that holds a title or link and no labels, as one persisted before the type and labels were recorded does, takes the type and labels of the candidate only and keeps the title and link it holds; no tracker call is added. The labels a park stores in either case leave out the park's own parking label, because a new park's labels are read before the label is written and a later read carries the label once it is applied. Beyond the candidate fetch in step 5, the batched state read and this single-issue read are the only tracker calls this step makes; the batched read is skipped when every parked issue is already a candidate.
9. Park each candidate whose consecutive handoff-absence count has just reached the ceiling (§14.2), skipped entirely under `tracker.handoff_evidence: off`. The tick first decides which candidates to park, then reads the newest agent run of those issues from the local store in one call (made only on a tick that parks one), and parks each with that run's dispatch rule, stage lineage, agent kind, and configured model. A failed read logs one `Warn` record and parks every one of them without a run: the read never delays or prevents a park.
10. Dispatch eligible issues while slots remain. For a candidate whose tracker adapter declares the per-issue blocker source, this step may read that one candidate's blocker list, bounded by the per-pass read budget and its rotating window (§8.2); a candidate the budget or window excludes this tick is held rather than dispatched. A candidate that holds a stage hop record whose target label no read has yet shown passes the stage hop hold below after it is admitted and before its rule is selected. Once a dispatch has created the running entry, it deletes the issue's outcome (Section 4.1.8); retries and reaction continuations do not.
11. Delete the acknowledgement of every attention item the state no longer derives (§13.7.2). A tick that ends before the candidate fetch completes deletes none.
12. Notify observability/status consumers of state changes.

Stage hop hold:

- After a hop (Section 7.3), the issue holds a hop record that remembers the label the hop added and whether any read has shown it. Until a read does, the issue is dispatched on no rule other than the hop's target, because a candidate listing can lag the label write and would otherwise select the previous stage again.
- A candidate that carries the target label marks the record observed and dispatches. A candidate that lacks it is read directly through `fetch_issue_by_id`, at most once per hop per tick and only when the listing lacks the label. The tracker contract requires that read to reflect the hop's own write (Section 11.1).
- A read that shows the target label marks the record observed and dispatches the target. A read that shows a terminal state records the terminal outcome (Section 4.1.8). A read that shows a terminal state, or a named state outside the active states, resets the hop count (below) and dispatches nothing this tick. A read that shows no state holds the issue for that tick, as a failed read does, and leaves the record, its count, and its hold untouched: a healthy label edit can show an issue with no state label for a moment, so reconciliation alone resets the record, on the second consecutive no-state reading (Section 8.5). A read that shows an active issue without the target label marks the record observed, logs the release at `Info`, and routes the issue by the labels the read shows, so an issue whose label was removed or replaced by a person or by tracker automation is never left active and undispatched. A failed read logs a warning and holds the issue for that tick.
- The observed flag is persisted, so a restart does not repeat a hold that already lifted.
- A held issue the candidate listing omits is never read here; reconciliation observes it (Section 8.5).
- Stage selection receives the hop route, the latest hop's target rule and label, so that an issue carrying the target label is selected by the target before any other stage label it carries (Section 5.3.9).
- The poll tick is the only lane that dispatches an issue after a hop, because the hop leaves no retry entry and no pending reaction; the retry lane needs no hold.

Preflight runs first so the reload it forces is visible to reconciliation and to the sweep, not only to dispatch. If validation fails, dispatch is skipped for that tick, but configuration is still applied, reconciliation still runs, and the sweep still runs when due: those steps keep orchestrator state aligned with the tracker using the last known good configuration, which remains valid for that purpose. Dispatch is the only step gated on preflight success.

### 8.2 Candidate Selection Rules

An issue is dispatch-eligible only if all are true:

- It has `id`, `identifier`, `title`, and `state`.
- Its state is in `active_states` and not in `terminal_states`.
- It is not already in `running`.
- It is not already in `claimed`.
- It is not parked (§14.2).
- Global concurrency slots are available.
- Per-state concurrency slots are available.
- Blocker rule passes: for issues in any non-running active state, do not dispatch when any blocker is non-terminal. The blocker-gating states are the configured active states, not a hardcoded state name.
- Its blockers could be resolved this tick: an issue whose blocker list could not be resolved, whether because its own read failed, the read budget or rotating window excluded it, or its producer declared the list incomplete, is not dispatched. The next tick retries.

An adapter derives a blocker's state from the state lists in force when its operation starts (Section 6.2), while the orchestrator rebuilds its own terminal set from the tick's reloaded configuration (step 2 above), so the two vocabularies can disagree for the one tick that straddles a `WORKFLOW.md` edit that changes those lists. The divergence exists on the retry lane (§8.4) as well as on the poll lane's blocker resolution.

For a tracker adapter whose candidate fetch cannot carry blockers, one read per needy candidate is bounded at a fixed per-pass budget, so a fleet with more needy candidates than the budget spends no more than that many blocker reads per tick. The excluded candidates are held this tick rather than evaluated, and a rotating window walks the needy candidates across ticks so every one is read within a bounded number of ticks rather than being starved behind a permanently-held head of the list. The window advances only when the budget actually denied a needy candidate its read, which is what leaves a backlog for the next tick to step to, and resets otherwise. A tick that spends its whole budget on the last needy candidates denies nobody, so it resets too: advancing there would make the next tick skip exactly the candidates it just served. That keeps the window from drifting ahead of a shrinking backlog.

Immediately before the dispatch claims a candidate that holds a standing escalation, it applies Part B's reading of the escalation (Section 8.5) to the candidate's state. A person's release of a park can dispatch the issue on the tick that releases it, after the refresh ran, so no refresh finds the issue undriven and the escalation would outlive the run. The candidate passed the dispatch gates in an active state, which is never the handoff state, so the direct read decides. The reading changes no dispatch decision.

Sorting order (stable intent):

1. `priority` ascending (1..4 are preferred; null/unknown sorts last)
2. `created_at` oldest first
3. `identifier` lexicographic tie-breaker

### 8.3 Concurrency Control

Global limit:

- `available_slots = max(max_concurrent_agents - running_count, 0)`

Per-state limit:

- `max_concurrent_agents_by_state[state]` if present (state key normalized)
- otherwise fallback to global limit

The runtime counts issues by their current tracked state in the `running` map.

Optional SSH host limit:

- When `worker.max_concurrent_agents_per_host` is set, each configured SSH host may run at most that many concurrent agents at once.
- Hosts at that cap are skipped for new dispatch until capacity frees up.

### 8.4 Retry and Backoff

Retry entry creation:

- The caller checks the retry slot first (Section 7.5): a challenger consults the slot and schedules only when it is free. The cancellation the primitive performs internally is a backstop for a call site that skipped the check, not the primitive's own contract.
- Store `attempt`, `identifier`, `error`, `trigger` (the `sortie_retries_total` label of the schedule, empty for a reaction continuation that records none), `due_at_ms`, `session_id` (continuation retries propagate the session ID from the exiting worker; error and reaction retries leave it null), and new timer handle.
- Store `origin_trigger`, what started the retry: the schedule's `trigger` label, or on a reschedule by the retry timer the `origin_trigger` of the retry it replaces. It is persisted with the entry, so it survives a restart. The run the retry dispatches records it as its `trigger`.
- A reschedule by the retry timer records the trigger `timer` and its own reason as the error, never the error of the retry it replaces. The reasons are listed in Section 13.7.2. Every put-back keeps the `origin_trigger` of the retry it replaces, including the re-arm of an overdue retry.

Backoff formula:

- Normal continuation retries after a clean worker exit use a short fixed delay of `1000` ms.
- Failure-driven retries use `delay = min(10000 * 2^(attempt - 1), agent.max_retry_backoff_ms)`.
- Power is capped by the configured max retry backoff (default `300000` / 5m).

Retry handling behavior:

1. Fetch active candidate issues (not all issues).
2. Find the specific issue by `issue_id`.
3. If not found, release claim.
4. If found and still candidate-eligible:
   - Dispatch if slots are available, on the selection the configuration in force gives the retry (Retry selection, below).
   - Otherwise requeue with error `no available orchestrator slots`.
5. If found but no longer active, release claim. When the issue is terminal, record the terminal outcome (Section 4.1.8). When the issue is terminal or otherwise outside the active states, the issue's consecutive stage hop count resets. An issue that reads no state is the exception, and it is decided before the required-field check and before any stage hop routing: the first no-state reading reschedules the retry at the same attempt number after the backoff delay, keeping the session, continuation context, and reaction kind, and logs the deferral at `Info`; a second consecutive no-state reading releases the claim and deletes the retry entry. A reading in a configured state between the two clears the first. A no-state reading never resets the stage hop record, which reconciliation decides. The first reading waits because a released retry loses its session, continuation context, and reaction kind, and a healthy label edit can show an issue with no state label for a moment.
6. A reaction retry the issue's own current state does not permit to dispatch is rescheduled with backoff, but only for as long as it has been pausing consecutively for that reason; past 30 minutes of consecutive pausing it is dropped instead, its persisted row deleted and its claim released, with a warning naming the kind and the dwell (Section 7.5).
7. A retry entry whose timer event was never delivered is re-armed with a zero delay once its due time is more than 60 seconds in the past, so a dropped timer event self-heals within a few ticks instead of holding its slot for the process lifetime (Section 7.5).
8. An operator request (Section 13.7.2) makes a retry due now by re-arming its entry in place with a zero delay, and changes nothing outside the entry's schedule. When the fire it brings forward cannot start the retry, the put-back keeps the retry's attempt number and its due time from before the request, under the put-back's own reason, in place of the attempt number and backoff delay a timer's fire sets. The re-arm of item 7 keeps that rule.

Retry selection:

- Every frozen `(agent_kind, template_id, rule_name)` selection returns through the retry handler: rows reloaded at startup, retries queued at worker exit and by the reaction and label-command reconcilers, and reactions rebuilt from `run_history`. After the empty-kind coalescing and before the adapter lookup, the handler selects again from the configuration in force at that timer.
- Selection algorithm:

```
retry_selection(cfg, template_held, frozen, issue, hop):
  target = replacement of the conversion record in cfg whose kind is frozen.agent_kind,
           else frozen.agent_kind
  if target is among the agent kinds cfg reaches and template_held(frozen.template_id):
    return (target, frozen.template_id, frozen.rule_name)
  return resolve_rule(issue, cfg.dispatch, hop, cfg.agent.kind)
```

- `hop` is the issue's hop route, the target rule and label of its latest hop record, or empty when it holds none. A retry routed afresh therefore selects the latest hop's target under the same precedence as a poll tick.
- A retry that keeps its frozen rule renders the frozen `stage.previous` and `stage.previous_outcome` pair, also after a restart; a retry routed afresh to another rule renders the pair as a poll tick does, filled only when the selected rule is the target of the issue's latest hop and the hop count has not reset. `stage.current` is computed at each dispatch from the configuration in force.

- A frozen kind that is still reached, through `agent.kind`, `dispatch.default.agent`, or any rule, keeps its selection whether or not the rule matches the issue now. The kind launches its own command as the configuration states it at that timer.
- A selection that differs from the frozen one in kind or template clears the resume session identifier, because session identifiers are adapter-specific; the continuation context, reaction kind, attempt number, and last SSH host carry over. A changed selection is logged once at `Info` (Section 13.1).
- When the adapter for the selected kind is unavailable because its construction failed at startup, the handler logs the failure, releases the acquired host, and reschedules with the backoff of the next attempt instead of releasing the claim. The claim and the continuation stay until a restart constructs the adapter. Releasing the claim would drop a reaction that a reconciler already consumed, and no poll dispatches an issue in the handoff state.

Settings per attempt:

- The selection is frozen per claim; the settings are not. Every dispatch resolves its attempt's settings on the event loop from the one configuration snapshot its lane reads: the top-level settings block of the selection's kind with the frozen rule's block laid over it (Section 5.3.9). The resolution also yields the usage-reporting disposition the running entry freezes, the model and reasoning level the attempt was configured with, and the error-severity settings checks the block fails. A reload therefore reaches the next attempt of a held claim, and a running session never changes settings.
- An attempt whose block fails an error-severity check starts no session. The first dispatch releases the acquired host, logs an `Error` record (Section 13.1), and skips the candidate for the tick. The retry handler releases the acquired host, logs an `Error` record, and reschedules with the backoff of the next attempt under the error text `retry agent settings refused`, keeping its claim, its continuation context, and its session identifier. This gate exists because a retry or continuation never passes the per-tick preflight, so a reload could otherwise carry a block into a session that preflight refuses.
- A change in the resolved settings never clears the resume session identifier; only a changed kind or template does. The resumed attempt delivers its settings on every turn, the first included (Section 10.1).
- When the selection stands, the attempt's rule was applied to the previous attempt, and the rule no longer carries a block for the kind, the attempt runs on the top-level block and logs one `Info` record naming the rule. A runtime-only flag on the running entry, the retry entry, and the pending reaction records that the rule's block was applied; it travels with the claim beside the rule name and starts false after a restart, so a restored claim logs nothing for its first attempt.

Per-issue effort budget (defense-in-depth):

- When `agent.max_sessions > 0`, two lanes evaluate it: the retry handler counts completed sessions for the issue from `run_history` before fetching candidates, once per retry; the poll tick's rebuild runs the same count as one batch query over the whole candidate set, once per tick, before dispatch (see the rebuild bullet below).
- If the count reaches `max_sessions`, a warning is logged on both lanes. The retry handler releases the claim it holds and does not re-dispatch; the rebuild writes the candidate into the exhausted-issue set instead, because a candidate the rebuild holds never held a claim to release.
- If the retry handler's count query fails, its check is skipped (fail-open) and dispatch proceeds normally for that issue. The rebuild's own fail-open, which differs because it rebuilds a whole set rather than deciding one dispatch, is described in the rebuild bullet below.
- `max_sessions = 0` (default) disables the budget entirely.

Per-issue token budget (cost ceiling):

- When `agent.max_tokens > 0`, three lanes evaluate it: the retry handler sums `total_tokens` across the issue's `run_history` entries on the same pre-dispatch path, after the session check, once per retry; the poll tick's rebuild runs the same sum as one batch query over the whole candidate set, once per tick, after the session-count query (see the rebuild bullet below); and the event loop evaluates a run already in flight against the same ceiling as each usage figure arrives.
- If the sum reaches `max_tokens`, a warning is logged on both lanes. The retry handler releases the claim it holds and does not re-dispatch; the rebuild writes the candidate into the exhausted-issue set instead, for the reason the effort-budget bullet above states.
- The session and token ceilings are independent hard ceilings. A re-dispatch is blocked when either is reached, so whichever fills first across polling cycles is the one that fires. When a single evaluation finds both exhausted, the reported and logged reason names the token budget; the block itself is identical regardless of which ceiling triggered it.
- The per-tick rebuild of the exhausted-issue set accounts for these two ceilings only: it runs the session-count batch query when `max_sessions > 0` and the token-sum batch query when `max_tokens > 0`, and unions the results, token taking precedence over session when one issue reaches both. On the tick an issue enters the set, the rebuild emits one warning record naming the issue, the reason, and the used and budgeted numbers, and increments a counter for that reason; a tick that re-observes an issue already in the set under the same reason emits neither, so the record and the counter fire once per hold rather than once per tick. Each blocked issue's reason and numbers travel with its own entry, keyed by issue, rather than in a map alongside the set. An entry also carries the dispatch context of the dispatch the hold blocks: the rule name, the stage lineage with its pass dispatch time, the agent kind, and the configured model. An entry holds a context exactly when its agent kind is non-empty. The rebuild keeps the context of a prior entry that holds one, which includes a hold the retry lane recorded with the context of the retry it blocked. An entry still without a context takes the context of the dispatch it blocks. When the issue holds a stage hop record whose run is not yet dispatched, that dispatch is the hop's target: the rule name is the target rule, the agent kind and model come from the configuration in force, and the lineage is the one the target's dispatch inherits, so the rebuild reads no store for it. Every other entry takes the context of the issue's latest agent run, read for all of them in one batched query per tick, because with no hop waiting the latest agent run is where the issue stands. The synthetic `ci_failed` row is no agent run, and an issue with no agent run stays without a context. When the batched read fails, the rebuild logs one warning and leaves those entries without a context until the next tick reads again. On a query error for one axis, the rebuild folds the prior set's entries for that axis forward unchanged and keeps the other axis fresh, which is a different fail-open mechanism from the retry lane's own skip-and-proceed; dispatch still proceeds for every candidate not in the set. The rebuild is skipped entirely when both budgets are disabled. A hold newly entering the set, on either this rebuild or the retry lane, also produces one `budget.held` event whose body is a plain-text notice naming the ceiling and the setting that raises it. The event is delivered to the destinations subscribed to it (Section 10.4.7): the `tracker_comment` destination posts it as a comment on the tracker issue, and a Slack or webhook entry that lists `budget.held` receives it as well. The producer routes the event first, and a hold whose delivery has no destination is skipped: it spends no pacing slot, writes no durable record, and leaves no memory entry, so the issue stays un-noticed and every later pass routes it again against the configuration then in force. A configuration change that subscribes a destination to `budget.held` therefore delivers each held issue's notice on the next pass. A hold with at least one destination is durably deduplicated, so its notice is delivered once per hold across restarts rather than once per process lifetime, the durable record is written before the delivery, and its rate is paced by a wall-clock window shared by both lanes that holds at whatever `polling.interval_ms` the deployment configures. A failed delivery keeps its record.
- The consecutive handoff-absence ceiling (§14.2), governed by `agent.max_consecutive_absences`, is evaluated separately, by the same mechanism that parks a `blocked` soft stop, after the release-evaluation step below so a park released this tick is not immediately re-parked. It shares the `parked` reason vocabulary with the `blocked` park rather than the exhausted-issue set's own reasons, and is skipped entirely under `tracker.handoff_evidence: off`, which records no absence.
- If the retry handler's token query fails, its check fails open and dispatch proceeds for that issue, the same fail-open shape the session check above uses. A token sum recorded before the token columns were added reads as zero.
- A sum below the ceiling that includes at least one unmeasured run allows the dispatch and logs a warning naming the issue, the sum, the ceiling, and the unmeasured count, matching the visibility of the query-failure fail-open case above. The retry path warns on every occurrence rather than once per issue, because it runs once per retry rather than once per poll tick.
- `max_tokens = 0` (default) disables the budget entirely.
- The in-flight lane runs on the event loop rather than at dispatch: an integer comparison against the running session's own token total pre-filters every usage event so no store read runs until the pre-filter fires, and only then does a confirming read against the same sum the dispatch lanes read decide the stop. A failed confirming read still stops the run when the running session's own spend has reached the ceiling on its own, because a completed-session sum is never negative and the read is then not needed to know the ceiling is breached. Where the completed sum is what carries the issue over, a failed read fails open: the run keeps going and can pass `max_tokens` until a later read succeeds or the run ends, and the next dispatch decision prevents only the session after it. The pre-filter re-evaluates on every later usage figure. Once the sum is established, by a confirming read or by the session's own spend alone, the lane cancels the running session's context with the ceiling as the recorded cause. The run is recorded `budget_stopped`, counted, logged, and included in the next hold notice's stopped-in-flight count only when that cancellation ends it before any other cancellation and before the run ends on its own. The post-turn issue refresh is not interrupted by the ceiling. Once the ceiling has decided the stop, the worker starts no further coding turn and does not enter the self-review phase, and a self-review phase the stop cuts short ends the run as a ceiling stop.
- The token warning threshold (`agent.token_warning_percent`), when `agent.max_tokens > 0`, warns before the in-flight lane above stops a run. Its figure, in tokens, is that percentage of `agent.max_tokens`, and it is evaluated against the same live per-issue sum the ceiling reads: once at the dispatch baseline read, and again on each usage figure of a live run, ahead of the in-flight lane's own evaluation of the same figure. It never fires for a figure applied outside the in-flight lane's own gates: a figure applied while the ceiling is not enforced, a run whose worker context has ended, or a run whose stop the ceiling has already decided. At most one warning record is emitted per run, latched for the run's lifetime. The threshold changes neither a dispatch decision nor a stop decision; it only warns and, once reached, owes the event loop an immediate `session_metadata` write of the reaching figure, bypassing the write throttle described in the persistence specification, so the running session's `cost_budget` tool result reflects it without delay.

Note:

- Terminal-state workspace cleanup is handled by startup cleanup, active-run reconciliation (including terminal transitions for currently running issues), and the periodic workspace sweep (Section 8.7).
- Retry handling mainly operates on active candidates and releases claims when the issue is absent, rather than performing terminal cleanup itself.
- Within one periodic sweep pass, the terminal check always runs before the age bound (see `workspace.retention_days` in the configuration specification), and a key the terminal check removes is never re-evaluated by the age bound on that same pass. The age bound needs no answer from the tracker: it is evaluated from the workspace listing and the persistence layer alone, so it runs whether or not the tracker state read for that pass succeeded, and it still evaluates and can remove eligible workspaces on a pass where that read failed. Section 8.7 defines the full pass.

### 8.5 Active Run Reconciliation

Reconciliation runs every tick, in the order below.

An operator request (Section 13.7.2) makes a pending reaction's next check due at once. It moves the entry's `PendingRetryAt` to the time of the request when that time is later, and changes nothing else about the entry, so the next tick checks it under every rule below, as it checks any due entry. When that check keeps the reaction pending and starts before the due time the request replaced, the entry gets back that due time and its backoff count from before the request, in place of the ones the check sets. The count is zero when the check found a new pull request head.

Part A: Stall detection

- For each running issue, compute `elapsed_ms` since:
  - `last_agent_timestamp` if any event has been seen, else
  - `started_at`
- If `elapsed_ms > agent.stall_timeout_ms`, terminate the worker unconditionally, then queue a retry when the retry slot is free or defer to the incumbent already occupying it, re-emitting the cancellation warning on every stalled tick either way (Section 7.5).
- If `stall_timeout_ms <= 0`, skip stall detection entirely.

Part B: Tracker state refresh

- Fetch current issue states for the deduplicated union of all running issue IDs, every issue ID holding a pending reaction entry, and every issue ID holding a stage hop record; a hop-record issue is read on every tick, whether or not it runs and whether or not preflight passes. An issue missing from the response, a deleted one included, keeps its record and its place in the set until a read reports a terminal or inactive state or a park resets the count. Skip the call entirely when that union is empty or no tracker adapter is configured.
- For each issue in the response that holds a stage hop record: if its state is terminal, or otherwise outside the active states, delete the record and reset its consecutive hop count. A stage label that changes while the issue stays active does not reset the count. A state that is empty (the issue reads no configured state) resets the record only when the issue's next reading is also empty; a reading in a configured state in between keeps the record and clears the first reading.
- For each running issue:
  - If tracker state is terminal: terminate worker and clean workspace.
  - If tracker state is still active: update the in-memory issue snapshot.
  - If the tracker reports no state, which is an empty state and never active, terminal, or the handoff state: on the first consecutive reading, keep the worker, leave the in-memory snapshot and any retry unchanged, and log the deferred stop at `Info`; on the second consecutive reading, take the arm below. A reading in a configured state in between clears the first. A healthy label edit can show an issue with no state label for a moment, so one reading never stops a run. A run that ends before the second reading takes the exit disposition of its last configured state: an active one hands the finished work off to the handoff state.
  - If tracker state is neither active nor terminal: terminate the worker without workspace cleanup, then cancel the pending retry and delete its persisted row, unless the retry slot is occupied by an incumbent, in which case both are skipped together so the in-memory entry and the persisted row stay in agreement and the incumbent survives (Section 7.5). This population is exactly the mid-session-queued retries the design depends on protecting.
- For an issue reported terminal, whether or not it has a running worker: record the outcome of the release read, which reads the pull request of the issue's pending merge-completion entry, else of its pending auto-merge entry, once before the release and yields the merged outcome when that pull request reads merged and the terminal outcome otherwise (Section 4.1.8). Then release the issue's pending reaction entries, reaction attempt counters, pending retry, and dispatch claim, leaving `reaction_fingerprints` intact. Apart from the reading below, an issue with no running worker and no terminal state is left untouched.
- For an issue in a non-terminal state that holds a standing escalation (a pending entry of a kind that escalates, with its escalation recorded), end every standing escalation of the issue when the tracker reads it outside `tracker.handoff_state` while Sortie does not drive it. A person who moves the issue out of the handoff state, or releases its park so Sortie runs the issue again, has answered the escalation, and the next run starts the reaction with a fresh budget. The reading is bounded as follows.
  - It applies only when `tracker.handoff_state` is set. With it unset, reactions run while the issue stays active, so no state marks a person's answer.
  - It applies only while the issue has no entry in the running, retry, or claimed sets and is not parked. Every reaction continuation dispatches under the normal posture, which moves the issue to `tracker.in_progress_state`, and a park keeps that state after it releases the claim, so Sortie's own readings end nothing.
  - A listing reading ends nothing by itself. A tracker search can trail a write, so a tick inside that lag after a sibling's exit wrote the handoff state still lists Sortie's own in-progress state. The reading takes effect only once a direct read of the issue shows a non-terminal state other than the handoff state. A direct read that fails ends nothing, and is never a no-state reading.
  - A state that is empty (the issue reads no configured state) ends the escalation only at the second consecutive reading, the direct read confirming it as it does for any other state. A reading in a configured state in between clears the first.
  - Ending drops each escalated entry of the issue with its attempt counter, and a `bot-review` entry also with its reported set. The stored record keeps its subject, and entries with no standing escalation stay as they are.
- An issue missing from the response leaves the no-state readings of its running entry and its hop record as they are.
- If state refresh fails, keep workers running and try again on the next tick, and release nothing.

Part C: CI status reconciliation (when `reactions.ci_failure.provider` is configured)

- For each entry in `pending_reactions` with kind `ci`:
  - If the entry holds a triage run that has not finished, re-enqueue it ready for the next tick and continue to the next entry. The pass makes no provider call and leaves the pending attempt count untouched, because waiting on a triage run is not a fetch error.
  - Resolve the pull request's current head via `SCMAdapter.GetMergeability` before deduplicating: the fingerprint compares against this pass's live head, not a ref captured at worker exit (§11A.4, §11A.5).
  - If the call fails with `ErrSCMNotFound`: drop the entry and its attempt counter and log a warning; the watch ends.
  - If the call fails for any other reason: log a warning and re-enqueue with an exponential backoff delay derived from the poll interval and the pending attempt count.
  - If the pull request is merged, or closed without merging: drop the entry and its attempt counter and log; the watch ends. The merged check runs first, so a provider whose closed state subsumes merging still ends the watch through the merged branch.
  - If the resolved head is empty: re-enqueue with backoff, recording no head and spending no attempt.
  - Deduplicate against the resolved head: a stored fingerprint equal to the current head and already marked dispatched drops the entry for this pass, except an entry with a standing escalation, which proceeds to the status read; a stored fingerprint that differs opens a new epoch (§11A.5).
  - Call `CIStatusProvider.FetchCIStatus` with the resolved head.
  - If the call fails: log a warning, re-enqueue with an exponential backoff delay derived from the poll interval and the pending attempt count, and continue to the next entry.
  - If status is `passing`: clear reaction attempts for the issue and kind, end a standing escalation, and keep watching; the entry re-enqueues rather than retiring, so a later commit's failure is still observed.
  - If status is `pending`: re-enqueue with the same exponential backoff as the fetch-error path.
  - If status is `failing` on an entry with a standing escalation: re-enqueue with backoff and dispatch nothing, unless a reload left the ceiling no longer counting the fixes as spent, which ends the escalation and handles the failure as below (§11A.6). The watch window (§11A.9) never drops such an entry.
  - If status is `failing`: consult the retry slot before handling as a CI failure; a non-nil incumbent defers instead of dispatching (Section 7.5, Section 7.3 "CI Status Failing").
  - If status is `failing` and the retry slot is free, run the triage gate before handling the failure, above the run-history append so a resuming pass cannot append a second row. `dispatch-agent` falls through to the existing CI-failure handling unchanged; `handled` marks the fingerprint dispatched and re-enqueues with the delay the already-dispatched branch applies, spending no attempt; `escalate` marks the fingerprint dispatched and applies the configured escalation with the un-incremented attempt count. The gate is inert when `reactions.ci_failure.triage` is absent.
  - Every branch that does not end the watch re-enqueues the entry.

Part D: Review comment reconciliation (when `reactions.review_comments` is configured)

- Skip entirely when no SCM adapter is configured. Whatever reaction builds the SCM adapter, a `review` entry exists only while `reactions.review_comments` is configured: worker exit and startup recovery seed one only then.
- For each entry in `pending_reactions` with kind `review`:
  - Remove entry from the map (prevents reprocessing within the same tick).
  - Check the configured watch window: if `reactions.review_comments.watch_window_ms` is positive and the entry's age, counted from its creation or its last retry-slot deferral, exceeds it, log a WARN record and drop the entry (no re-enqueue). The drop deletes the entry's `reaction_attempts` counter only while the counter is below `max_continuation_turns`; at or above it, the counter is kept. The drop discards the in-memory copy of the handed-off comment set at any counter value, and the persisted set is kept. The window never drops an entry with a standing escalation. Default `1800000` (thirty minutes); `0` removes the bound.
  - Respect `PendingRetryAt` poll throttle: if not yet due, re-enqueue and continue.
  - If the entry holds a triage run that has not finished, re-enqueue it ready for the next tick and continue to the next entry, making no provider call and leaving the pending attempt count untouched.
  - An entry with a standing escalation takes the standing pass (Section 11B.4) after the fetch and filtering below: it dispatches nothing, applies no action, and ends only on its ending facts.
  - Call `SCMAdapter.FetchPendingReviews` with the PR number, owner, and repo from `ReviewReactionData`.
  - If the call fails: increment backoff counter, set `PendingRetryAt` with exponential backoff, re-enqueue, and continue.
  - Filter out outdated comments, then drop any surviving comment whose author matches `reactions.bot_review.bot_usernames` (Section 11D.2's allowlist arm, applied here rather than inside the adapter). Compute max timestamp over the surviving set for debounce gating; an excluded comment does not raise `LastEventAt`.
  - If no actionable comments: re-enqueue with poll interval delay.
  - Build fingerprint from the sorted IDs of the surviving comment set (SHA-256 hash).
  - Check `reaction_fingerprints` table: if the fingerprint matches and is marked dispatched, re-enqueue with the poll interval delay and continue.
  - Check the surviving set against the handed-off comment set, also when the fingerprint read failed: a comment is new when no run of the issue was given it and no running turn carries it. When no comment is new, re-enqueue with the poll interval delay and continue, marking the fingerprint dispatched when the handed-off set alone covers the set. The check leaves the entry's creation time, the counter, and the pending attempt count untouched. When the handed-off set cannot be loaded, the entry waits one poll interval and nothing is dispatched, escalated, or marked (Section 11B.4).
  - If within debounce window (`now - LastEventAt < debounce_ms`): defer and re-enqueue.
  - Otherwise: consult the retry slot (Section 7.5). A non-nil incumbent means the pass defers, re-enqueuing the entry rather than dispatching. The deferral restarts the watch window and keeps the entry's creation time. On a free slot, check the continuation turn budget (`reactions.review_comments.max_continuation_turns`): when `reaction_attempts` has reached it, escalate (Section 11B.4), keeping the entry pending with the escalation recorded, since the check has already settled every set without a new comment. Below the budget, a free slot schedules a review-fix dispatch with review comment context and increments `reaction_attempts`. A run adds to the handed-off set the comment IDs its first prompt presented, when it exits normally. The fingerprint is marked dispatched later, when the scheduled retry fires and dispatch succeeds, not during this pass.
  - With the slot free and the budget not spent, the triage gate runs before the dispatch counter is incremented, so no pass that dispatches nothing is counted as one. `dispatch-agent` proceeds to the dispatch above; `handled` marks the fingerprint dispatched and re-enqueues on the poll interval without touching `reaction_attempts` or the dispatch counter; `escalate` marks the fingerprint dispatched and escalates (Section 11B.4) with the un-incremented turn count. The gate is inert when `reactions.review_comments.triage` is absent.

Part E: Bot review comment reconciliation (when `reactions.bot_review` is configured)

- Skip entirely when no SCM adapter is configured.
- Mirrors Part D's reconcile loop for the `bot-review` kind, but with no debounce gate and no `LastEventAt` tracking, and it calls `SCMAdapter.FetchBotReviewComments` (allowlisted bot authors) instead of `FetchPendingReviews`.
- Dispatches immediately on a confirmed comment set, with no debounce window, because bot comments arrive in bulk on push rather than trickling in from a human reviewer.
- Drops an entry past its watch window with Part D's rule: the `reaction_attempts` counter and the reported set (below) are deleted only while the counter is below `max_continuation_turns`, and the cached handed-off set is discarded at any counter value. The window never drops an entry with a standing escalation, which takes Part D's standing pass over the bot comments and ends under Section 11D.5.
- Applies Part D's handed-off check after the fingerprint check, over the handed-off set and the reported set, with the same outcomes (Section 11D.3).
- Consults the retry slot before dispatching, exactly as Part D: a non-nil incumbent defers instead (Section 7.5).
- On a free slot, checks the continuation turn budget (`reactions.bot_review.max_continuation_turns`) as Part D does, with one difference: when the budget is spent, only a new inline comment (one with a non-empty file path) escalates. A PR-level item never escalates alone. Every surviving comment ID is added to the runtime-only reported set whether the pass escalates or re-enqueues, so the next pass settles it. Below the budget, a PR-level item that is new still dispatches.
- Waits and skips exactly as Part D when the entry holds an unfinished triage run, and runs the triage gate after slot arbitration and the budget check and before the dispatch counter is incremented, with the same three dispositions. The gate is inert when `reactions.bot_review.triage` is absent.
- See Section 11D for the full contract.

Part F: Merge conflict detection (when `reactions.merge_conflicts` is configured)

- Skip entirely when no SCM adapter is configured.
- For each `pending_reactions` entry with kind `merge-conflict`, call `SCMAdapter.GetMergeability` on every due tick (no retry-budget check gates the read). A dirty result runs the escalation-bearing conflict-resolution path; any other result closes the episode and clears its fingerprint and attempt counter.
- The dirty branch consults the retry slot before its other guards (Section 7.5); a non-nil incumbent defers the whole branch, re-enqueuing the entry unchanged.
- An entry holding an unfinished triage run is re-enqueued ready for the next tick before the mergeability read, so the pass makes no provider call and leaves the pending attempt count untouched.
- Inside the dirty branch the triage gate runs after the head-SHA dedup check and above the head-change block, so that block runs once per conflicting head rather than once per pass. `dispatch-agent` falls through to the existing attribution, increment, and dispatch-or-escalate sequence; `handled` marks the fingerprint dispatched and re-enqueues on the poll interval without touching the per-episode counter; `escalate` marks the fingerprint dispatched and applies the configured escalation with the un-incremented counter, which is the previous episode's count when the head is new. The gate is inert when `reactions.merge_conflicts.triage` is absent.
- Closing the episode clears the fingerprint an in-flight triage run was started for, so that run is cancelled on the same pass.
- An entry with a standing escalation is never dropped by the watch window. A dirty read whose head fingerprint equals the escalation's subject dispatches nothing, a read that is not dirty closes the episode and ends the escalation, and a dirty read of a different head ends it and goes on as a new head (Section 11E.5).
- See Section 11E for the full contract.

Part G: Auto-merge reconciliation (when `reactions.auto_merge` is configured)

Activation gate: `reactions.auto_merge.provider` non-empty AND `state.AutoMergePreflightFailed == false`. An entry with a standing escalation is processed even after the preflight failed, because items 2 and 3 spare it.

If `state.AutoMergePreflightRetryDueAt` is non-zero and `now >= AutoMergePreflightRetryDueAt`, run the scope-verification preflight first and clear or re-arm the flags before processing entries.

For each entry in `pending_reactions` with kind `merge`:

1. Remove the entry from the `pending_reactions` map.
2. Drop with a WARN log when `state.AutoMergePreflightFailed == true`, unless the entry holds a standing escalation.
3. Drop when the entry's age, measured from its creation, exceeds the configured `reactions.auto_merge.watch_window_ms` (default `1800000`, thirty minutes; `0` removes the bound), unless the entry holds a standing escalation.
4. Re-enqueue when `now < pending.PendingRetryAt`.
5. Escalate when `MaxRetries > 0` and `reaction_attempts[issue_id:merge] >= MaxRetries`; a configured `MaxRetries` of `0` disables count-based escalation instead of firing it immediately (Section 11C.6). An entry with a standing escalation makes no merge call and applies no action; it re-enqueues after item 6 unless a read ends the escalation (Section 11C.6).
6. Fetch mergeability via `SCMAdapter.GetMergeability`. When the read reports the pull request already merged, whatever its mergeability, record the merged outcome with the `merge` path (Section 4.1.8) and drop the entry: delete the issue's `merge` attempt counter and its `merge` fingerprint, log at INFO, and stop. The drop emits no event, deletes no branch, changes no metric, and leaves the entries, counters, and fingerprints of every other kind alone. Otherwise re-enqueue on transport error or `MergeabilityUnknown`, and re-enqueue with poll interval on `Draft`, `Dirty`, or `Blocked`.
7. Fetch review decision via `SCMAdapter.GetReviewDecision`; re-enqueue when not `APPROVED` or `NOT_REQUIRED`.
8. When `require_ci == true`, fetch CI status via `SCMAdapter.GetCIStatus`; re-enqueue when not `success`.
9. Compute the fingerprint from `(head_sha, review_decision)`; upsert into `reaction_fingerprints` with `kind = "merge"`. Skip when the stored fingerprint matches and is marked dispatched.
10. Call `SCMAdapter.MergePR` with the configured strategy and the observed head SHA; increment `reaction_attempts[issue_id:merge]`.
11. On success: record the merged outcome with the `merge` path (Section 4.1.8); emit the `auto_merge.merged` event to its subscribers; delete the branch when `delete_branch == true`; clear the fingerprint; remove the pending entry; increment `sortie_reactions_auto_merge_total{result="merged"}`.

Error dispositions:

- On `ErrSCMConflict` ("already merged"): treat as idempotent success; same actions as merge succeeded, including recording the merged outcome and incrementing `sortie_reactions_auto_merge_total{result="merged"}`.
- On `ErrSCMConflict` (head SHA mismatch or branch-protection refusal): re-enqueue with poll interval; the next tick observes the new SHA via a refreshed fingerprint.
- On `ErrSCMAuth` on the merge call: escalate immediately with trigger `refused`; the entry stays pending.
- On `ErrSCMPayload` on the merge call: escalate immediately with trigger `refused`, the same disposition as `ErrSCMAuth`.
- On other transient errors: re-enqueue with backoff; escalate per the guard in item 5.

Cross-kind isolation: the success and escalation paths, and the drop of an entry whose pull request reads merged (item 6), MUST scope cleanup to `kind = "merge"` only. They MUST NOT mutate `ci` or `review` reaction state (full invariant in §11C.10).

Part G runs after Parts C through F (CI, review, bot review, and merge conflict) so its precondition reads observe the most current per-kind state. It runs before the label-command passes (Parts H and I); that relative order does not affect correctness because those passes are fully cross-kind isolated (Section 11F.4).

Part H: Label review command detection (when `reactions.label_commands.review_label` is configured)

- Skip entirely when no SCM adapter is configured or the `label_commands` block is absent.
- For each Sortie-managed PR, read the label-event journal past its stored high-water mark via `SCMAdapter.ListLabelEvents`. A confirmed `review_label` command is decided in this order: a foreign-kind incumbent occupying the retry slot, or a running worker of another kind, defers (Section 7.5), leaving the label and the high-water mark untouched and the entry due for the first pass that finds neither; a same-kind incumbent or a running worker of this kind collapses, advancing the mark without a second dispatch; otherwise the pass dispatches a read-only, no-clone review session and removes the label.
- Ordering relative to the other parts does not affect correctness: the pass is fully cross-kind isolated. See Section 11F for the full contract.

Part I: Label fix command detection (when `reactions.label_commands.fix_label` is configured)

- Structurally identical to Part H, scoped to the `fix_label` command and the `label-fix` reaction kind, including the same three-way retry-slot decision (Section 7.5).
- A confirmed command dispatches a read-write, clone-and-checkout fix session instead of the read-only review session.
- Ordering relative to the other parts does not affect correctness: the pass is fully cross-kind isolated. See Section 11F for the full contract.

Part J: Merge completion reconciliation (when `reactions.merge_completion` is configured)

- Skip entirely when no SCM adapter or tracker adapter is configured.
- Before examining any entry, check once whether the configured `target_state` is still a member of the runtime terminal-state list; log one warning per onset of drift, suppressed while the condition persists.
- For each due entry in `pending_reactions` with kind `merge-completion`, fetch tracker state for the linked issues in one batched read.
- Drop an entry whose issue is missing from the response or already terminal; for an entry with a standing escalation, a missing issue is dropped only once the direct read of the issue confirms it (Part B). For an already-terminal issue, the pass first reads the entry's pull request and records the merged outcome when it reads merged and the terminal outcome otherwise (Section 4.1.8). Defer an entry whose issue is currently claimed. Stop an entry whose issue has left the configured handoff state; an entry with a standing escalation stops only once the direct read of the issue confirms the exit (Part B).
- Fetch mergeability via `SCMAdapter.GetMergeability`; re-enqueue with backoff on a transport error. While the pull request is not yet reported merged, re-enqueue on the poll interval without starting the missing-identifier clock. A merged response records the merged outcome with the `merge-completion` path before the identifier check, so a merge reported without a commit identifier records too, and a later merged response keeps the first time (Section 4.1.8). On the first merged response with no commit identifier, persist a thirty-minute first-seen observation and use exponential pending backoff, floored at the poll interval. At or after the deadline, begin a `missing_merge_commit` escalation: attempt the configured escalation, mark it delivered only after the tracker write succeeds, and keep the entry pending, re-enqueued on the poll interval, so it observes the forge reporting the commit identifier. A failed write leaves the observation undelivered, and the first pass after a restart, or a fresh entry, retries delivery once without restarting the grace period.
- Upsert the merge commit identifier into `reaction_fingerprints` under kind `merge-completion`; skip an entry already latched to that identifier.
- Call `TrackerAdapter.TransitionIssue` to the configured `target_state`. Route a failure by the tracker error taxonomy: retry with backoff up to `max_retries` then escalate for transport and API failures, escalate immediately for auth and payload failures, and stop without escalating for a not-found issue. An escalation keeps the entry pending and makes no further transition call until it ends; a restart resumes one call (Section 11G.6).
- Unlike the CI, review, bot-review, merge-conflict, and auto-merge kinds in this list, a pending entry of this kind carries no general expiry, the same posture as the label-review and label-fix kinds (Parts H and I): a merge can wait on human review for days. Only the post-merge missing-identifier condition has the fixed thirty-minute bound; the entry is also bounded by the issue leaving the handoff state and by the pending-reaction recovery lookback.
- Placed last so a merge the orchestrator performed earlier in the same tick, in Part G, is observed on this same pass.
- See §11G for the full contract.

### 8.6 Startup Terminal Workspace Cleanup

When Sortie starts:

1. Enumerate workspace directories on disk.
2. Map directory names back to issue identifiers.
3. Query the tracker for the states of those specific issues.
4. For each issue in a terminal state, remove the corresponding workspace directory.
5. If the terminal-issues fetch fails, log a warning and continue startup.

This approach scopes the query to workspaces that actually exist on disk, avoiding expensive full-project terminal issue sweeps for large trackers.

### 8.7 Periodic Workspace Sweep

Startup cleanup and active-run reconciliation between them cover an issue that is terminal when the process starts and an issue that turns terminal while a worker is running. Neither covers an issue whose worker has already exited: it holds no running entry, so nothing observes it turning terminal and nothing observes its workspace aging on disk. The periodic sweep closes that gap.

The sweep runs on the poll tick, throttled to one pass every sixty ticks rather than every tick, because its cost scales with the number of leftover directories rather than with the number of running agents. Its work is housekeeping: a workspace left behind wastes disk, but it never produces a wrong scheduling decision, so bounded tracker load is worth more than immediate removal.

One pass proceeds as follows:

```text
sweep_workspaces(state, config):
  keys = list_workspace_keys(config.workspace.root)
  if listing failed:
    log_warning()
    return                              # no summary record for this pass

  exclusions = {}                       # workspace key -> reason, first writer wins
  for entry in state.running:
    set_if_absent(exclusions, key_of(entry), "running")
  for entry in state.retry_attempts:
    set_if_absent(exclusions, key_of(entry), "retry")
  for entry in state.pending_reactions:
    if reaction_kind_pins_workspace(entry.kind):
      set_if_absent(exclusions, key_of(entry), "reaction")

  remaining = keys - exclusions.keys
  states = tracker.fetch_issue_states_by_identifiers(remaining)
  if fetch failed:
    states = {}                         # every key falls through to the age bound

  terminal_keys = [k for k in remaining if states[k] is known and terminal]
  age_keys      = remaining - terminal_keys

  remove_workspaces(terminal_keys)      # the terminal gate
  run_age_bound(age_keys, config.workspace.retention_days)
  emit_sweep_summary()
```

Rules that govern the pass:

- The candidate set for a pass is every workspace key the listing returned. A key is excluded from evaluation before any tracker call when it belongs to a running entry, a scheduled retry, or a pending reaction entry whose kind pins its workspace. Exclusion precedence is running, then retry, then reaction, so a key present in more than one source is attributed to exactly one reason and the pass's outcome counters partition the candidate set (Section 13.1).
- A pending reaction entry pins its workspace only when its kind carries an expiry. A kind that polls an external fact indefinitely (§11F and §11G) does not pin, because pinning it would hold a workspace on disk for as long as that unbounded poll runs. A kind the orchestrator does not recognize pins, the non-destructive default.
- The terminal gate removes every evaluated key the tracker reports in a terminal state. A key whose state the tracker does not report on is not removed by this gate.
- The age bound then evaluates whatever the terminal gate left, under the rules in Section 9.1. A failed tracker read leaves every evaluated key unclassified, so all of them reach the age bound rather than being skipped.
- Each pass that produced a candidate set emits exactly one summary record, including a pass over zero keys and a pass that removed nothing (Section 13.1). A pass that could not list the workspace root emits no summary and removes nothing.
