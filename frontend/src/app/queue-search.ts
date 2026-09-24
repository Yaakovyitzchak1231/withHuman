export type QueueFilter = "pending" | "resolved" | "all";

/* The queue's layers, each containing the one before it: what the
 * reader was told about, what they may decide, and everything they may
 * see. The server draws the lines; the client only asks for one. */
export type QueueScope = "notified" | "decidable" | "visible";

export const DEFAULT_QUEUE_SCOPE: QueueScope = "notified";

/* Pending and notified are the defaults and stay out of the URL. */
export interface QueueSearch {
  filter?: "resolved" | "all";
  scope?: "decidable" | "visible";
}

export function validateQueueSearch(
  search: Record<string, unknown>,
): QueueSearch {
  const result: QueueSearch = {};
  if (search.filter === "resolved" || search.filter === "all") {
    result.filter = search.filter;
  }
  if (search.scope === "decidable" || search.scope === "visible") {
    result.scope = search.scope;
  }
  return result;
}

/* The search for one view of the queue, defaults left out. */
export function queueSearch(scope: QueueScope, filter: QueueFilter): QueueSearch {
  const search: QueueSearch = {};
  if (filter !== "pending") search.filter = filter;
  if (scope === "decidable" || scope === "visible") search.scope = scope;
  return search;
}

export function queueScopeOf(search: QueueSearch): QueueScope {
  return search.scope ?? DEFAULT_QUEUE_SCOPE;
}
