import type { ConfigResponse, IssueDetailResponse, IssueHistoryResponse } from "@/api/types.gen";

/** An estimated cost and the served status that says how far to trust it. */
export interface CostFigure {
  usd: number | null;
  status: string;
}

/** One budgeted figure: what was used, the limit, and whether the limit is known at all. */
export interface BudgetLine {
  used: number | null;
  limit: number | null;
  /** False only while the limit would come from a `config` that has not loaded. */
  limitKnown: boolean;
}

/** The budget of one issue: sessions, tokens, and the estimated cost of the spend. */
export interface BudgetFigures {
  sessions: BudgetLine;
  tokens: BudgetLine & { complete: boolean };
  /** The tokens of the issue without cache reads; null while the spend is not known. */
  uncachedTokens: number | null;
  /** Null while `config` or its `token_rates` is absent, and when the issue has no recorded run. */
  cost: CostFigure | null;
}

function totalsCost(history: IssueHistoryResponse | null): CostFigure | null {
  return history === null
    ? null
    : { usd: history.totals.estimated_cost_usd, status: history.totals.cost_status };
}

function uncachedTotal(history: IssueHistoryResponse | null): number | null {
  return history === null ? 0 : history.totals.uncached_tokens;
}

function figuresOf(detail: IssueDetailResponse, config: ConfigResponse | undefined): BudgetFigures {
  const { running, budget_exhausted: exhausted, history } = detail;
  if (detail.status === "running" && running !== null) {
    return {
      sessions: { used: running.used_sessions, limit: running.budget_sessions, limitKnown: true },
      tokens: {
        used: running.used_tokens,
        limit: running.budget_tokens,
        limitKnown: true,
        complete: running.used_tokens_complete,
      },
      uncachedTokens: running.issue_uncached_tokens,
      cost: { usd: running.issue_estimated_cost_usd, status: running.issue_cost_status },
    };
  }
  if (detail.status === "budget_exhausted" && exhausted !== null) {
    return {
      sessions: {
        used: exhausted.used_sessions,
        limit: exhausted.budget_sessions,
        limitKnown: true,
      },
      tokens: {
        used: exhausted.used_tokens,
        limit: exhausted.budget_tokens,
        limitKnown: true,
        complete: exhausted.unmeasured_sessions === 0,
      },
      uncachedTokens: uncachedTotal(history),
      cost: totalsCost(history),
    };
  }
  const isKnown = config !== undefined;
  return {
    sessions: {
      used: history === null ? 0 : history.totals.run_count,
      limit: config?.agent.max_sessions ?? null,
      limitKnown: isKnown,
    },
    tokens: {
      used: history === null ? 0 : history.totals.tokens.total_tokens,
      limit: config?.agent.max_tokens ?? null,
      limitKnown: isKnown,
      complete: history === null ? true : history.totals.tokens_complete,
    },
    uncachedTokens: uncachedTotal(history),
    cost: totalsCost(history),
  };
}

/**
 * Gives the budget figures of an issue from the source its status names: the
 * running entry, the budget stop, or the recorded runs and the configured
 * limits. A figure Sortie does not know stays null, never zero.
 */
export function budgetOf(
  detail: IssueDetailResponse,
  config: ConfigResponse | undefined,
): BudgetFigures {
  const figures = figuresOf(detail, config);
  const isPriced = config !== undefined && config.token_rates !== null;
  return isPriced ? figures : { ...figures, cost: null };
}
