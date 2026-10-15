import { fmtDue, fmtDuration, parseInstant } from "@/lib/format";

import type {
  ActiveIssueResponse,
  AttentionItemResponse,
  BudgetExhaustedResponse,
  ConfigResponse,
  PendingReactionResponse,
  ParkedEntryResponse,
  RetryEntryResponse,
  RunningEntryResponse,
  StageHopResponse,
  StagePositionResponse,
  StateResponse,
} from "@/api/types.gen";
import type { Mark } from "@/lib/run-status";

/** The row list of the state that decided a row. */
export type RowKind =
  | "running"
  | "retrying"
  | "budget_exhausted"
  | "parked"
  | "pending_reactions"
  | "between_stages";

/** The lifecycle phase a row belongs to; the Active chips filter on it. */
export type Phase =
  | "running"
  | "pull_request"
  | "between_stages"
  | "retry"
  | "needs_attention"
  | "ended_today";

/** Lists every phase in the order the chips appear. */
export const PHASES: readonly Phase[] = [
  "running",
  "pull_request",
  "between_stages",
  "retry",
  "needs_attention",
  "ended_today",
];

/** What the lifecycle chips and the Rule menu read of a row, whichever screen draws it. */
export interface Filterable {
  phase: Phase;
  rule: string | null;
}

/** One issue in Active, built from a served `active` entry. */
export interface ActiveRow {
  issueId: string;
  identifier: string;
  displayKey: string;
  title: string | null;
  kind: RowKind;
  phase: Phase;
  mark: Mark;
  statusText: string;
  statusTextFull: string;
  route: string | null;
  agentKind: string | null;
  rule: string | null;
  timeBase: string | null;
  acknowledgedAt: string | null;
}

/** One entry of the Rule menu; `group` is the first rule of the chain a header or member belongs to. */
export interface RuleOption {
  kind: "any" | "chain" | "member" | "caption" | "rule" | "none";
  value: string;
  label: string;
  count: number;
  group: string | null;
}

/** The state row that decided an issue, tagged with the list it came from. */
export type Deciding =
  | { kind: "running"; entry: RunningEntryResponse }
  | { kind: "retrying"; entry: RetryEntryResponse }
  | { kind: "budget_exhausted"; entry: BudgetExhaustedResponse }
  | { kind: "parked"; entry: ParkedEntryResponse }
  | { kind: "pending_reactions"; entry: PendingReactionResponse }
  | { kind: "between_stages"; entry: StageHopResponse };

interface RuleSource {
  stage_position: StagePositionResponse | null;
  rule_name?: string;
}

const DEFAULT_RULE = "default";

function isNamedRule(name: string | undefined): name is string {
  return name !== undefined && name !== "" && name !== DEFAULT_RULE;
}

/** Gives the phase of a row; a reaction row is needs-attention only once it escalated. */
export function phaseOf(kind: RowKind, escalatedAt: string | null): Phase {
  switch (kind) {
    case "parked":
    case "budget_exhausted":
      return "needs_attention";
    case "pending_reactions":
      return escalatedAt === null ? "pull_request" : "needs_attention";
    case "running":
      return "running";
    case "retrying":
      return "retry";
    case "between_stages":
      return "between_stages";
  }
}

/** Gives the mark of a row and, while an item of the issue was acknowledged, when. */
export function rowMark(
  kind: RowKind,
  issueId: string,
  attention: readonly AttentionItemResponse[],
): { mark: Mark; acknowledgedAt: string | null } {
  const items = attention.filter((item) => item.issue_id === issueId);
  if (items.some((item) => item.acknowledged_at === null)) {
    return { mark: "needs", acknowledgedAt: null };
  }
  const acknowledged = items.find((item) => item.acknowledged_at !== null);
  if (acknowledged !== undefined) {
    return { mark: "held", acknowledgedAt: acknowledged.acknowledged_at };
  }
  if (kind === "running") {
    return { mark: "running", acknowledgedAt: null };
  }
  return {
    mark: kind === "retrying" ? "retry" : "waiting",
    acknowledgedAt: null,
  };
}

/** Words a row's place in a chain or its rule, or `null` when neither is worth showing. */
export function routeText(entry: RuleSource, config: ConfigResponse | undefined): string | null {
  const position = entry.stage_position;
  if (position !== null) {
    const index = (position.chain ?? []).indexOf(position.current);
    const length = (position.chain ?? []).length;
    if (index >= 0 && length > 1) {
      return `${position.current}, ${index + 1} of ${length}`;
    }
    return position.current;
  }
  const hasRules = (config?.dispatch.rules ?? []).length > 0;
  return hasRules && isNamedRule(entry.rule_name) ? entry.rule_name : null;
}

/** Gives the value the Rule menu filters a row on. */
export function ruleOf(entry: RuleSource): string | null {
  if (entry.stage_position !== null) {
    return entry.stage_position.current;
  }
  return isNamedRule(entry.rule_name) ? entry.rule_name : null;
}

/** Gives the instant a deciding entry's time is counted from or to. */
export function timeBaseOf(deciding: Deciding): string | null {
  switch (deciding.kind) {
    case "running":
      return deciding.entry.started_at;
    case "retrying":
      return deciding.entry.due_at;
    case "budget_exhausted":
      return deciding.entry.exhausted_at;
    case "parked":
      return deciding.entry.parked_at;
    case "pending_reactions":
      return deciding.entry.created_at;
    case "between_stages":
      return deciding.entry.hopped_at;
  }
}

function isRowKind(value: string): value is RowKind {
  switch (value) {
    case "running":
    case "retrying":
    case "budget_exhausted":
    case "parked":
    case "pending_reactions":
    case "between_stages":
      return true;
    default:
      return false;
  }
}

function namedRow(
  state: StateResponse,
  kind: RowKind,
  entry: ActiveIssueResponse,
): Deciding | undefined {
  const isIssue = (row: { issue_id: string }) => row.issue_id === entry.issue_id;
  switch (kind) {
    case "running": {
      const named = (state.running ?? []).find(isIssue);
      return named === undefined ? undefined : { kind, entry: named };
    }
    case "retrying": {
      const named = (state.retrying ?? []).find(isIssue);
      return named === undefined ? undefined : { kind, entry: named };
    }
    case "budget_exhausted": {
      const named = (state.budget_exhausted ?? []).find(isIssue);
      return named === undefined ? undefined : { kind, entry: named };
    }
    case "parked": {
      const named = (state.parked ?? []).find(isIssue);
      return named === undefined ? undefined : { kind, entry: named };
    }
    case "pending_reactions": {
      const named = (state.pending_reactions ?? []).find(
        (row) => isIssue(row) && row.kind === entry.reaction_kind,
      );
      return named === undefined ? undefined : { kind, entry: named };
    }
    case "between_stages": {
      const named = (state.between_stages ?? []).find(isIssue);
      return named === undefined ? undefined : { kind, entry: named };
    }
  }
}

/**
 * Finds the state row that decided an active entry, from the list its status
 * names. It returns `undefined` for a status that names no list and for a row
 * the list no longer holds.
 */
export function decidingRowOf(
  state: StateResponse,
  entry: ActiveIssueResponse,
): Deciding | undefined {
  return isRowKind(entry.status) ? namedRow(state, entry.status, entry) : undefined;
}

function toRow(entry: ActiveIssueResponse, kind: RowKind, state: StateResponse): ActiveRow {
  const named = namedRow(state, kind, entry);
  const escalatedAt = named?.kind === "pending_reactions" ? named.entry.escalated_at : null;
  const { mark, acknowledgedAt } = rowMark(kind, entry.issue_id, state.attention ?? []);
  return {
    issueId: entry.issue_id,
    identifier: entry.issue_identifier,
    displayKey: entry.display_identifier || entry.issue_identifier,
    title: entry.issue_title || null,
    kind,
    phase: phaseOf(kind, escalatedAt),
    mark,
    statusText: entry.status_text,
    statusTextFull: entry.status_text_full,
    route: routeText(entry, state.config),
    agentKind: entry.agent_kind || null,
    rule: ruleOf(entry),
    timeBase: named === undefined ? null : timeBaseOf(named),
    acknowledgedAt,
  };
}

/** Builds the Active row of one served `active` entry, or `undefined` for a status that names no list. */
export function activeRowOf(
  state: StateResponse,
  entry: ActiveIssueResponse,
): ActiveRow | undefined {
  return isRowKind(entry.status) ? toRow(entry, entry.status, state) : undefined;
}

/** Builds the Active rows from the served `active` entries, in the served order. */
export function activeRows(state: StateResponse): ActiveRow[] {
  return (state.active ?? []).flatMap((entry) => {
    const row = activeRowOf(state, entry);
    return row === undefined ? [] : [row];
  });
}

/** Words the time of a deciding entry on the server clock: time left for a retry, time elapsed otherwise. */
export function timeText(kind: RowKind, timeBase: string | null, now: number): string {
  const base = parseInstant(timeBase);
  if (base === null) {
    return "unknown";
  }
  return kind === "retrying" ? fmtDue((base - now) / 1000) : fmtDuration((now - base) / 1000);
}

/** Words the time cell of a row on the server clock. */
export function timeCell(row: ActiveRow, now: number): string {
  return timeText(row.kind, row.timeBase, now);
}

function countRows(rows: readonly Filterable[], matches: (rule: string | null) => boolean): number {
  return rows.filter((row) => matches(row.rule)).length;
}

function chainsOf(config: ConfigResponse): string[][] {
  return (config.dispatch.chains ?? []).filter((chain) => chain.length > 0);
}

function matchesQuery(label: string, query: string): boolean {
  return label.toLowerCase().includes(query.toLowerCase());
}

function filterOptions(options: RuleOption[], query: string): RuleOption[] {
  if (query === "") {
    return options;
  }
  const selectable = options.filter((o) => o.kind !== "any" && o.kind !== "caption");
  const kept = options.filter((option) => {
    switch (option.kind) {
      case "any":
        return false;
      case "caption":
        return true;
      case "chain":
        return selectable.some((o) => o.group === option.group && matchesQuery(o.label, query));
      case "member":
        return matchesQuery(option.label, query) || matchesQuery(option.group ?? "", query);
      default:
        return matchesQuery(option.label, query);
    }
  });
  return kept.filter(
    (option, index) => option.kind !== "caption" || kept[index + 1]?.kind === "rule",
  );
}

/**
 * Builds the Rule menu options with the row count of each, narrowed by a
 * case-insensitive substring query. It returns nothing while the workflow
 * holds no rules.
 */
export function ruleOptions(
  config: ConfigResponse | undefined,
  rows: readonly Filterable[],
  query: string,
): RuleOption[] {
  const rules = config?.dispatch.rules ?? [];
  if (config === undefined || rules.length === 0) {
    return [];
  }
  const options: RuleOption[] = [
    {
      kind: "any",
      value: "",
      label: "Any rule",
      count: rows.length,
      group: null,
    },
  ];
  const chains = chainsOf(config);
  const chained = new Set(chains.flat());
  chains.forEach((chain, index) => {
    const [first] = chain;
    options.push({
      kind: "chain",
      value: `chain:${first}`,
      label: chains.length > 1 ? `Stage Chain (${index + 1})` : "Stage Chain",
      count: countRows(rows, (rule) => rule !== null && chain.includes(rule)),
      group: first,
    });
    for (const name of chain) {
      options.push({
        kind: "member",
        value: `rule:${name}`,
        label: name,
        count: countRows(rows, (rule) => rule === name),
        group: first,
      });
    }
  });
  const others: string[] = [];
  for (const name of rules.map((rule) => rule.name)) {
    if (name !== undefined && name !== "" && !chained.has(name) && !others.includes(name)) {
      others.push(name);
    }
  }
  for (const row of rows) {
    if (row.rule !== null && !chained.has(row.rule) && !others.includes(row.rule)) {
      others.push(row.rule);
    }
  }
  if (others.length > 0) {
    options.push({
      kind: "caption",
      value: "",
      label: chains.length > 0 ? "Other rules" : "Rules",
      count: 0,
      group: null,
    });
    for (const name of others) {
      options.push({
        kind: "rule",
        value: `rule:${name}`,
        label: name,
        count: countRows(rows, (rule) => rule === name),
        group: null,
      });
    }
  }
  if (rows.some((row) => row.rule === null)) {
    options.push({
      kind: "none",
      value: "none",
      label: "No rule",
      count: countRows(rows, (rule) => rule === null),
      group: null,
    });
  }
  return filterOptions(options, query);
}

/** Reports whether a search parameter value names a phase. */
export function isPhase(value: string | null): value is Phase {
  return PHASES.some((phase) => phase === value);
}

function matchesRule(row: Filterable, rule: string, config: ConfigResponse | undefined): boolean {
  if (rule === "none") {
    return row.rule === null;
  }
  if (rule.startsWith("rule:")) {
    return row.rule === rule.slice("rule:".length);
  }
  if (rule.startsWith("chain:")) {
    const first = rule.slice("chain:".length);
    const chain = config === undefined ? undefined : chainsOf(config).find((c) => c[0] === first);
    return row.rule !== null && chain !== undefined && chain.includes(row.rule);
  }
  return false;
}

/**
 * Keeps the rows a phase and a rule select. An unknown phase is ignored; a
 * rule no option carries matches nothing, so the list shows its empty state.
 */
export function filterRows<T extends Filterable>(
  rows: readonly T[],
  phase: string | null,
  rule: string | null,
  config: ConfigResponse | undefined,
): T[] {
  return rows.filter(
    (row) =>
      (!isPhase(phase) || row.phase === phase) &&
      (rule === null || rule === "" || matchesRule(row, rule, config)),
  );
}

/** Counts rows per phase; pass rows already narrowed by the rule. */
export function chipCounts(rows: readonly Filterable[]): Record<Phase, number> {
  const counts: Record<Phase, number> = {
    running: 0,
    pull_request: 0,
    between_stages: 0,
    retry: 0,
    needs_attention: 0,
    ended_today: 0,
  };
  for (const row of rows) {
    counts[row.phase] += 1;
  }
  return counts;
}
