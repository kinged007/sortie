<p align="center">
  <img src="docs/assets/banner.jpg" alt="Sortie - Turn tracker tickets into agent sessions" width="100%">
</p>

<div align="center">

**Your next sprint isn't capped by headcount.**

The open-source coding agent orchestrator.<br/>
Run the coding agents you already use on tasks from your issue tracker - in parallel, on your own machine or server.

[![CI](https://github.com/kinged007/sortie/actions/workflows/ci.yml/badge.svg)](https://github.com/kinged007/sortie/actions/workflows/ci.yml)
[![CI upstream](https://github.com/sortie-ai/sortie/actions/workflows/ci.yml/badge.svg)](https://github.com/sortie-ai/sortie/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/sortie-ai/sortie/graph/badge.svg?token=K2TPXBCbvb)](https://codecov.io/gh/sortie-ai/sortie)
[![Go Reference](https://pkg.go.dev/badge/github.com/sortie-ai/sortie.svg)](https://pkg.go.dev/github.com/sortie-ai/sortie)
[![Release](https://img.shields.io/github/v/release/kinged007/sortie?label=release)](https://github.com/kinged007/sortie/releases)

[Website](https://sortie-ai.com) · [Documentation](https://docs.sortie-ai.com) · [Discord](https://discord.gg/MCNrc8Wf9M) · [Contributing](CONTRIBUTING.md)

**English | [简体中文](README.zh-CN.md)**

</div>

## What this fork adds

> **Fork:** `kinged007/sortie` — 18 commits ahead of [`sortie-ai/sortie`](https://github.com/sortie-ai/sortie). Upstream PRs: **pi adapter** and **github-pr tracker** (see below).

This fork adds two adapters missing upstream so you can run Sortie against a wider set of tools:

| Feature | Kind | What it does | Docs / Example |
|---|---|---|---|
| **Pi agent** | `agent.kind: pi` | Drives the Pi CLI (`pi -p --mode json`) — one subprocess per turn, JSON event stream normalized to domain events, token usage from `turn_end`, session resume via `--session`, `~/.pi/agent/models.json` credential check. Declares `MCPInjectionUnsupported` (no MCP transport). | [`docs/pi-adapter-notes.md`](docs/pi-adapter-notes.md) · [`examples/WORKFLOW.pi.md`](examples/WORKFLOW.pi.md) |
| **GitHub PR tracker** | `tracker.kind: github-pr` | Surfaces GitHub pull requests as Sortie issues. Reads from `GET /repos/{owner}/{repo}/pulls` (PRs the issues route omits); writes labels via the Issues routes. `query_filter` is a raw GitHub search-qualifier fragment evaluated server-side. Declares `BlockersUnsupported`. | [`examples/WORKFLOW.github-pr.md`](examples/WORKFLOW.github-pr.md) · Architecture §11.6.5 |

Both adapters are covered by unit tests and conformance tests and ship with Docker examples (`examples/docker/pi.Dockerfile`).

**Upstream PRs:** to be opened against `sortie-ai/sortie` — one per feature, each with tests and docs — so the fork stays rebase-friendly while contributing back.

## What it is

Sortie turns tracker tickets into autonomous agent sessions. You declare which issues to work on, which coding agent to run, and what instructions to give it in one `WORKFLOW.md`; Sortie picks up matching issues, runs agents in parallel in isolated workspaces, retries failures, and tracks cost and history — as a single Go binary with SQLite, no database or queue to deploy.

## Works With

**Issue trackers:** GitHub Issues and GitHub Pull Requests, GitLab Issues, Gitea Issues, Linear and Jira.

**Coding agents:** Claude Code, Copilot, OpenCode, Codex, Kiro, Pi and Gemini.

## Install

```bash
curl -sSL https://get.sortie-ai.com/install.sh | sh
# or build from source
go install github.com/sortie-ai/sortie/cmd/sortie@latest
# or
make build   # produces ./sortie
```

Homebrew (upstream tap): `brew install --cask sortie-ai/tap/sortie`

Try the [local demo](https://docs.sortie-ai.com/getting-started/quick-start/) with a simulated agent and no external services, or [connect your tracker and agent](https://docs.sortie-ai.com/getting-started/).

## Usage

```bash
sortie init                  # scaffold WORKFLOW.md
sortie validate              # check WORKFLOW.md and adapter config
sortie run                   # start the orchestrator (polls tracker, launches agents)
sortie stats                 # token and cost totals
```

Minimal `WORKFLOW.md` with this fork's adapters:

```yaml
# pi agent + github-pr tracker
tracker:
  kind: github-pr
  api_key: $SORTIE_GITHUB_TOKEN
  project: myorg/myrepo
  active_states: [ready-for-agent]
  terminal_states: [agent-done]

agent:
  kind: pi
  # pi reads ~/.pi/agent/models.json; no env credential needed
```

```yaml
# pi agent with model/thinking controls
agent:
  kind: pi
  model: claude-opus-4
  thinking: medium
  allowed_tools: [read, write, bash]
```

More examples: [`examples/WORKFLOW.pi.md`](examples/WORKFLOW.pi.md), [`examples/WORKFLOW.github-pr.md`](examples/WORKFLOW.github-pr.md), [`examples/WORKFLOW.md`](examples/WORKFLOW.md). Full reference: [docs.sortie-ai.com](https://docs.sortie-ai.com) and [`docs/workflow-reference.md`](docs/workflow-reference.md).

## How it is tested

```bash
make check   # lint (golangci-lint x linux/darwin/windows) + go vet + tests + shellcheck
make test    # go test -race -count=1 ./...
make test-coverage  # -covermode=atomic, report per package
```

- **Go:** `go test -race -count=1 ./...` and `go vet ./...` (via `golangci-lint`). CI runs the matrix on `linux`, `darwin`, `windows` plus a `tools/qualify` sub-module check and `shellcheck` for `*.sh`.
- **Coverage:** uploaded to Codecov on `ubuntu-latest` runs (`coverage.out`, `-covermode=atomic`).
- **Adapters:** adapter conformance tests in `internal/agent/pi/*_test.go` and `internal/scm/githubpr/*_test.go` drive fake runtimes/APIs; nightly integration tests are env-gated (`SORTIE_<ADAPTER>_TEST=1`).

CI: [`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on push to `main` and on pull requests. See also `make help` for all targets.

## How It Works

You describe the work in one `WORKFLOW.md` file: which issues to pick up, which agent to run, and what to tell it.

```markdown
---
tracker:
  kind: github
  api_key: $GITHUB_TOKEN
  project: acme/billing-api
  query_filter: "label:agent-ready"
  active_states: [todo, in-progress]
  handoff_state: review
  terminal_states: [done]
  
agent:
  kind: claude-code
  max_concurrent_agents: 4
---
  
You are a senior engineer.
  
## {{ .issue.identifier }}: {{ .issue.title }}
  
{{ .issue.description }}
```

Sortie polls your tracker for matching issues and runs an agent on each one in its own workspace, several at a time. A failed run is retried. Turn on CI and review feedback, and failed checks and review comments go back to the agent to fix.

You can edit `WORKFLOW.md` while Sortie runs, and it picks up the change without a restart. Run history survives restarts too, so progress and costs are still there afterwards.

Follow along in the built-in dashboard, or connect your own tools: a JSON API with a live stream of changes, health probes, and Prometheus metrics, all on one port.

Sortie runs the same way on a laptop, a VM, or a Kubernetes cluster. It ships as a container image at `ghcr.io/sortie-ai/sortie`, its health probes plug straight into liveness and readiness checks, and it shuts down cleanly when the pod stops. The [Kubernetes guide](https://docs.sortie-ai.com/guides/deploy-sortie-to-kubernetes/) walks through a full deployment.

Sortie is a single binary. There's no separate database or job queue to deploy, and Sortie itself sends no telemetry.

## Documentation

Full configuration reference, CLI usage, and getting started guide: [docs.sortie-ai.com](https://docs.sortie-ai.com)

- [Guides](https://docs.sortie-ai.com/guides/)
- [Architecture](https://docs.sortie-ai.com/concepts/architecture/)
- [Roadmap](https://github.com/orgs/sortie-ai/projects/1)
- [Code signing policy](https://docs.sortie-ai.com/code-signing-policy/)

## Prior Art

Sortie's architecture is informed by [OpenAI Symphony](https://github.com/openai/symphony), a spec-first orchestration framework with an Elixir reference implementation. Sortie diverges in language (Go for deployment simplicity), persistence (SQLite instead of in-memory state), extensibility (pluggable adapters for any tracker or agent, not hardcoded to Linear and Codex), and completion signaling (orchestrator-managed handoff transitions instead of relying solely on agent-initiated tracker writes).

## License

[Apache License 2.0](LICENSE)
