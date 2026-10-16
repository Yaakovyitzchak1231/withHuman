// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TooltipProvider } from "@/ds/components/tooltip";
import type { Session } from "../types";
import { SessionContext } from "./principal";
import { SetupReminderCard, useSetupReminder } from "./setup-reminder";
import { stubWebStorage } from "./test-storage";

beforeEach(stubWebStorage);
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function Probe() {
  const reminder = useSetupReminder();
  return reminder.visible ? <SetupReminderCard onDismiss={reminder.dismiss} /> : <p>No reminder</p>;
}

async function renderAt(path: string, skipped: boolean) {
  const session = {
    principal: { actor_type: "human", actor_id: "u1", organization_id: "org-1", permissions: [], grants: [] },
    assignments: [],
    onboarding: { required: false, skipped, completed_at: "2026-10-01T00:00:00Z" },
    signOut: () => {},
  } as Session & { signOut: () => void };
  const root = createRootRoute({
    component: () => (
      <SessionContext.Provider value={session}>
        <TooltipProvider>
          <Probe />
        </TooltipProvider>
        <Outlet />
      </SessionContext.Provider>
    ),
  });
  const pages = ["/queue", "/welcome"].map((page) =>
    createRoute({ getParentRoute: () => root, path: page, component: () => null }),
  );
  const router = createRouter({
    routeTree: root.addChildren(pages),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  await router.load();
  render(<RouterProvider router={router} />);
  return router;
}

describe("setup reminder", () => {
  it("offers a skipped setup and opens it", async () => {
    const router = await renderAt("/queue", true);
    fireEvent.click(await screen.findByRole("button", { name: "Continue setup" }));
    await screen.findByText("No reminder");
    expect(router.state.location.pathname).toBe("/welcome");
  });

  it("stays hidden once dismissed", async () => {
    await renderAt("/queue", true);
    fireEvent.click(await screen.findByRole("button", { name: "Hide" }));
    expect(await screen.findByText("No reminder")).toBeTruthy();
    cleanup();
    await renderAt("/queue", true);
    expect(await screen.findByText("No reminder")).toBeTruthy();
  });

  it("is not offered when the setup was not skipped", async () => {
    await renderAt("/queue", false);
    expect(await screen.findByText("No reminder")).toBeTruthy();
  });
});
