import { QueryClient, useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";

import { ApiError, getIssue, getIssueGroups, getRuns, getState, getStats } from "@/api/client";
import { setServerOffset } from "@/lib/server-clock";

import type { HistoryFilter } from "@/api/client";
import type {
  IssueDetailResponse,
  IssueGroupsResponse,
  Report,
  RunsResponse,
  StateResponse,
} from "@/api/types.gen";
import type { InfiniteData, UseInfiniteQueryResult, UseQueryResult } from "@tanstack/react-query";

/** The span of run history the Analytics screen reports. */
export type AnalyticsRange = "24h" | "7d" | "all";

/** The one cache of server data in a tab; a change signal, not a timer, decides when data is stale. */
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: Infinity, refetchOnWindowFocus: true },
    mutations: { retry: false },
  },
});

/** Builds the cache keys; `midnight` is the start of the reader's day in RFC 3339 UTC. */
export const queryKeys = {
  state: () => ["state"] as const,
  issue: (identifier: string) => ["issue", identifier] as const,
  ended: (midnight: string) => ["ended", midnight] as const,
  tally: (midnight: string) => ["tally", midnight] as const,
  firstRun: () => ["first-run"] as const,
  history: (filter: HistoryFilter) => ["history", filter] as const,
  historyRuns: (issueId: string, filter: HistoryFilter) =>
    ["history-runs", issueId, filter] as const,
  stats: (range: AnalyticsRange) => ["stats", range] as const,
};

/** Issues per History page and runs per page of an expanded group. */
export const HISTORY_PAGE_SIZE = 20;
export const HISTORY_RUNS_PAGE_SIZE = 50;

/**
 * The least time between two refetches a change signal may cause. A History
 * refetch rereads every loaded page and an Analytics refetch rescans every run
 * in its range, both through the orchestrator's store.
 */
export const HISTORY_REFETCH_FLOOR_MS = 5_000;
export const STATS_REFETCH_FLOOR_MS = 30_000;

const SINCE_BY_RANGE: Record<AnalyticsRange, string | undefined> = {
  "24h": "24h",
  "7d": "168h",
  all: undefined,
};

const ENDED_PAGE_SIZE = 200;

async function fetchEnded(midnight: string, signal: AbortSignal): Promise<RunsResponse[]> {
  const pages: RunsResponse[] = [];
  let cursor: string | undefined;
  do {
    const page = await getRuns({ ended_since: midnight, limit: ENDED_PAGE_SIZE, cursor }, signal);
    pages.push(page);
    cursor = page.next_cursor ?? undefined;
  } while (cursor !== undefined);
  return pages;
}

async function fetchHasRun(signal: AbortSignal): Promise<boolean> {
  try {
    const page = await getRuns({ limit: 1 }, signal);
    return (page.runs ?? []).length > 0;
  } catch (error) {
    if (error instanceof ApiError && error.status === 503 && error.code === "history_unavailable") {
      return false;
    }
    throw error;
  }
}

async function fetchState(signal: AbortSignal): Promise<StateResponse> {
  const state = await getState(signal);
  setServerOffset(state.generated_at);
  return state;
}

/** Reads the runtime state and fixes the server clock offset from every response. */
export function useStateQuery(): UseQueryResult<StateResponse> {
  return useQuery({ queryKey: queryKeys.state(), queryFn: ({ signal }) => fetchState(signal) });
}

/** Reads one issue; it stays idle while no issue is shown. */
export function useIssueQuery(identifier: string | undefined): UseQueryResult<IssueDetailResponse> {
  return useQuery({
    queryKey: queryKeys.issue(identifier ?? ""),
    queryFn: ({ signal }) => getIssue(identifier ?? "", signal),
    enabled: identifier !== undefined,
  });
}

/** Reads every page of runs that ended since the instant. */
export function useEndedQuery(midnight: string): UseQueryResult<RunsResponse[]> {
  return useQuery({
    queryKey: queryKeys.ended(midnight),
    queryFn: ({ signal }) => fetchEnded(midnight, signal),
  });
}

/** Reads the run statistics since the instant. */
export function useTallyQuery(midnight: string): UseQueryResult<Report> {
  return useQuery({
    queryKey: queryKeys.tally(midnight),
    queryFn: ({ signal }) => getStats({ since: midnight }, signal),
  });
}

/**
 * Tells whether no run is on record: `true` while none is, `undefined` until
 * the first answer. Once a run exists the query is disabled for the rest of
 * the page's life, so no change signal refetches it.
 */
export function useFirstRunQuery(): boolean | undefined {
  const client = useQueryClient();
  const hasRun = client.getQueryData<boolean>(queryKeys.firstRun()) === true;
  const { data } = useQuery({
    queryKey: queryKeys.firstRun(),
    queryFn: ({ signal }) => fetchHasRun(signal),
    enabled: !hasRun,
  });
  return data === undefined ? undefined : !data;
}

/** Reads History pages of issues, newest finished run first; the next page continues from the last one's `next_cursor`. */
export function useHistoryQuery(
  filter: HistoryFilter,
): UseInfiniteQueryResult<InfiniteData<IssueGroupsResponse, string | undefined>> {
  return useInfiniteQuery({
    queryKey: queryKeys.history(filter),
    queryFn: ({ signal, pageParam }) =>
      getIssueGroups({ ...filter, cursor: pageParam, limit: HISTORY_PAGE_SIZE }, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    meta: { refetchFloorMs: HISTORY_REFETCH_FLOOR_MS },
  });
}

/** Reads the runs of one expanded group page by page; it stays idle until `enabled`. */
export function useHistoryRunsQuery(
  issueId: string,
  filter: HistoryFilter,
  enabled: boolean,
): UseInfiniteQueryResult<InfiniteData<RunsResponse, string | undefined>> {
  return useInfiniteQuery({
    queryKey: queryKeys.historyRuns(issueId, filter),
    queryFn: ({ signal, pageParam }) =>
      getRuns(
        {
          issue_id: issueId,
          rule: filter.rule,
          status: filter.status,
          stage: filter.stage,
          limit: HISTORY_RUNS_PAGE_SIZE,
          cursor: pageParam,
        },
        signal,
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    enabled,
    meta: { refetchFloorMs: HISTORY_REFETCH_FLOOR_MS },
  });
}

/**
 * Reads the run statistics of a range. The range is part of the key and no
 * previous data stands in while another range loads, so a range never shows
 * the figures of the one before it.
 */
export function useStatsQuery(range: AnalyticsRange): UseQueryResult<Report> {
  return useQuery({
    queryKey: queryKeys.stats(range),
    queryFn: ({ signal }) => getStats({ since: SINCE_BY_RANGE[range] }, signal),
    meta: { refetchFloorMs: STATS_REFETCH_FLOOR_MS },
  });
}
