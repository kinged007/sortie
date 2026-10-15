const NONE = "<none>";
const UNKNOWN = "<unknown>";

const ORIGIN_LABELS: Record<string, string> = {
  poll_tick: "Picked up by a poll",
  stage_hop: "Stage hop",
  error: "Retry after an error",
  stall: "Retry after a stall",
  continuation: "Continuation",
  ci_fix: "CI fix",
  review: "Review fix",
  "bot-review": "Bot review fix",
  "merge-conflict": "Merge conflict fix",
  "label-review": "Review command",
  "label-fix": "Fix command",
  [NONE]: "CI result",
  [UNKNOWN]: "Not recorded",
};

const TRIGGER_LABELS: Record<string, string> = {
  error: "Retry after an error",
  stall: "Retry after a stall",
  continuation: "Continuation",
  ci_fix: "CI fix",
  [NONE]: "Not a retry",
  [UNKNOWN]: "Not recorded",
};

function worded(labels: Record<string, string>, name: string): string {
  return Object.hasOwn(labels, name) ? labels[name] : name;
}

/** Words a rule name; an absent rule reads "No rule" and any rule the client has no wording for reads as served. */
export function ruleLabel(name: string | undefined): string {
  if (name === undefined || name === "" || name === NONE) {
    return "No rule";
  }
  return name === "default" ? "dispatch.default" : name;
}

/** Words a configured model name; a run that set none reads "Agent default". */
export function modelLabel(name: string): string {
  return name === NONE ? "Agent default" : name;
}

/** Words an agent kind; a run with none reads "No agent". */
export function agentLabel(name: string): string {
  return name === NONE ? "No agent" : name;
}

/** Words a retry trigger or a run origin; a name the client has no wording for reads as served. */
export function startLabel(name: string, breakdown: "trigger" | "origin"): string {
  return worded(breakdown === "trigger" ? TRIGGER_LABELS : ORIGIN_LABELS, name);
}
