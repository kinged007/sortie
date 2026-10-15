const ISSUE_ROUTE_PREFIX = "/issues/";
const FALLBACK_BACK = "/";

/** The router state an issue link carries: the in-app path the issue page returns to. */
export interface IssueRouteState {
  back: string;
}

function isIssueRoute(pathname: string): boolean {
  return pathname.startsWith(ISSUE_ROUTE_PREFIX);
}

function withoutPeek(pathname: string, search: string): string {
  const params = new URLSearchParams(search);
  params.delete("peek");
  const text = params.toString();
  return text === "" ? pathname : `${pathname}?${text}`;
}

/**
 * Gives the back target stored in a router state, or `/` when the state holds
 * none or one that leaves the dashboard: history state is writable by any
 * script of the origin and by the address bar of a restored session.
 */
export function backOf(state: unknown): string {
  const back = (state as { back?: unknown } | null | undefined)?.back;
  if (
    typeof back === "string" &&
    back.startsWith("/") &&
    !back.startsWith("//") &&
    !isIssueRoute(back)
  ) {
    return back;
  }
  return FALLBACK_BACK;
}

/**
 * Gives the target and router state of a link to an issue page opened from
 * `from`. An issue page opened from another issue page keeps that page's back
 * target, so `Esc` never walks a chain of issues.
 */
export function issueLinkOf(
  identifier: string,
  from: { pathname: string; search: string; state: unknown },
): { to: string; state: IssueRouteState } {
  const back = isIssueRoute(from.pathname)
    ? backOf(from.state)
    : withoutPeek(from.pathname, from.search);
  return { to: `${ISSUE_ROUTE_PREFIX}${encodeURIComponent(identifier)}`, state: { back } };
}
