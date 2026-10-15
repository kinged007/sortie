/** Joins the truthy class names with single spaces. */
export function cn(...parts: (string | false | null | undefined)[]): string {
  return parts.filter((part) => typeof part === "string" && part !== "").join(" ");
}
