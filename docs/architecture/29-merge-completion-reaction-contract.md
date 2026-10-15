## 11G. Merge Completion Reaction Contract

The auto-merge contract in §11C and the merge-conflict contract in §11E both observe pull request merge state to decide whether to act on the pull request itself. This section defines a third observer of the same state: the `merge-completion` reaction kind, which transitions the linked tracker issue to a terminal state once its managed pull request merges, independently of who or what performed the merge. Unlike its two siblings it never calls an SCM write method and never dispatches an agent continuation; its only side effect is the tracker transition. See ADR-0017 for the decision record.

### 11G.1 Trigger and seeding

A pending `merge-completion` entry is created on normal worker exit when the SCM adapter is configured, the reaction is configured, the exiting worker produced a non-empty workspace path, and the workspace's `SCMMetadata` (§9.5) reports `pr_number > 0` and non-empty `owner` and `repo`. Seeding additionally requires that the issue was claimed by the orchestrator at the moment of exit, that either the exit followed the handoff-transition path or the issue remains claimed afterward, and that the exit did not make a stage hop (§7.3), which seeds no reaction of any kind. An exit whose handoff was suppressed because the issue was already reported terminal seeds nothing, this kind included: that exit performs no handoff and enqueues no reaction of any kind (§7.3). Unlike the checkout-bearing sibling kinds, no `branch` field is required: the reaction performs no checkout and reads no branch. A "create only if absent" guard preserves an in-progress pending entry across a re-exit on the same issue.

Startup recovery re-seeds a `merge-completion` pending entry from persisted run history for an issue that satisfies the same metadata predicate, is currently sitting in the configured handoff state, and whose most recent recorded activity falls inside the shared thirty-day recovery lookback. Recovery is gated on the reaction being configured, matching the pattern the auto-merge, bot-review, merge-conflict, label-review, and label-fix kinds already follow; the review kind is the one exception, gated only on the SCM adapter being present rather than on a dedicated configured flag. Recovery also caps the number of candidate issues considered per startup pass at two hundred; an issue beyond that cap is silently skipped and its pending entry is not rebuilt until a subsequent worker exit or a later startup recovers it.

### 11G.2 Reconcile loop integration

The merge-completion reconcile pass runs as Part J of active run reconciliation, last in the tick order, after the label-command passes (§8.5 Part J summarizes where this pass sits in the tick; this section is the full contract). Placing it last means a merge the orchestrator performed earlier in the same tick, in Part G, is observed on this same pass rather than deferred to the next one. The pass is skipped entirely when no SCM adapter or no tracker adapter is configured, or when the reaction itself is not configured.

The pass fetches tracker state for every due entry's issue in one batched call per tick, not one call per issue. A due entry that is not yet ready to defer, stop, or transition proceeds to a `GetMergeability` read; a not-found result on that read drops the entry (the pull request is gone), while any other read failure re-enqueues with backoff. The poll interval defaults to sixty seconds and configuration rejects any value below thirty seconds. The exponential backoff applied on a tracker state-read failure, a fingerprint upsert or read failure, a mergeability-read failure, or a merged response whose commit identifier is still missing floors at that poll interval, not at a fixed constant.

A `GetMergeability` read that reports the pull request merged records the merged outcome with the `merge-completion` path (Section 4.1.8), with or without a merge commit identifier and independent of the idempotency latch (§11G.4), so a merge reported without an identifier records too and a repeated merged read keeps the first time. An entry whose issue the batched state read shows already terminal gets the release read before the drop: the pass reads the entry's pull request once and records the merged outcome when it reads merged and the terminal outcome otherwise. A forge that closes the issue on merge makes the tracker report terminal before this pass reads the merge, and the release read keeps the merge from being lost.

Unlike five of its seven sibling reaction kinds, a `merge-completion` pending entry carries no general expiry. CI, review, bot-review, auto-merge, and merge-conflict each observe something transient and bound their pending entries with a per-kind configured watch window so a dropped observation does not poll forever. Merge-completion carries no such bound, the same posture as the label-review and label-fix kinds: a pull request may wait on human review for an unbounded time. That time does not count toward the missing-commit bound in §11G.4, and the pending entry's original `CreatedAt` MUST NOT be used as that bound's origin.

Carrying no general expiry also means a pending `merge-completion` entry does not pin its workspace against periodic-sweep candidacy (§9.6, Invariant 4a), which is safe here because the pass reads nothing from the workspace directory. The entry is bounded by the issue leaving the configured handoff state and, after a merge is first reported without a commit identifier, by the fixed thirty-minute grace period in §11G.4, after which the entry stays pending under a `missing_merge_commit` escalation. It is rebuilt through three paths: a fresh pending entry seeded on a qualifying normal worker exit (§11G.1), a startup recovery re-seed for an issue still in the handoff state within the shared thirty-day recovery lookback (§14.3), and the startup restore of a standing escalation from its stored record (§11G.6).

An escalated entry keeps watching the issue's state. It stays pending, re-enqueued at the poll interval, and each due pass makes the batched tracker state read and the `GetMergeability` read for it, so the pass observes the facts that end the escalation (§11G.6). It makes no transition call, with the one exception of the call a restart resumes (§11G.6), and it applies no escalation action.

Before any entry is examined, the pass checks once per tick whether the `target_state` captured at construction is still a member of the runtime `tracker.terminal_states` list, which is read fresh on every tick unlike the reaction configuration itself. A drift (the target state falling out of a reloaded terminal-state list) logs one warning at the onset and suppresses repeats while the condition persists. The check never changes an entry's disposition; a running orchestrator keeps transitioning issues to the frozen target until it restarts, at which point the same drifted configuration is rejected offline (§11G.3).

### 11G.3 Terminal-target selection rule

The reaction transitions the linked issue to the single state named by its own `target_state` field. The field has no default and the target is never inferred from `tracker.terminal_states`: deriving it by list position or by matching a well-known name would depend on ordering and vocabulary that carry no guaranteed meaning, since a tracker's terminal-state list routinely mixes a completion state with one or more abandonment states.

`target_state` is validated offline, with no network access, against the tracker configuration as it stood when the check ran:

- Required whenever `reactions.merge_completion.provider` is non-empty.
- MUST NOT equal `tracker.handoff_state` (case-insensitive comparison).
- MUST NOT be a member of `tracker.active_states` (case-insensitive).
- MUST be a member of `tracker.terminal_states` (case-insensitive).

Both lists are read exactly as written in configuration.

`tracker.handoff_state` and `tracker.terminal_states` are themselves required whenever the reaction is configured, each reported as a distinct configuration error when absent.

Reaction configuration, including `target_state`, is captured once at orchestrator construction and is not rebuilt when `WORKFLOW.md` is reloaded. A change to `target_state`, or to either tracker prerequisite, takes effect only on the next restart. The offline validator runs the same construction path, so its verdict cannot diverge from what a restart would build.

### 11G.4 Idempotency latch

The normal idempotency key is the `reaction_fingerprints` row identified by the issue and the kind `merge-completion`, whose fingerprint value is the merge commit identifier (`PRMergeStatus.MergeCommitSHA`, §11C.2) reported by `GetMergeability`, not the pull request number. No sentinel, pull request identity, branch, or other substitute is ever written to this row. A merge reported with `Merged == true` but no commit identifier therefore does not latch the normal fingerprint.

The first such response for a pull request starts a fixed thirty-minute grace period. The pass persists a separate internal observation in `reaction_fingerprints` under kind `merge-completion-missing-sha`: its fingerprint is the normalized `owner/repo#number` identity, `updated_at` is the first time that identity was observed merged without a commit identifier, and `dispatched` records whether the one-shot escalation has been successfully delivered. Re-observing the same identity MUST preserve both fields; a different pull request identity replaces the fingerprint, records a new first-observed time, and resets `dispatched`. This row is observation state, not the merge idempotency key.

During the grace period the pending entry re-enqueues with the existing exponential pending backoff, floored at `poll_interval_ms`. `reactions.merge_completion.max_retries` is not consulted: it continues to bound only failed `TransitionIssue` calls (§11G.5). Expiry is evaluated on the first reconcile tick at or after the deadline. If a real commit identifier appears, the pass proceeds through the unchanged normal fingerprint and transition path. The internal observation is cleared best-effort only after that normal fingerprint is already latched as dispatched, so an observation-delete failure cannot block the primary transition and a failed transition cannot restart the grace-period clock.

At expiry, while the identifier is still absent, the pass begins a `missing_merge_commit` escalation on the pending entry, logs the escalation at error level, and emits the configured escalation (§11G.6). The entry stays pending and re-enqueues at the poll interval, so it observes the forge reporting the identifier later. It never calls `TransitionIssue` while the identifier is absent and never writes a successful merge fingerprint for this condition. The internal observation is marked `dispatched` only under the delivery rule of §11G.6, once the action that confirms it and its follow-up marker write both succeed; a failure in either leaves it undispatched, without restarting the grace period. When only the marker write failed, the notification was already delivered, so the retry described in §11G.6 redelivers it rather than delivering it for the first time. If a later fresh pending entry sees the same expired identity and the identifier is still absent, it retries the undelivered path once and stays escalated. Once delivery is marked, a fresh entry stays escalated without repeating it.

If a later fresh pending entry instead observes a real identifier, it may perform the normal transition and then clear the internal observation as described above. While a merge-completion entry is active, seeing an issue that is terminal, outside the handoff state, absent from the tracker response, or attached to a pull request that is no longer found also clears the internal observation; a different pull request identity starts a new observation if it later reaches the same missing-identifier state. The entry stays active while the escalation stands, so it observes a person's state transition and clears the internal observation as described above.

Before transitioning, the pass upserts the observed commit identifier into the fingerprint row. If the stored value already equals the observed one and is marked dispatched, the transition is skipped; this dedups the same merge across both repeated poll ticks and a process restart in between. A different commit identifier (the issue produces a second managed merge) resets the dispatched flag through the same upsert, re-arming the latch for exactly one further transition.

The row's lifecycle diverges from the sibling reaction kinds in three ways. First, on a successful transition the row is marked dispatched and retained, never deleted: deleting it would let the next poll tick observe the same merge as new. Second, when a transition attempt escalates, whether immediately on an authentication or payload failure or after the retry bound is exhausted, the pending entry and the reaction-attempt counter stay in place under the escalation, and the fingerprint row is left undispatched. This is deliberate rather than an oversight: a fingerprint left undispatched lets the one transition call a restart resumes (§11G.6) retry the transition for the same commit identifier, instead of the escalated attempt being treated as final. A worker exit seeds over no standing escalation, so the later attempt comes from a restart.

Third, when `TransitionIssue` succeeds but the subsequent `MarkReactionDispatched` call itself fails, the pending entry and the reaction-attempt counter are still cleared and a warning is logged, but the fingerprint row is left undispatched even though the tracker transition already completed. This residue is accepted rather than treated as a bug: the issue is terminal by the time this happens, so on any later re-seed of the pending entry, the terminal-state drop in §11G.7 removes it before a second `TransitionIssue` call is ever attempted.

A not-found transition failure (§11G.5) is the one exception that marks the row dispatched despite never completing the transition, because there is no issue left for a later attempt to reach.

Cross-kind isolation: the stop and escalation paths touch only this kind's own pending entry and reaction-attempt counter. Neither path deletes `state.Claimed[issue_id]` or any pending entry, fingerprint row, or attempt counter belonging to another reaction kind on the same issue.

### 11G.5 Failure matrix by tracker error kind

A transition failure is routed by the tracker error taxonomy defined in §11:

| Error kind | Posture |
|------------|---------|
| `ErrTrackerTransport` | Retry with backoff, bounded by `max_retries`, then escalate. |
| `ErrTrackerAPI` | Retry with backoff, bounded by `max_retries`, then escalate. |
| Unclassified, or any kind not listed above | Routed with the retryable kinds above, the non-destructive default. |
| `ErrTrackerAuth` | Escalate immediately; consumes no retry budget. |
| `ErrTrackerPayload` | Escalate immediately; consumes no retry budget. |
| `ErrTrackerNotFound` | Stop: mark the fingerprint dispatched, drop the entry, log at warning level, no escalation. |

`max_retries` defaults to `2` and bounds every `TransitionIssue` disposition in the retryable group above: the transport and API kinds, and any unclassified or unlisted kind routed with them. The comparison is strict over-limit (`attempts > max_retries`) against a per-issue counter scoped to this kind. The counter is incremented after every `TransitionIssue` call regardless of outcome, including on an authentication or payload failure; that disposition consumes no retry budget only in the sense that it skips the over-limit comparison against the counter, not that the counter is left untouched. The counter's value at the point of escalation is still surfaced to the operator, in the `comment` escalation posture's attempt count (§11G.6).

Not every orchestrator-driven tracker write in the failure model (§14) gets a later re-attempt (a dispatch-time in-progress transition failure is only logged, and a handoff transition failure on a soft-stop worker exit releases the claim without one), but this transition is the terminal event in an issue's lifecycle under this reaction with no continuation of its own to fall back on: it fires once, after the pull request has already merged. Within a single process run, a failure that is only logged here is a failure an operator would otherwise never see. Across a restart, the residue described in §11G.4 gives the transition one later attempt instead (§11G.6). This is why every disposition above ends in either a bounded retry followed by escalation, an immediate escalation, or an explicit logged stop, and never a silent drop. An escalation keeps the pending entry and the counter (§11G.6).

### 11G.6 Escalation behavior

Every escalation emits the `escalation.merge_completion` event (Section 10.4.7) under every posture. Escalation applies one of three operator-configured postures, matching the mechanism the sibling reaction kinds already use:

- `label` (default): add `escalation_label` (default `needs-human`) to the tracker issue via `TrackerAdapter.AddLabel`. The event reaches only destinations that subscribe to `escalation.merge_completion`, so the issue receives no comment unless a `tracker_comment` entry lists it.
- `comment` (deprecated): apply no label; the `tracker_comment` destination receives the event through a subscription Sortie synthesizes from this value. The comment keeps posting and a deprecation advisory names the replacement: `escalation: none` plus `escalation.merge_completion` in the events of a `tracker_comment` entry (Section 5.3.10).
- `none`: apply no label; the event reaches only destinations subscribed to it.

The event body for a transition escalation is a plain-text message naming the pull request number, the repository, the configured target state, and the number of transition attempts made. For a missing-identifier expiry, the body names the repository, pull request number, elapsed wait, stop reason, configured target state, and manual follow-up, and the `label` posture also emits an operator-facing log carrying the same identifying and manual-action context.

A transition escalation records its trigger on the entry: `budget_exhausted` after the retry bound is exhausted, and `refused` on an authentication or payload failure. Its subject is the merge commit identifier. A missing-identifier expiry records `missing_merge_commit`, and its subject is the normalized pull request identity its observation row holds. The record is stored with the counter, so the escalation survives a restart. The configured action applies when the escalation begins and the stored subject differs from this one or this one is empty; the missing-identifier signal follows the delivery rule below instead, and the observation row, not the subject, decides whether it is sent.

The producer routes the event when it decides to escalate, so a reload that lands while the detached write is in flight does not change its destinations. A transition escalation runs detached under its own thirty-second deadline, and an escalation failure is logged.

A missing-identifier escalation runs in one detached `TrackerOpsWg` goroutine under a single shared thirty-second deadline, so a slow or failing tracker call does not block the reconcile tick and the goroutine as a whole stays within the budget the shutdown drain assumes for it. The observation is marked `dispatched` by this rule, which names the one action whose outcome confirms delivery:

```
if posture == label:
  if AddLabel fails: log error; return          // observation stays undelivered
  mark observation dispatched                   // right after the label
  deliver the event to its destinations         // best effort, after the mark
else:
  outcome = deliver the event to its destinations
  if tracker_comment was a destination and its write failed: log error; return
  mark observation dispatched                   // tracker accepted, or tracker_comment was not a destination
```

A Slack or webhook outcome never confirms delivery and never blocks it, so a successful send to either never suppresses the retry of a failed tracker action. Under `none` with no subscriber the event has no destination, and the observation is marked dispatched at once, as it would be after a label that nothing retries.

The pending entry stays pending with the escalation recorded on it, re-enqueued at the poll interval, whether or not the escalation call succeeds: a failure of the label or comment write itself changes nothing about the entry. The missing-identifier stop and any failure to deliver its operator signal are logged at error level. If the tracker write succeeds but the follow-up marker write itself fails, the notification has already reached the operator even though the observation is left recorded as undelivered, so the one delivery retry the first pass after a restart, or a pass of a fresh entry, performs redelivers rather than delivering for the first time: `label` repeats harmlessly, because re-applying a present label is a no-op, while a comment posts a second time. Neither failure mode restarts the grace period. Once marked, the same identity never repeats the operator signal.

While the escalation stands, the entry makes no transition call, applies no action, and appends no run-history row. The escalation ends on any of these facts, and on no other:

- The batched state read finds the issue in a terminal state. It also ends the escalation when it finds the issue missing or outside `tracker.handoff_state`, but only once a direct read of the issue confirms it (Section 8.5 Part B), because the batched read is a listing that can trail Sortie's own handoff write. The outside-handoff case is a person's answer: the pass drops the entry with its counter, so the next run starts the reaction with a fresh budget.
- The pull request is missing (`ErrSCMNotFound`). A merged read does not end the escalation, because the pull request has already merged.
- For a `budget_exhausted` escalation, the ceiling no longer counts the stored tries as spent: this kind counts a counter above `max_retries` as spent. The pass then goes on to the transition as for an entry with no escalation.
- For a `missing_merge_commit` escalation, the forge reports the merge commit, and the pass goes on to the transition.

An entry restored at startup with a standing escalation runs its normal path once on its first due pass, making at most one transition call: a success ends the escalation and latches the fingerprint, and a failure leaves it standing, with no second action and no further call from later passes. The same pass retries an undelivered missing-identifier signal once under the delivery rule above.

### 11G.7 State machine

Per-issue `merge-completion` reaction lifecycle (the `issue_id:merge-completion` slot):

| From | Event | To | Action |
|------|-------|----|--------|
| (none) | Worker exits normally and passes the reaction-enqueue gate (§7.3; a made stage hop never passes it), SCM adapter and merge-completion configured, PR metadata present | pending | Seed the entry if absent. |
| pending | Reconcile tick, `now < PendingRetryAt` | pending | Re-enqueue, no API call. |
| pending | Reconcile tick, batched tracker state read fails | pending | Back off every due entry; re-enqueue; no forge call. |
| pending | Reconcile tick, issue missing from the state response | (none) | Drop the entry and clear the missing-SHA observation. |
| pending | Reconcile tick, issue already terminal | (none) | Read the entry's pull request once and record the merged outcome when it reads merged, the terminal outcome otherwise (Section 4.1.8); drop the entry and clear the missing-SHA observation. |
| pending | Reconcile tick, issue currently claimed by the orchestrator | pending | Re-enqueue at the poll interval; no forge call. |
| pending | Reconcile tick, issue has left the configured handoff state | (none) | Stop; drop the entry and clear the missing-SHA observation. |
| pending | Reconcile tick, `GetMergeability` returns not-found | (none) | Drop the entry and clear the missing-SHA observation. |
| pending | Reconcile tick, `GetMergeability` fails (other error) | pending | Back off; re-enqueue. |
| pending | Reconcile tick, PR not yet merged | pending | Re-enqueue at the poll interval; do not create or age a missing-SHA observation. |
| pending | First tick reporting this PR merged with no commit identifier | pending | Insert the internal observation with `updated_at = now`; back off from the poll interval; log a warning. |
| pending | Tick during the missing-identifier grace period | pending | Preserve the first-observed time; re-enqueue with exponential pending backoff; ignore `max_retries`. |
| pending | Missing-identifier grace expires, observation not dispatched | escalated | Begin a `missing_merge_commit` escalation; log the stop at error level; attempt the configured escalation; do not transition; re-enqueue at the poll interval. Mark the observation dispatched only after delivery succeeds. |
| pending | Expired missing identifier, observation still undispatched on a fresh entry | escalated | Retry the operator signal once and stay escalated; do not restart the grace period. |
| pending | Missing identifier, observation already dispatched | escalated | Begin or keep the `missing_merge_commit` escalation and re-enqueue at the poll interval; the observation row alone decides whether the signal goes out, so none is repeated. |
| pending | Real commit identifier appears after a missing-identifier observation | pending | Continue through the normal merge fingerprint path; after its dispatched latch is established, clear the internal observation best-effort. |
| pending | Reconcile tick, fingerprint upsert fails | pending | Back off; re-enqueue; log a warning. |
| pending | Reconcile tick, fingerprint read fails (after a successful upsert) | pending | Back off; re-enqueue; log a warning. |
| pending | Reconcile tick, merge commit already latched and dispatched | (none) | Drop the entry without transitioning. |
| pending | Reconcile tick, new or unlatched merge commit, `TransitionIssue` succeeds | dispatched | Mark the fingerprint dispatched; clear the attempt counter; drop the entry. |
| pending | Reconcile tick, new or unlatched merge commit, `TransitionIssue` succeeds, `MarkReactionDispatched` fails | (none) | Clear the attempt counter; drop the entry; log a warning; fingerprint stays undispatched (accepted residue, §11G.4). |
| pending | `TransitionIssue` fails with `ErrTrackerNotFound` | dispatched | Mark the fingerprint dispatched anyway; clear the attempt counter; drop the entry; no escalation. |
| pending | `TransitionIssue` fails with `ErrTrackerAuth` or `ErrTrackerPayload` | escalated | Escalate immediately with trigger `refused`; keep the entry and the attempt counter, re-enqueued at the poll interval; fingerprint stays undispatched. |
| pending | `TransitionIssue` fails (transport, API, or unclassified), `attempts <= max_retries` | pending | Back off; re-enqueue; fingerprint stays undispatched. |
| pending | `TransitionIssue` fails (transport, API, or unclassified), `attempts > max_retries` | escalated | Escalate with trigger `budget_exhausted`; keep the entry and the attempt counter, re-enqueued at the poll interval; fingerprint stays undispatched. |
| escalated | Reconcile tick, no ending fact | escalated | Re-enqueue at the poll interval. No transition call and no action. |
| escalated | First due pass after a restart that restored the escalation | escalated | Run the normal path once: at most one transition call. A success ends the escalation and latches the fingerprint; a failure leaves it standing with no second action. Retry an undelivered missing-identifier signal once (§11G.6). |
| escalated | The forge reports the merge commit of a `missing_merge_commit` escalation, or a ceiling that no longer counts the tries as spent ends a `budget_exhausted` one | pending | End the escalation; the pass goes on to the transition. |
| escalated | Issue terminal, or the pull request not found | (none) | End the escalation; drop the entry and its counter; clear the missing-SHA observation as the rows above do. |
| escalated | Issue missing or outside the handoff state | (none) | Keep the entry, re-enqueued at the poll interval, until the direct read of the issue confirms the exit; then end the escalation, drop the entry and its counter, and clear the missing-SHA observation. |
