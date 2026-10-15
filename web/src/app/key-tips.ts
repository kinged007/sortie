/** Writes a control's tooltip, such as "Commands (Ctrl+K)"; the command key reads "⌘K" without a plus. */
export function keyTip(label: string, ...keys: string[]): string {
  const joiner = keys[0] === "⌘" ? "" : "+";
  return `${label} (${keys.join(joiner)})`;
}
