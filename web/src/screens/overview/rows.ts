/** Gives the keyboard-selection key of an attention item. */
export function attentionNavKey(id: string): string {
  return `attention:${id}`;
}

/** Gives the keyboard-selection key of an Active row. */
export function activeNavKey(issueId: string): string {
  return `active:${issueId}`;
}

/** Gives the keyboard-selection key of an Ended today row. */
export function endedNavKey(issueId: string): string {
  return `ended:${issueId}`;
}
