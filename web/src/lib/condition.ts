import type { StateResponse } from "@/api/types.gen";

/** The one state the status card, narrow bar, title, and favicon follow. */
export type Condition = "degraded" | "needs" | "clear";

const SCM_PROVIDER_NAMES: Record<string, string> = {
  github: "GitHub",
  gitlab: "GitLab",
  gitea: "Gitea",
};

/** Names an SCM provider the way the Health screen titles its check. */
export function scmProviderName(provider: string): string {
  return SCM_PROVIDER_NAMES[provider] ?? provider;
}

/** Names the health checks that fail, in the order the Health screen lists them. */
export function failingChecks(state: StateResponse): string[] {
  const { health } = state;
  if (health === undefined) {
    return [];
  }
  const checks = [
    { name: "Database", check: health.database },
    { name: "Dispatch preflight", check: health.preflight },
    { name: health.workflow.file ?? "Workflow file", check: health.workflow },
    { name: "Tracker poll", check: health.poll },
    { name: health.scm === null ? "" : scmProviderName(health.scm.provider), check: health.scm },
  ];
  return checks
    .filter(({ check }) => check !== null && check.status === "fail")
    .map(({ name }) => name);
}

/** Derives the condition: a failing check outranks attention items, which outrank all clear. */
export function condition(state: StateResponse): Condition {
  if (failingChecks(state).length > 0) {
    return "degraded";
  }
  return state.counts.attention > 0 ? "needs" : "clear";
}

/** Words the condition for the status card and the title. */
export function conditionWord(c: Condition): string {
  switch (c) {
    case "degraded":
      return "Degraded";
    case "needs":
      return "Needs attention";
    case "clear":
      return "All clear";
  }
}
