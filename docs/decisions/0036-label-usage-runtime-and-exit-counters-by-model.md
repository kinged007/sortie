---
status: accepted
date: 2026-10-17
decision-makers: Serghei Iakovlev
---

# Label Usage, Runtime and Exit Counters by Model

## Context and Problem Statement

A dispatch rule can select a different model per issue, and the runtime may run a model other than the one configured. Operators compare spend, agent time and failures across models. Until now, `sortie_tokens_total`, `sortie_agent_runtime_seconds_total` and `sortie_worker_exits_total` carried no model, so a per-model comparison needed run history and `sortie stats`, never the metrics endpoint. The decision that let a dispatch rule carry its agent kind's settings ruled out a `model` metric label on the grounds that its cardinality is unbounded. This record replaces that one sentence. The rest of that decision stands.

## Decision Drivers

1. **Bounded cardinality.** A label value must never grow with issues or runs.
2. **One rule for every agent.** The model a sample counts under is chosen the same way whichever agent kind ran.
3. **No lost samples on a healthy deployment.** A restart or a reload must not drop the first increment of a known model from `rate()` and `increase()`.
4. **No standing operational cost.** Sortie holds no increments back and needs no external coordination with the scraper.

## Considered Options

- Keep model out of metrics; per-model figures come from run history only
- Add a `model` label, created lazily on first increment
- Add a `model` label with zero series prepared for every known model

## Decision Outcome

Chosen option: **Add a `model` label with zero series prepared for every known model**, because it serves every driver. The one gap it leaves is closed on the Prometheus side, with no code in sortie.

The model values are model names from the workflow configuration and the names agents report. The label therefore multiplies the existing series by the number of models a deployment uses, never by issues or runs. Values pass verbatim. An empty value exports as `<none>`.

When a usage event also carries per-model token counts, its tokens count under every model those counts name, split by the counts and never more than the event reports. The ordered rule below governs only the share no count claims, which counts under the first non-empty model, in this order:

1. the model the carrying event or worker result names;
2. the model the run last reported;
3. the run's configured model.

The rule lives in orchestrator core and is the same for every agent kind. A run that switches models mid-way counts each sample, or the share of it no count claims, under the model current when that sample arrived.

Zero series are prepared at start-up for the configured models, for `<none>` and for every model in run history. They are prepared again after every successful configuration reload, and whenever a run first reports a model. A model the deployment has never seen before loses its first increment on a default Prometheus, because the sample that names it is also its first. Prometheus closes that gap with `--enable-feature=created-timestamp-zero-ingestion`: client_golang stamps each new series with its creation time, and the protobuf exposition carries that time to the server. Each server in an HA pair handles this independently. The operator documentation recommends the flag.

`sortie_worker_duration_seconds` keeps its labels.

### Considered Options in Detail

**Keep model out of metrics.** Rejected. It fails the operator need that drives this decision: dashboards and alerts cannot split spend or failures by model.

**Lazy label creation.** Rejected. Every model's first increment after each restart vanishes from `rate()` and `increase()`. That means a lost run on the runtime graph and a missed first error in alerts on a healthy deployment.

**Holding the first increment until a scrape reads the zero** was also weighed and rejected. With two scrapers, or a manual read of the endpoint, the held increment goes to the wrong reader or is counted wrongly.

## Consequences

### Negative

- A model that falls out of use keeps exporting its series: flat at its last value until the process restarts, then flat at zero, because run history still names it.
- Start-up reads every model in run history once, at a cost that grows with history.
- Dashboards and alerts that matched these counters without a `model` label must aggregate across it.
