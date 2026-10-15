import { Fragment } from "react";

import { Badge } from "@/components/ui/badge";
import { fmtDuration } from "@/lib/format";

import type { ShapeMatch, ShapePriority, ShapeRule } from "@/api/types.gen";
import type { ReactNode } from "react";

/** Props of {@link Code}. */
export interface CodeProps {
  children: ReactNode;
}

/** Renders a configured name or pattern in the monospace face. */
export function Code({ children }: CodeProps) {
  return (
    <code className="rounded bg-surface-raised px-xxs font-mono text-[0.9em] [overflow-wrap:anywhere]">
      {children}
    </code>
  );
}

/** Renders each tracker label as a {@link Badge}, joined by a separator text. */
export function joinBadges(labels: readonly string[], separator: string): ReactNode {
  return joinNodes(
    labels.map((label) => <Badge key={label}>{label}</Badge>),
    separator,
  );
}

/** Joins nodes with a separator text, keying each so the result renders as siblings. */
export function joinNodes(nodes: readonly ReactNode[], separator: string): ReactNode {
  return nodes.map((node, index) => (
    <Fragment key={index}>
      {index === 0 ? undefined : separator}
      {node}
    </Fragment>
  ));
}

/** Renders each value in the monospace face, joined by a separator text. */
export function joinCode(values: readonly string[], separator: string): ReactNode {
  return values.map((value, index) => (
    <Fragment key={index}>
      {index === 0 ? undefined : separator}
      <Code>{value}</Code>
    </Fragment>
  ));
}

function priorityClause({ op, value, values }: ShapePriority): ReactNode {
  switch (op) {
    case "eq":
      return `priority ${value}`;
    case "in":
      return `priority ${(values ?? []).join(" or ")}`;
    case "lt":
      return `priority below ${value}`;
    case "lte":
      return `priority ${value} or below`;
    case "gt":
      return `priority above ${value}`;
    case "gte":
      return `priority ${value} or above`;
    default:
      return `priority ${op} ${value}`;
  }
}

function matchClauses(match: ShapeMatch): ReactNode[] {
  const clauses: ReactNode[] = [];
  if (match.labels !== undefined && match.labels.length > 0) {
    clauses.push(joinBadges(match.labels, " or "));
  }
  if (match.issue_type !== undefined && match.issue_type.length > 0) {
    clauses.push(`issue type ${match.issue_type.join(" or ")}`);
  }
  if (match.priority !== undefined) {
    clauses.push(priorityClause(match.priority));
  }
  if (match.identifier !== undefined && match.identifier.length > 0) {
    clauses.push(<>identifier {joinCode(match.identifier, " or ")}</>);
  }
  if (match.assignee !== undefined && match.assignee.length > 0) {
    clauses.push(`assignee ${match.assignee.join(" or ")}`);
  }
  if (match.title !== undefined && match.title.length > 0) {
    clauses.push(`title ${match.title.map((phrase) => `"${phrase}"`).join(" or ")}`);
  }
  return clauses;
}

/** Words how a dispatch rule selects an issue; a selection kind the client has no wording for reads as served. */
export function selectionText(rule: Pick<ShapeRule, "selected_by" | "stage" | "match">): ReactNode {
  switch (rule.selected_by) {
    case "stage":
      return rule.stage === undefined ? rule.selected_by : <Badge>{rule.stage}</Badge>;
    case "catch_all":
      return "as the catch-all";
    case "match": {
      const clauses = rule.match === undefined ? [] : matchClauses(rule.match);
      return clauses.length === 0 ? rule.selected_by : joinNodes(clauses, " and ");
    }
    default:
      return rule.selected_by;
  }
}

/** Words whether an agent kind reports token usage; an arrival the client has no wording for reads "not declared". */
export function usageText(arrival: string): string {
  switch (arrival) {
    case "incremental":
    case "turn_end":
      return "reports tokens";
    case "none":
      return "reports none";
    default:
      return "not declared";
  }
}

/** Words a configured span in milliseconds. */
export function msText(ms: number): string {
  return fmtDuration(ms / 1000);
}

/** Words where a reaction escalates; an escalation the client has no wording for reads as served. */
export function escalationText(escalation: string, label: string | undefined): ReactNode {
  switch (escalation) {
    case "label":
      return label === undefined ? "label" : <Badge>{label}</Badge>;
    case "none":
      return "—";
    default:
      return escalation;
  }
}

/** Props of {@link Note}. */
export interface NoteProps {
  children: ReactNode;
}

/** Renders a line of body text under a panel's content. */
export function Note({ children }: NoteProps) {
  return <p className="text-body-md text-ink-body">{children}</p>;
}

/** Props of {@link Table}. */
export interface TableProps {
  headers: readonly string[];
  children: ReactNode;
}

/** Renders a borderless table of rules or reactions in a container that scrolls sideways. */
export function Table({ headers, children }: TableProps) {
  return (
    <div className="max-w-full overflow-x-auto">
      <table className="w-full border-collapse text-body-md text-ink">
        <thead>
          <tr>
            {headers.map((header) => (
              <th
                key={header}
                scope="col"
                className="border-b border-line px-sm pb-xs text-left text-body-sm font-medium whitespace-nowrap text-ink-muted"
              >
                {header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

/** The class of a {@link Table} body cell. */
export const CELL = "border-b border-line px-sm py-xs align-baseline whitespace-nowrap";
