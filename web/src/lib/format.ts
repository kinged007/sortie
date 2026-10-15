import { localMidnight, now } from "@/lib/server-clock";

type Instant = string | number | Date;

const UNKNOWN = "unknown";

/** Parses an RFC 3339 instant to epoch milliseconds; absent or unparsable input is `null`. */
export function parseInstant(value: string | null | undefined): number | null {
  if (value === null || value === undefined) {
    return null;
  }
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? null : parsed;
}

/** Words a span of seconds in at most two units; a missing or non-finite span reads "unknown". */
export function fmtDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds)) {
    return UNKNOWN;
  }
  if (seconds < 1) {
    return "under 1s";
  }
  if (seconds < 10) {
    return `${(Math.round(seconds * 10) / 10).toFixed(1).replace(/\.0$/, "")}s`;
  }
  if (seconds < 59.5) {
    return `${Math.round(seconds)}s`;
  }
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) {
    return `${minutes}m`;
  }
  const hours = Math.floor(minutes / 60);
  if (minutes < 24 * 60) {
    return unitPair(hours, "h", minutes % 60, "m");
  }
  return unitPair(Math.floor(hours / 24), "d", hours % 24, "h");
}

function unitPair(major: number, majorUnit: string, minor: number, minorUnit: string): string {
  const text = `${major}${majorUnit}`;
  return minor === 0 ? text : `${text} ${minor}${minorUnit}`;
}

/** Words a moment a number of seconds ahead; a moment at hand or past reads "due now". */
export function fmtDue(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || Number.isNaN(seconds)) {
    return UNKNOWN;
  }
  return seconds < 1 ? "due now" : `in ${fmtDuration(seconds)}`;
}

/** Words a token figure; a missing figure reads "not measured". */
export function fmtTokens(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n)) {
    return "not measured";
  }
  if (n >= 999_500) {
    return millions(n);
  }
  if (n >= 1_000) {
    return `${Math.floor((n + 500) / 1_000)}K`;
  }
  return String(Math.trunc(n));
}

function millions(n: number): string {
  const hundredths = Math.floor((n + 5_000) / 10_000);
  const whole = Math.floor(hundredths / 100);
  const frac = hundredths % 100;
  if (frac === 0) {
    return `${whole}M`;
  }
  if (frac % 10 === 0) {
    return `${whole}.${frac / 10}M`;
  }
  return `${whole}.${String(frac).padStart(2, "0")}M`;
}

/** Writes an integer with comma-separated thousands. */
export function fmtCount(n: number): string {
  const digits = String(Math.abs(Math.trunc(n)));
  const grouped = digits.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return n < 0 ? `-${grouped}` : grouped;
}

/** Writes the count and the noun form the count calls for. */
export function plural(n: number, one: string, many: string): string {
  return `${fmtCount(n)} ${n === 1 ? one : many}`;
}

/** A count with the noun form and the verb form the count calls for. */
export interface Agreement {
  /** The count as written, with thousands separators. */
  count: string;
  noun: string;
  verb: string;
  /** The count and the noun, as `plural` writes them. */
  subject: string;
}

/** Picks the noun and the verb form the count calls for, so "1 agent is" and "4 agents are" never disagree. */
export function pluralVerb(
  n: number,
  nouns: readonly [one: string, many: string],
  verbs: readonly [one: string, many: string],
): Agreement {
  const [noun, verb] = n === 1 ? [nouns[0], verbs[0]] : [nouns[1], verbs[1]];
  return { count: fmtCount(n), noun, verb, subject: plural(n, nouns[0], nouns[1]) };
}

/** Writes dollars with two decimals below $100 and as whole dollars with thousands separators from $100. */
export function fmtMoney(usd: number): string {
  const cents = Math.round(usd * 100);
  if (cents >= 10_000) {
    return `$${fmtCount(Math.round(usd))}`;
  }
  return `$${(cents / 100).toFixed(2)}`;
}

const RATE = new Intl.NumberFormat("en-US", {
  minimumFractionDigits: 2,
  maximumFractionDigits: 20,
  useGrouping: false,
});

/** Writes a configured USD rate unrounded, with at least two decimals and no thousands separator: 3 reads "$3.00", 0.3 "$0.30", 0.075 "$0.075". */
export function fmtRate(usd: number): string {
  return `$${RATE.format(usd)}`;
}

function toDate(at: Instant): Date | null {
  const date = at instanceof Date ? at : new Date(at);
  return Number.isNaN(date.getTime()) ? null : date;
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

/**
 * Writes a share of runs as a whole percent. A ratio strictly between 0 and 1
 * never rounds to either end, so "100%" means every run and "0%" none.
 */
export function fmtPercent(ratio: number): string {
  const percent = Math.round(ratio * 100);
  const isPartial = ratio > 0 && ratio < 1;
  return `${isPartial ? Math.min(99, Math.max(1, percent)) : percent}%`;
}

/** Writes a mean with one decimal place, "4.0" included. */
export function fmtMean(n: number): string {
  return n.toFixed(1);
}

/** Writes 24-hour `HH:MM` in the browser's time zone. */
export function fmtClock(at: Instant): string {
  const date = toDate(at);
  return date === null ? UNKNOWN : `${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/** Writes 24-hour `HH:MM:SS` in the browser's time zone. */
export function fmtClockSec(at: Instant): string {
  const date = toDate(at);
  return date === null ? UNKNOWN : `${fmtClock(date)}:${pad(date.getSeconds())}`;
}

const WEEKDAY = new Intl.DateTimeFormat("en-US", { weekday: "long" });
const MONTH = new Intl.DateTimeFormat("en-US", { month: "long" });

/** Writes the date as "Monday 12 October" in the browser's time zone. */
export function fmtDate(at: Instant): string {
  const date = toDate(at);
  if (date === null) {
    return UNKNOWN;
  }
  return `${WEEKDAY.format(date)} ${date.getDate()} ${MONTH.format(date)}`;
}

/** Writes the age of an instant as a duration under an hour, else as its clock time. */
export function fmtAgo(since: Instant, at: number = now()): string {
  const date = toDate(since);
  if (date === null) {
    return UNKNOWN;
  }
  const seconds = (at - date.getTime()) / 1000;
  return seconds < 3600 ? `${fmtDuration(seconds)} ago` : fmtClock(date);
}

/** Writes the clock time of an instant on the server's current day, else the date and the clock time. */
export function fmtStamp(at: string): string {
  const instant = parseInstant(at);
  if (instant === null) {
    return UNKNOWN;
  }
  const isToday = localMidnight(instant).getTime() === localMidnight(now()).getTime();
  return isToday ? fmtClock(instant) : `${fmtDate(instant)}, ${fmtClock(instant)}`;
}

/** Writes `HH:MM:SS` for an instant on the server's current day, else the date and `HH:MM:SS`, as fmtStamp does with `HH:MM`. */
export function fmtStampSec(at: string): string {
  const instant = parseInstant(at);
  if (instant === null) {
    return UNKNOWN;
  }
  const isToday = localMidnight(instant).getTime() === localMidnight(now()).getTime();
  return isToday ? fmtClockSec(instant) : `${fmtDate(instant)}, ${fmtClockSec(instant)}`;
}

const DESCRIBE_SUFFIX = /-\d+-g[0-9a-f]+(-dirty)?$/;
const DIRTY_SUFFIX = /-dirty$/;

/** Words a served version for the rail: a local build's git-describe suffix is dropped and a bare number gains its "v". */
export function fmtVersion(version: string): string {
  const release = version.replace(DESCRIBE_SUFFIX, "").replace(DIRTY_SUFFIX, "");
  return /^\d/.test(release) ? `v${release}` : release;
}
