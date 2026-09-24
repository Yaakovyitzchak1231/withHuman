/* What every edition's main.tsx hands createRouter, and the deploy
 * safety net around it, so the hosted overlay and the open bundle start
 * the same way. */

/* Data loads before a page renders, never after: route loaders start on
 * hover or focus (intent) and the previous page stays put while the next
 * one loads. Loaded data stays fresh for a short while, so hovering the
 * same link again or revisiting a page just opened does not refetch;
 * after that a revisit renders the cached data instantly while the
 * loader refreshes it. A pending indicator only appears once a load has
 * stalled past the threshold, and then stays long enough not to flash. */
const FRESH_MS = 10_000;

export const ROUTER_OPTIONS = {
  defaultPreload: "intent",
  defaultPreloadStaleTime: FRESH_MS,
  defaultStaleTime: FRESH_MS,
  defaultPendingMs: 300,
  defaultPendingMinMs: 300,
} as const;

/* A deploy replaces every hashed chunk the server carries. A tab that
 * loaded the previous index.html then fails to fetch a route chunk on its
 * next navigation. Reload once so the tab picks up the new index; the
 * session storage flag stops a loop if the reload fails the same way. */
export function reloadOncePerDeploy(): void {
  window.addEventListener("vite:preloadError", (event) => {
    const key = "wh-preload-reloaded";
    try {
      if (sessionStorage.getItem(key) === window.location.href) return;
      sessionStorage.setItem(key, window.location.href);
    } catch {
      /* storage unavailable: still worth one reload */
    }
    event.preventDefault();
    window.location.reload();
  });
}
