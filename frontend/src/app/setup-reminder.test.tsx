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
import type { Session, SetupChecklist } from "../types";
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
  return reminder.visible ? (
    <SetupReminderCard items={reminder.items} onDismiss={reminder.dismiss} />
  ) : (
    <p>No reminder</p>
  );
}

const AGENT_ONLY: SetupChecklist = { agent_connected: true, rules_written: false, tested: false };

async function renderAt(path: string, checklist: SetupChecklist | null) {
  const session = {
    principal: { actor_type: "human", actor_id: "u1", organization_id: "org-1", permissions: [], grants: [] },
    assignments: [],
    onboarding: { required: false, checklist, completed_at: "2026-10-01T00:00:00Z" },
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
  it("lists what is left and opens the setup", async () => {
    const router = await renderAt("/queue", AGENT_ONLY);
    const agent = await screen.findByText("Connect an agent");
    expect(agent.parentElement?.textContent).toContain("Done");
    expect(screen.getByText("Write approval rules").parentElement?.textContent).toContain("Not done");
    expect(screen.getByText("Test an approval").parentElement?.textContent).toContain("Not done");
    fireEvent.click(screen.getByRole("button", { name: "Continue setup" }));
    await screen.findByText("No reminder");
    expect(router.state.location.pathname).toBe("/welcome");
  });

  it("stays hidden once dismissed", async () => {
    await renderAt("/queue", AGENT_ONLY);
    fireEvent.click(await screen.findByRole("button", { name: "Hide" }));
    expect(await screen.findByText("No reminder")).toBeTruthy();
    cleanup();
    await renderAt("/queue", AGENT_ONLY);
    expect(await screen.findByText("No reminder")).toBeTruthy();
  });

  it("is not offered once everything is set up", async () => {
    await renderAt("/queue", { agent_connected: true, rules_written: true, tested: true });
    expect(await screen.findByText("No reminder")).toBeTruthy();
  });

  it("is not offered to someone who cannot run the setup", async () => {
    await renderAt("/queue", null);
    expect(await screen.findByText("No reminder")).toBeTruthy();
  });
});
