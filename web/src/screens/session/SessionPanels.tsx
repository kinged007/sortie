import { Pair, Pairs, Panel } from "@/components/Panel";
import { costText, NOT_MEASURED } from "@/lib/figures";
import { fmtCount, fmtDuration } from "@/lib/format";

import type { ConfigResponse, TokenInfo } from "@/api/types.gen";
import type { SessionRun } from "@/components/use-session-run";
import type { ReactNode } from "react";

/** A session that resolved, live or finished. */
export type ResolvedSession = Extract<SessionRun, { kind: "live" | "finished" }>;

function figureText(n: number | null): string {
  return n === null ? NOT_MEASURED : fmtCount(n);
}

function tokensOf(session: ResolvedSession): ReactNode {
  const total = (session.kind === "live" ? session.running : session.run).uncached_tokens;
  if (total === null) {
    return NOT_MEASURED;
  }
  return session.kind === "live" ? (
    <>
      <strong className="font-bold">{fmtCount(total)}</strong> so far
    </>
  ) : (
    <strong className="font-bold">{fmtCount(total)}</strong>
  );
}

/** Input arrives cache-inclusive, so the fresh input leaves out both cache counters. */
function freshInputOf(tokens: TokenInfo): number | null {
  const { input_tokens: input, cache_read_tokens: read, cache_write_tokens: write } = tokens;
  if (input === null || read === null || write === null) {
    return null;
  }
  return Math.max(0, input - read - write);
}

/** Props of {@link UsagePanel}. */
export interface UsagePanelProps {
  session: ResolvedSession;
}

/** Renders the tokens and the estimated cost of one session. */
export function UsagePanel({ session }: UsagePanelProps) {
  const source = session.kind === "live" ? session.running : session.run;
  const { tokens } = source;

  return (
    <Panel title="Usage">
      <Pairs>
        {source.tokens_measured ? (
          <>
            <Pair label="Input">{figureText(freshInputOf(tokens))}</Pair>
            <Pair label="Output">{figureText(tokens.output_tokens)}</Pair>
            <Pair label="Cache read">{figureText(tokens.cache_read_tokens)}</Pair>
            <Pair label="Cache write">{figureText(tokens.cache_write_tokens)}</Pair>
            <Pair label="Tokens">{tokensOf(session)}</Pair>
          </>
        ) : (
          <Pair label="Tokens">{NOT_MEASURED}</Pair>
        )}
        <Pair label="Est. cost">{costText(source.cost_status, source.estimated_cost_usd)}</Pair>
      </Pairs>
    </Panel>
  );
}

/** Props of {@link SessionPanel}. */
export interface SessionPanelProps {
  session: ResolvedSession;
  /** The state's configuration; absent until the state loads. */
  config: ConfigResponse | undefined;
}

/** Renders a long identifier by its tail or head in mono, the whole value on hover. */
function ShortValue({ full, shown }: { full: string; shown: string }) {
  return (
    <span title={full} className="font-mono text-body-sm">
      {shown}
    </span>
  );
}

function lastSegmentOf(path: string): string {
  return path.split(/[\\/]/).filter(Boolean).pop() ?? path;
}

/** Renders the models, host, workspace, session ID, workflow, and stall timeout of one session. */
export function SessionPanel({ session, config }: SessionPanelProps) {
  const isLive = session.kind === "live";
  const source = isLive ? session.running : session.run;
  const stallTimeoutMs = config?.agent.stall_timeout_ms ?? null;

  return (
    <Panel title="Session">
      <Pairs>
        <Pair label="Configured">{source.configured_model || "agent default"}</Pair>
        <Pair label="Reported model">{source.model_name || "not reported"}</Pair>
        {source.ssh_host ? <Pair label="Host">{`${source.ssh_host} (SSH)`}</Pair> : undefined}
        {isLive ? (
          <Pair label="Workspace">
            <ShortValue
              full={session.running.workspace_path}
              shown={lastSegmentOf(session.running.workspace_path)}
            />
          </Pair>
        ) : undefined}
        {source.session_id ? (
          <Pair label="Session ID">
            <ShortValue full={source.session_id} shown={source.session_id.slice(0, 8)} />
          </Pair>
        ) : undefined}
        {source.workflow_file ? <Pair label="Workflow">{source.workflow_file}</Pair> : undefined}
        {isLive && stallTimeoutMs !== null ? (
          <Pair label="Stall timeout">{fmtDuration(stallTimeoutMs / 1000)}</Pair>
        ) : undefined}
      </Pairs>
    </Panel>
  );
}
