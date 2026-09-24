import { useEffect, useState } from "react";
import { RequestError } from "../api";

/*
 * The reads behind reference cards. A card mounts when its hover card
 * opens and asks for one thing (a member roster, a path's active
 * revision, an agent); many cards on one screen ask for the same thing
 * within seconds, so answers are shared at module scope and kept for a
 * short while. The store is read-only and never the source of truth for
 * a screen: a page that edits the thing refetches for itself, and a
 * card opened after the edit sees the new state once the entry ages out.
 */

const KEEP_MS = 15_000;

interface Entry {
  at: number;
  promise: Promise<unknown>;
}

const entries = new Map<string, Entry>();

/* One shared, short-lived read; a failed read is forgotten at once so
 * the next card tries again. */
export function loadShared<T>(key: string, load: () => Promise<T>): Promise<T> {
  const hit = entries.get(key);
  if (hit && Date.now() - hit.at < KEEP_MS) return hit.promise as Promise<T>;
  const promise = load().catch((reason: unknown) => {
    entries.delete(key);
    throw reason;
  });
  entries.set(key, { at: Date.now(), promise });
  return promise;
}

/* What a card has to show: the value, or why not. missing is the
 * server's 404 (archived, deleted, never existed); forbidden is its
 * 403, for a reader whose permission is scoped narrower than the
 * mention; failed is everything else. */
export type Loaded<T> =
  | { status: "loading" }
  | { status: "ready"; value: T }
  | { status: "missing" }
  | { status: "forbidden" }
  | { status: "failed"; message: string };

export function useShared<T>(key: string, load: () => Promise<T>): Loaded<T> {
  const [state, setState] = useState<{ key: string; loaded: Loaded<T> }>({
    key,
    loaded: { status: "loading" },
  });
  useEffect(() => {
    let cancelled = false;
    loadShared(key, load).then(
      (value) => {
        if (!cancelled) setState({ key, loaded: { status: "ready", value } });
      },
      (reason: unknown) => {
        if (cancelled) return;
        if (reason instanceof RequestError && reason.status === 404) {
          setState({ key, loaded: { status: "missing" } });
        } else if (reason instanceof RequestError && reason.status === 403) {
          setState({ key, loaded: { status: "forbidden" } });
        } else {
          const message = reason instanceof Error ? reason.message : "Could not load";
          setState({ key, loaded: { status: "failed", message } });
        }
      },
    );
    return () => {
      cancelled = true;
    };
    /* load is an inline closure at every call site; key names what it
     * reads, so key alone decides when to read again. */
  }, [key]);
  /* A key that changed under a mounted card is loading again, whatever
   * the previous key had. */
  return state.key === key ? state.loaded : { status: "loading" };
}

/* A shared read a card wants only when the reader holds the permission
 * for it; otherwise the empty value stands in, under its own key so it
 * never shadows a real read. */
export function useSharedIf<T>(allowed: boolean, key: string, load: () => Promise<T>, empty: T): Loaded<T> {
  return useShared(allowed ? key : `${key}:unreadable`, () => (allowed ? load() : Promise.resolve(empty)));
}
