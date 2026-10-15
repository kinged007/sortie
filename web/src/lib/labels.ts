const NONE = "<none>";
const UNKNOWN = "<unknown>";

const ORIGIN_LABELS: Record<string, string> = {
  poll_tick: "Picked up by a poll",
  stage_hop: "Stage hop",
  error: "Retry after an error",
  stall: "Retry after a stall",
  continuation: "Continuation",
  ci_fix: "CI fix",
  ci: "CI fix",
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

const REACTION_LABELS: Record<string, string> = {
  ci_failure: "CI",
  review_comments: "Review",
  bot_review: "Bot review",
  merge_conflicts: "Conflicts",
  auto_merge: "Merge",
  merge_completion: "Merge completion",
  label_commands: "Label commands",
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

/** Words a model name; a run that neither configured nor reported one reads "Not reported". */
export function modelLabel(name: string): string {
  return name === NONE ? "Not reported" : name;
}

/** Words an agent kind; a run with none reads "No agent". */
export function agentLabel(name: string): string {
  return name === NONE ? "No agent" : name;
}

/** Words a retry trigger or a run origin; a name the client has no wording for reads as served. */
export function startLabel(name: string, breakdown: "trigger" | "origin"): string {
  return worded(breakdown === "trigger" ? TRIGGER_LABELS : ORIGIN_LABELS, name);
}

/** Words a reaction config key; a key the client has no wording for reads as served. */
export function reactionLabel(configKey: string): string {
  return worded(REACTION_LABELS, configKey);
}

const REACTION_SHORT_LABELS: Record<string, string> = {
  bot_review: "Bot",
  merge_conflicts: "Confl.",
  merge_completion: "Completion",
  label_commands: "Commands",
};

/** Words a reaction config key in the form a narrow station header uses; a key whose label is already short has none. */
export function reactionShortLabel(configKey: string): string | undefined {
  return Object.hasOwn(REACTION_SHORT_LABELS, configKey)
    ? REACTION_SHORT_LABELS[configKey]
    : undefined;
}
