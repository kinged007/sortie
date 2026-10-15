import { useStateQuery } from "@/api/queries";
import { fmtCount, fmtRate, fmtStamp, fmtTokens } from "@/lib/format";
import { Pair, Pairs, Panel } from "@/components/Panel";
import { StateBadge } from "@/components/StateBadge";

import { DispatchSection } from "./DispatchSection";
import { ReactionsSection } from "./ReactionsSection";
import { Code, joinCode, msText, Note, usageText } from "./workflow-text";

import type { ConfigResponse, TokenRateResponse, WorkflowHealthResponse } from "@/api/types.gen";
import type { ReactNode } from "react";

const HEADING = "font-heading text-h5 font-semibold text-ink";
const STANDFIRST = "text-body-lg text-ink-body";
const NONE = "none";

interface StandfirstProps {
  workflow: WorkflowHealthResponse | undefined;
}

function Standfirst({ workflow }: StandfirstProps) {
  if (workflow?.last_success_at == null) {
    return undefined;
  }
  const loaded = `Loaded at ${fmtStamp(workflow.last_success_at)}`;
  if (workflow.status === "fail") {
    return (
      <p className={STANDFIRST}>
        {loaded}; the newer edit did not load, so no new issue starts until it does.
      </p>
    );
  }
  return <p className={STANDFIRST}>{loaded}.</p>;
}

interface StateListProps {
  states: readonly string[] | null;
}

function StateList({ states }: StateListProps) {
  if (states === null || states.length === 0) {
    return NONE;
  }
  return (
    <span className="flex flex-wrap gap-xxs">
      {states.map((state) => (
        <StateBadge key={state} state={state} />
      ))}
    </span>
  );
}

interface ConfigProps {
  config: ConfigResponse;
}

function TrackerPanel({ config }: ConfigProps) {
  const { tracker, polling } = config;
  return (
    <Panel title="Tracker">
      <Pairs>
        <Pair label="Kind">{tracker.kind}</Pair>
        <Pair label="Active">
          <StateList states={tracker.active_states} />
        </Pair>
        {tracker.in_progress_state ? (
          <Pair label="In progress">
            <StateBadge state={tracker.in_progress_state} />
          </Pair>
        ) : undefined}
        {tracker.handoff_state ? (
          <Pair label="Handoff">
            <StateBadge state={tracker.handoff_state} />
          </Pair>
        ) : undefined}
        {tracker.no_change_state ? (
          <Pair label="No change">
            <StateBadge state={tracker.no_change_state} />
          </Pair>
        ) : undefined}
        <Pair label="Terminal">
          <StateList states={tracker.terminal_states} />
        </Pair>
        <Pair label="Poll">every {msText(polling.interval_ms)}</Pair>
      </Pairs>
    </Panel>
  );
}

function usageByKind(config: ConfigResponse): [string, string][] {
  const byKind = new Map<string, string>([[config.agent.kind, config.agent.usage_arrival]]);
  const { dispatch } = config;
  if (dispatch.default !== null) {
    byKind.set(dispatch.default.agent_kind, dispatch.default.usage_arrival);
  }
  for (const rule of dispatch.rules ?? []) {
    byKind.set(rule.agent_kind, rule.usage_arrival);
  }
  return [...byKind].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
}

function AgentPanel({ config }: ConfigProps) {
  const { agent, worker } = config;
  const byState = Object.entries(agent.max_concurrent_agents_by_state ?? {}).sort(([a], [b]) =>
    a < b ? -1 : a > b ? 1 : 0,
  );
  const hosts = worker.ssh_hosts ?? [];
  const perHost = worker.max_concurrent_agents_per_host;
  return (
    <Panel title="Agent">
      <Pairs>
        <Pair label="Kind">{agent.kind}</Pair>
        <Pair label="Max turns">{fmtCount(agent.max_turns)}</Pair>
        <Pair label="Stall timeout">
          {agent.stall_timeout_ms === null ? "off" : msText(agent.stall_timeout_ms)}
        </Pair>
        <Pair label="Retry backoff">up to {msText(agent.max_retry_backoff_ms)}</Pair>
        <Pair label="Concurrency">
          At most {fmtCount(agent.max_concurrent_agents)} at a time
          {byState.map(([state, cap]) => (
            <span key={state} className="block text-ink-body">
              {state}: at most {fmtCount(cap)}
            </span>
          ))}
        </Pair>
        <Pair label="Token budget">
          {agent.max_tokens === null ? NONE : `${fmtTokens(agent.max_tokens)} per issue`}
        </Pair>
        <Pair label="Session budget">
          {agent.max_sessions === null ? NONE : `${fmtCount(agent.max_sessions)} per issue`}
        </Pair>
        <Pair label="Usage reporting">
          {usageByKind(config).map(([kind, arrival]) => (
            <span key={kind} className="block">
              {kind}: {usageText(arrival)}
            </span>
          ))}
        </Pair>
        {hosts.length === 0 ? undefined : (
          <Pair label="SSH workers">
            {joinCode(hosts, ", ")}
            <span className="block text-ink-body">
              {perHost === null ? "no limit per host" : `at most ${fmtCount(perHost)} per host`}
            </span>
          </Pair>
        )}
      </Pairs>
    </Panel>
  );
}

function rateText(rate: number | null, unset: string): string {
  return rate === null ? unset : `${fmtRate(rate)} per 1M`;
}

interface RateListProps {
  kind: string;
  rate: TokenRateResponse;
}

function RateList({ kind, rate }: RateListProps) {
  const unsetCache = rate.input_per_mtok === null ? "not set" : "input rate (not set)";
  return (
    <div className="flex flex-col gap-xs">
      <p className="font-mono text-body-sm text-ink">{kind}</p>
      <Pairs>
        <Pair label="Input">{rateText(rate.input_per_mtok, "not set")}</Pair>
        <Pair label="Output">{rateText(rate.output_per_mtok, "not set")}</Pair>
        <Pair label="Cache read">{rateText(rate.cache_read_per_mtok, unsetCache)}</Pair>
        <Pair label="Cache write">{rateText(rate.cache_write_per_mtok, unsetCache)}</Pair>
      </Pairs>
      {rate.complete ? undefined : (
        <Note>
          Not priced without <Code>input_per_mtok</Code> and <Code>output_per_mtok</Code>.
        </Note>
      )}
    </div>
  );
}

function TokenRatesPanel({ config }: ConfigProps) {
  const rates = config.token_rates;
  return (
    <Panel title="Token rates">
      {rates === null ? (
        <p className="text-body-md text-ink-body">Not set: no cost is shown.</p>
      ) : (
        Object.keys(rates)
          .sort()
          .map((kind) => <RateList key={kind} kind={kind} rate={rates[kind]} />)
      )}
    </Panel>
  );
}

function hasReactions(config: ConfigResponse): boolean {
  return Object.values(config.reactions).some((reaction) => reaction !== undefined);
}

/** Renders Workflow: the configuration in effect as the state route serves it, under a standfirst that names a failed load of the file. */
export function WorkflowScreen(): ReactNode {
  const { data } = useStateQuery();
  if (data === undefined) {
    return <h1 className={HEADING}>Workflow</h1>;
  }
  const { config } = data;
  if (config === undefined) {
    return (
      <div className="flex flex-col gap-xs">
        <h1 className={HEADING}>Workflow</h1>
        <p className={STANDFIRST}>Sortie serves no configuration.</p>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-lg">
      <div className="flex flex-col gap-xs">
        <h1 className={HEADING}>Workflow</h1>
        <Standfirst workflow={data.health?.workflow} />
      </div>
      <div className="grid grid-cols-1 items-start gap-x-xxxl gap-y-xl min-[1024px]:grid-cols-[minmax(0,1fr)_320px]">
        <div className="flex min-w-0 flex-col gap-xl">
          <DispatchSection
            dispatch={config.dispatch}
            agent={config.agent}
            handoffState={config.tracker.handoff_state}
            hasReactions={hasReactions(config)}
          />
          <ReactionsSection
            reactions={config.reactions}
            handoffState={config.tracker.handoff_state}
          />
        </div>
        <div className="flex min-w-0 flex-col gap-xl">
          <TrackerPanel config={config} />
          <AgentPanel config={config} />
          <TokenRatesPanel config={config} />
        </div>
      </div>
    </div>
  );
}
