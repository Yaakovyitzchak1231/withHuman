export function argumentPreview(args: unknown): string {
  if (typeof args === "string") return args;
  if (args && typeof args === "object" && !Array.isArray(args)) {
    const entries = Object.entries(args as Record<string, unknown>);
    if (entries.length === 1 && typeof entries[0][1] === "string") {
      return entries[0][1] as string;
    }
    return entries
      .map(
        ([key, value]) =>
          `${key}: ${typeof value === "string" ? value : JSON.stringify(value)}`,
      )
      .join("   ");
  }
  return JSON.stringify(args);
}

function compactDuration(seconds: number): string {
  const clamped = Math.max(1, seconds);
  if (clamped < 60) return `${clamped}s`;
  if (clamped < 3600) return `${Math.floor(clamped / 60)}m`;
  if (clamped < 86400) return `${Math.floor(clamped / 3600)}h`;
  return `${Math.floor(clamped / 86400)}d`;
}

export function formatRelative(value: string): string {
  return compactDuration(
    Math.round((Date.now() - new Date(value).getTime()) / 1000),
  );
}

/* Time remaining until a future instant. */
export function formatUntil(value: string): string {
  return compactDuration(
    Math.round((new Date(value).getTime() - Date.now()) / 1000),
  );
}

/* Enum values arrive lowercase on the wire; display labels are
 * capitalized, matching the queue's status labels. */
export function capitalize(value: string): string {
  return value ? value[0].toUpperCase() + value.slice(1) : value;
}

/* Exact local date and time, the way the detail view prints every
 * timestamp: "Sep 7, 2026, 14:32" in an en-US locale. */
export function formatAbsolute(value: string): string {
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(value));
}

/* A duration in the two largest units that matter: "18h 20m", "3d 2h",
 * "45m", "12s". Under a minute only seconds show. */
export function formatSpan(seconds: number): string {
  const total = Math.max(0, Math.round(seconds));
  if (total < 60) return `${total}s`;
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  if (days > 0) return hours > 0 ? `${days}d ${hours}h` : `${days}d`;
  if (hours > 0) return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`;
  return `${minutes}m`;
}

/* How long ago an instant was, as a two-unit span. */
export function formatElapsed(value: string, now = Date.now()): string {
  return formatSpan((now - new Date(value).getTime()) / 1000);
}

/* How long until an instant, as a two-unit span; zero once it passed. */
export function formatRemaining(value: string, now = Date.now()): string {
  return formatSpan((new Date(value).getTime() - now) / 1000);
}

/* The span between two instants, oldest first. */
export function formatBetween(from: string, to: string): string {
  return formatSpan((new Date(to).getTime() - new Date(from).getTime()) / 1000);
}

/* A tool name as a sentence: "kubernetes.deployments.rollout" reads
 * "Kubernetes deployments rollout", "create_issue" reads "Create
 * issue". Separators and camelCase boundaries become
 * spaces; the first letter is capitalized and the rest left alone so
 * acronyms and product names survive. */
export function humanizeTool(tool: string): string {
  const words = tool
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .split(/[._:/\-\s]+/)
    .filter(Boolean);
  if (words.length === 0) return tool;
  const sentence = words.join(" ");
  return sentence[0].toUpperCase() + sentence.slice(1);
}

/* An argument key as a label: "max_unavailable" reads "Max unavailable",
 * "customerId" reads "Customer id". */
export function humanizeKey(key: string): string {
  return humanizeTool(key).toLowerCase().replace(/^./, (c) => c.toUpperCase());
}

/* "1 request", "3 requests". */
export function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

/* The first eight characters of an id, for a mention with no name. */
export function shortId(id: string): string {
  return id.slice(0, 8);
}
