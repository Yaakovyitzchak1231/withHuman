/* Go duration strings on the wire ("24h0m0s", "15m0s", "90s"), seconds
 * in the app. The DurationField edits seconds; the policy endpoints
 * speak Go's format both ways. */

const UNIT_SECONDS: Record<string, number> = {
  h: 3600,
  m: 60,
  s: 1,
  ms: 0.001,
  us: 0.000001,
  "µs": 0.000001,
  ns: 0.000000001,
};

/* Seconds in a Go duration string, or null when it does not parse. A
 * bare "0" is Go's zero duration. */
export function parseGoDuration(text: string): number | null {
  const trimmed = text.trim();
  if (trimmed === "0") return 0;
  const pattern = /(\d+(?:\.\d+)?)(h|ms|m|s|us|µs|ns)/gy;
  let total = 0;
  let consumed = 0;
  for (const match of trimmed.matchAll(pattern)) {
    if (match.index !== consumed) return null;
    total += Number(match[1]) * UNIT_SECONDS[match[2]];
    consumed = match.index + match[0].length;
  }
  if (consumed === 0 || consumed !== trimmed.length) return null;
  return total;
}

/* The shortest Go duration string for a whole number of seconds: "24h"
 * rather than "24h0m0s", "90m" rather than "1h30m", so the value reads
 * the way the person typed it. */
export function formatGoDuration(seconds: number): string {
  const whole = Math.max(0, Math.round(seconds));
  if (whole === 0) return "0s";
  if (whole % 3600 === 0) return `${whole / 3600}h`;
  if (whole % 60 === 0) return `${whole / 60}m`;
  return `${whole}s`;
}
