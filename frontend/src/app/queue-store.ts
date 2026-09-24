import { useCallback, useSyncExternalStore } from "react";
import { api, APP_API_BASE } from "../api";
import type { ApprovalRequest, ApprovalReview } from "../types";
import { DEFAULT_QUEUE_SCOPE, type QueueScope } from "./queue-search";

/* The live review queue, shared by every screen that shows it.
 *
 * One snapshot per queue layer lives at module scope for the life of the
 * tab. Route loaders await the first snapshot of the layer they show, so
 * a queue screen never paints its empty state on the way in; the events
 * stream (which sends a full snapshot on connect and on every change)
 * keeps a layer current while any screen is subscribed to it, and a
 * screen that comes back later renders the last snapshot instantly while
 * the stream reconnects. Switching layers switches streams; the layer
 * left behind keeps its snapshot for the next visit. */

export interface QueueState {
  requests: ApprovalRequest[];
  /* True once any snapshot has arrived: tells "nothing yet" from
   * "nothing". */
  loaded: boolean;
  liveError: string;
}

const DISCONNECTED = "Live updates disconnected. Reconnecting automatically.";
/* A stream stays open this long after the last subscriber leaves, so
 * moving between the list and a request, or a quick detour to another
 * page, does not tear the connection down and back up. */
const IDLE_CLOSE_MS = 5_000;

interface Layer {
  scope: QueueScope;
  state: QueueState;
  listeners: Set<() => void>;
  subscribers: number;
  source: EventSource | null;
  idleClose: ReturnType<typeof setTimeout> | null;
  inflight: Promise<void> | null;
}

const layers = new Map<QueueScope, Layer>();

function layer(scope: QueueScope): Layer {
  let found = layers.get(scope);
  if (!found) {
    found = {
      scope,
      state: { requests: [], loaded: false, liveError: "" },
      listeners: new Set(),
      subscribers: 0,
      source: null,
      idleClose: null,
      inflight: null,
    };
    layers.set(scope, found);
  }
  return found;
}

function set(target: Layer, patch: Partial<QueueState>) {
  target.state = { ...target.state, ...patch };
  for (const listener of target.listeners) listener();
}

/* Fetches a fresh snapshot of one layer. Errors land in liveError rather
 * than throwing, so a route loader never trips the router's error
 * boundary (the session gate handles an expired session). */
export function refreshQueue(scope: QueueScope = DEFAULT_QUEUE_SCOPE): Promise<void> {
  const target = layer(scope);
  if (target.inflight) return target.inflight;
  target.inflight = api
    .approvals(scope)
    .then(({ requests }) => set(target, { requests, loaded: true, liveError: "" }))
    .catch((reason: Error) => set(target, { liveError: reason.message }))
    .finally(() => {
      target.inflight = null;
    });
  return target.inflight;
}

/* For route loaders: resolves as soon as a snapshot of the layer exists.
 * A revisit resolves immediately; the stream brings anything newer. */
export function ensureQueue(scope: QueueScope = DEFAULT_QUEUE_SCOPE): Promise<void> {
  return layer(scope).state.loaded ? Promise.resolve() : refreshQueue(scope);
}

function connect(target: Layer) {
  if (target.source) return;
  const source = new EventSource(
    `${APP_API_BASE}/events?${new URLSearchParams({ scope: target.scope })}`,
  );
  source.addEventListener("queue", (event) => {
    const requests = JSON.parse(
      (event as MessageEvent<string>).data,
    ) as ApprovalRequest[];
    set(target, { requests, loaded: true, liveError: "" });
  });
  source.onerror = () => set(target, { liveError: DISCONNECTED });
  target.source = source;
}

function disconnect(target: Layer) {
  target.source?.close();
  target.source = null;
}

function subscribe(target: Layer, listener: () => void) {
  target.listeners.add(listener);
  target.subscribers += 1;
  if (target.idleClose) {
    clearTimeout(target.idleClose);
    target.idleClose = null;
  }
  connect(target);
  return () => {
    target.listeners.delete(listener);
    target.subscribers -= 1;
    if (target.subscribers === 0 && !target.idleClose) {
      target.idleClose = setTimeout(() => {
        target.idleClose = null;
        if (target.subscribers === 0) disconnect(target);
      }, IDLE_CLOSE_MS);
    }
  };
}

/* Subscribes the calling component to one live layer of the queue. */
export function useQueue(scope: QueueScope = DEFAULT_QUEUE_SCOPE): QueueState {
  const subscribeLayer = useCallback(
    (listener: () => void) => subscribe(layer(scope), listener),
    [scope],
  );
  const snapshot = useCallback(() => layer(scope).state, [scope]);
  return useSyncExternalStore(subscribeLayer, snapshot, snapshot);
}

/* Review context a route loader fetched ahead of the detail panel, so
 * the reviewer and activity sections render with the request instead of
 * arriving a beat later. The panel still refetches on mount to follow
 * status and claim changes; identical data costs no layout. */
const reviews = new Map<string, ApprovalReview>();

export function preloadReview(id: string): Promise<void> {
  return api
    .approvalReview(id)
    .then((review) => {
      reviews.set(id, review);
    })
    .catch(() => undefined);
}

export function preloadedReview(id: string): ApprovalReview | null {
  return reviews.get(id) ?? null;
}
