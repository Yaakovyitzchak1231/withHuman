import { useSyncExternalStore } from "react";
import { api } from "../api";

export type RuntimeConfig = Awaited<ReturnType<typeof api.runtimeConfig>>;

/* What this deployment has switched on: fetched once per page load by
 * the root route loader, alongside the session and the page's own data,
 * so the sidebar, the access screen, the billing banner and the invite
 * dialog all render with the answer instead of adjusting to it. */

export interface RuntimeConfigState {
  /* null until loaded, or when the request failed. */
  config: RuntimeConfig | null;
  failed: boolean;
}

let state: RuntimeConfigState = { config: null, failed: false };
const listeners = new Set<() => void>();
let inflight: Promise<void> | null = null;

function set(next: RuntimeConfigState) {
  state = next;
  for (const listener of listeners) listener();
}

/* For the root route loader. A failure is recorded, never thrown: the
 * app renders with hosted-only surfaces hidden and the sign-in screen
 * explains, exactly as it would have with a late failure. */
export function ensureRuntimeConfig(): Promise<void> {
  if (state.config) return Promise.resolve();
  inflight ??= api
    .runtimeConfig()
    .then((config) => set({ config, failed: false }))
    .catch(() => set({ config: null, failed: true }))
    .finally(() => {
      inflight = null;
    });
  return inflight;
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function snapshot() {
  return state;
}

export function useRuntimeConfig(): RuntimeConfigState {
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}
