# `github-pr` tracker compatibility plan

Make `internal/scm/githubpr` a peer of `internal/scm/github` across transport,
filtering, blocker declaration, error mapping, pagination, and operator-facing
documentation.

Status: Phases A, B, and D are implemented. Phase C is deferred at the user's
direction and stays recorded below.

## Contract baseline

`docs/architecture/11-issue-tracker-integration-contract.md` is authoritative.
The gaps below are contract violations, not preferences:

| Contract clause | Requirement | `github-pr` today |
| --- | --- | --- |
| §11.2 | Network timeout `30000 ms` on tracker transport | no timeout set |
| §11.3 | Blocker source is *declared* at registration; nothing inferred | declares `per_issue` over a no-op reader |
| §11.6.4 | Operator filter "never hides a terminal issue from that lookup" | filter appended to the closed-PR search |
| §11.4 / §11.6.4 | 403 splits auth vs rate limit; 400/422 are payload errors | every 403 is auth; 400/422 are generic API errors |
| §11.2 | Pagination ceiling must not silently truncate | ceiling hit returns partial data silently |

`github` is the reference implementation for each of these.

## Phase A — correctness (contract violations)

### A1. Blocker source

Register `registry.BlockersUnsupported`, drop the no-op `BlockerReader`, and set
`BlockersUnresolved: false` in `normalize`.

Today every candidate is marked unresolved, so the shared resolver spends one of
its four per-pass reads on every eligible PR. The read costs no HTTP call, but it
consumes the budget in `internal/orchestrator/dispatch.go`, so the fifth and later
eligible PRs in a pass are held as `blockers_not_read` for at least one poll
interval. GitLab, which also has no dependency relation, already declares
`BlockersUnsupported` and is never throttled.

Acceptance: a candidate from `github-pr` reports `BlockersUnresolved == false`
with a non-nil empty `BlockedBy`; no `fetch_blockers` metric is emitted; the
adapter no longer satisfies `domain.BlockerReader`.

### A2. Operator query filter

Delete the local filter engine (`queryFilter`, `queryAssignee`,
`resolvedQueryFilter`, `match`, `parseRequiredLabels`, `parseExcludedLabels`,
`parseAssigneeClause`, `currentLogin`) and fetch candidates through
`/search/issues` with `repo:<owner>/<repo> type:pr state:open <filter>` when a
filter is set, exactly as `github` does at `internal/scm/github/tracker.go:263`.

The current engine recognizes only `label:`, `-label:`, and `assignee:` and drops
every other qualifier, so an operator filter that also carries `author:`,
`draft:false`, or `milestone:` silently returns a larger candidate set than
requested. Its `strings.Fields` tokenizer also splits `label:"needs review"` into
`label:"needs` plus a stray `review"`, matching a label named `needs` and
discarding the rest.

`assignee:@me` needs no local handling: GitHub documents `@me` as a suffix for any
username qualifier, including `assignee`, so the raw filter goes to the server
unchanged. This removes the extra `/user` round trip the current code makes on
every candidate fetch.

Acceptance: `tracker.query_filter` reaches the search query verbatim; the open
path issues no request when no filter is set; quoted multi-word labels and
qualifiers the old parser dropped now work.

### A3. Terminal-state filter leak

Stop appending `a.queryFilter` in `fetchClosedPRsByLabel`.

`FetchIssuesByStates` drives running-state reconciliation and startup terminal
workspace cleanup. With the filter applied, a dispatched PR whose assignee or
labels drift away from the filter stops being found, so its workspace is never
cleaned and its claim is never released. `github` does not apply the filter on
this path for the same reason.

Acceptance: the closed-PR search query contains only the adapter's own
`repo:`, `type:pr`, `state:closed`, and `label:` qualifiers.

## Phase B — operational parity

### B1. Shared client and error classifier

Move `newGitHubClient` and `classifyHTTPError` from `internal/scm/github` into a
new `internal/scm/githubapi` package and register it in
`internal/adaptertest/contract_test.go` `contractSharedFamilyPackages`, following
the existing `internal/scm/scmcore` precedent.

This fixes the missing timeout at its source for both adapters and gives
`github-pr` the full status mapping in one place, rather than duplicating the
classifier per adapter, which the adapter-boundary rule forbids.

Call sites to update: `github/tracker.go`, `github/ci.go`, `github/review.go`
(two), `githubpr/tracker.go`. `github/client_test.go` moves with the code.

### B2. Pagination diagnostics

Add `OnLimitReached` to the three `githubpr` paginators and decode
`incomplete_results` in the search response, warning when the server reports a
partial result set.

Acceptance: hitting the 200-page ceiling logs a warning naming the endpoint;
an `incomplete_results: true` response logs a warning.

## Phase C — feature and validation (deferred)

Deferred by decision. Nothing here is a correctness defect: each item is a
capability the `github` adapter has and `github-pr` does not.

- **ETag cache on state refresh.** `fetchStatesByNumbers` issues one
  unconditional GET per PR per tick. Move `github`'s cache into `githubapi` and
  use conditional requests from both adapters. Requires an exported cache API.
- **Normalization.** `pullRequest` carries no `IssueType` and no parent link.
  `github` populates both. `state_reason` is not accepted on GitHub's PR route,
  so the write-side difference is intentional and must not be "fixed".
- **Credential reuse with the `github` CI/SCM providers.**
  `mergeTrackerCredentials` runs only on exact kind equality
  (`cmd/sortie/main.go`), so `tracker.kind: github-pr` with `provider: github`
  requires a duplicate top-level `github:` block, which is undocumented and draws
  an `unknown_key` advisory. Decide between a credential-source alias and
  requiring the explicit block, then document the choice.
- **Validation hardening.** `validateConfig` omits the `GITHUB_TOKEN` advisory
  that `github` emits, and an empty `project` returns `tracker_payload_error`
  instead of `missing_tracker_project`. Add a `validate_test.go` either way.
- **Live integration suite.** `github` has an env-gated
  `SORTIE_GITHUB_TEST` suite. `github-pr` has none.

## Phase D — documentation (implemented)

`github-pr` appeared in no Markdown file in the repository. Added:

- `README.md` issue-tracker list.
- `docs/workflow-reference.md` supported kinds, API-key list, and a
  `query_filter` note stating the raw search-qualifier grammar.
- `docs/architecture/11-issue-tracker-integration-contract.md` section 11.6.5,
  covering the differences from section 11.6.4: no dependency relation, no
  `state_reason`, no parent, no branch name, no native issue type, no ETag
  cache, and tracker-only registration.
- `examples/WORKFLOW.github-pr.md`.
- `CHANGELOG.md` Unreleased entries.

## Test plan

Existing `githubpr` coverage is six happy-path functions. This change adds:

- Candidate `BlockersUnresolved == false` and empty non-nil `BlockedBy`.
- Search query carries `type:pr` and the operator filter verbatim, including a
  quoted multi-word label.
- No `/search/issues` request when the filter is empty.
- Closed-PR search query omits the operator filter.
- 30-second client timeout is configured.
- Error classification table: 400/401/403-perm/403-ratelimit/404/405/409/410/422/429/500.
- Pagination ceiling warning fires.

Rewritten: the three existing filter tests currently assert client-side matching
against `/pulls`. They now assert the query the adapter sends.

Gate: `make check`, plus `go test -race` on both SCM packages.
