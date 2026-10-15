---
tracker:
  kind: github-pr
  api_key: $SORTIE_GITHUB_TOKEN
  project: $SORTIE_GITHUB_PROJECT
  active_states:
    - ready-for-agent
    - in-progress
  in_progress_state: in-progress
  terminal_states:
    - agent-done
  handoff_state: agent-review
  # A raw GitHub search-qualifier fragment. The adapter appends its own
  # repo:, type:pr, and state:open qualifiers, so do not repeat them here.
  query_filter: 'label:"needs agent" -label:blocked'

polling:
  interval_ms: 45000

workspace:
  root: $SORTIE_WORKSPACE_ROOT

hooks:
  after_create: |
    git clone --depth 1 $SORTIE_REPO_URL .
    go mod download
  before_run: |
    # SORTIE_ISSUE_IDENTIFIER is the pull-request number, so the GitHub CLI
    # checks out exactly the branch this run is meant to work on.
    git fetch origin "pull/$SORTIE_ISSUE_IDENTIFIER/head:pr-$SORTIE_ISSUE_IDENTIFIER"
    git checkout -B "sortie/pr-$SORTIE_ISSUE_IDENTIFIER" "pr-$SORTIE_ISSUE_IDENTIFIER"
  after_run: |
    make fmt 2>/dev/null || true
    git add -A
    git diff --cached --quiet || git commit -m "sortie(pr-$SORTIE_ISSUE_IDENTIFIER): automated changes"
    git push origin "HEAD:refs/heads/sortie/pr-$SORTIE_ISSUE_IDENTIFIER"
  before_remove: |
    git push origin --delete "sortie/pr-$SORTIE_ISSUE_IDENTIFIER" 2>/dev/null || true
  timeout_ms: 120000

agent:
  kind: claude-code
  command: claude
  max_turns: 15
  max_concurrent_agents: 4
  turn_timeout_ms: 3600000
  read_timeout_ms: 5000
  stall_timeout_ms: 300000
  stop_grace_ms: 5000
  max_retry_backoff_ms: 300000
  max_concurrent_agents_by_state:
    in-progress: 3
    ready-for-agent: 1

claude-code:
  permission_mode: bypassPermissions
  model: claude-sonnet-4-6
  max_turns: 50
  max_budget_usd: 5

server:
  port: 8642
---

{{/* Sortie sample workflow, GitHub Pull Requests + Claude Code.
     This adapter dispatches agents onto pull requests rather than issues.
     Required env vars:
       SORTIE_GITHUB_TOKEN     GitHub token (a fine-grained token scoped to
                              one repository is the least-privilege choice;
                              reading needs read access, and transitioning a
                              pull request or posting a comment needs write)
       SORTIE_GITHUB_PROJECT   Target repository as owner/repo (e.g. sortie-ai/sortie)
       SORTIE_REPO_URL         Git clone URL for the repository
     Optional:
       SORTIE_WORKSPACE_ROOT   Base directory for per-issue workspaces
                               (defaults to system temp)
     The before_run and after_run hooks shell out to the GitHub CLI, so gh
     must be on the agent host and authenticated for the same repository.
       gh auth status
       gh pr checkout --help

     github-pr is a tracker only. It registers no CI provider and no
     source-control provider, so the ci_failure, review_comments,
     bot_review_comments, merge_conflict, label_review, and label_fix
     blocks that the github and gitea examples carry are absent here and
     never render. Add a separate github: block if you need them.

     {{ .issue.parent }}, {{ .issue.blocked_by }}, {{ .issue.priority }},
     {{ .issue.branch_name }}, and {{ .issue.issue_type }} are always empty
     for a pull request, so no block below reads them. */}}
You are a senior engineer. Your work is tracked by an automated orchestrator (Sortie)
that manages your session, retries failures, and monitors progress.

## Your task

**PR #{{ .issue.identifier }}**: {{ .issue.title }}

{{ if .issue.description }}

### Description

{{ .issue.description }}
{{ end }}

## Context

This session works on an open pull request. The branch for it is already checked
out. Read before making changes:

- `CLAUDE.md` for build commands and project boundaries
- `docs/architecture.md` for the relevant specification section
- The pull request's existing discussion, so your changes do not contradict a
  review comment that has already been addressed

## Rules

1. Run the project's lint and test commands before finishing. All checks must pass.
2. Do not modify protected files (architecture docs, ADRs, LICENSE) unless the task
   explicitly requires it.
3. Write table-driven tests. Cover edge cases, not just the happy path.
4. If you encounter a problem outside the scope of this task, write `blocked` to
   `.sortie/status` and stop.
5. Keep changes minimal - implement exactly what the task requires.

{{ if not .run.is_continuation }}

## Approach

1. Read the relevant documentation and existing code before writing anything.
2. Implement the minimal change that satisfies the task requirements.
3. Write or update tests to cover the new behavior.
4. Run verification commands and fix any failures.
5. Push your commits to the branch above. Do not force-push over other people's
   commits, and do not merge the pull request.
{{ end }}

{{ if .run.is_continuation }}

## Continuation

You are resuming work on this pull request (turn {{ .run.turn_number }} of
{{ .run.max_turns }}). Review the current state of the workspace - check test
output, lint results, and any partial changes. Do not repeat work already completed.
Proceed with the next step.
{{ end }}

{{ if .attempt }}

## Retry

This is retry attempt {{ .attempt }}. A previous run failed or timed out. Check the
workspace for partial work and do not start from scratch. Review any error output from
the previous attempt if visible in the workspace.
{{ end }}

{{ if .issue.url }}

## Reference

Pull request: {{ .issue.url }}
{{ end }}

{{ if .issue.labels }}

## Labels

{{ .issue.labels | join ", " }}
{{ end }}
