// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { afterEach, describe, expect, it } from "vitest";
import { formatNavCount, NavItem } from "./nav-item";

afterEach(cleanup);

async function renderNav(count: number) {
  const root = createRootRoute({ component: () => <Outlet /> });
  const queue = createRoute({
    getParentRoute: () => root,
    path: "/queue",
    component: () => (
      <NavItem to="/queue" count={count} countLabel="pending requests sent to you">
        Review queue
      </NavItem>
    ),
  });
  const router = createRouter({
    routeTree: root.addChildren([queue]),
    history: createMemoryHistory({ initialEntries: ["/queue"] }),
  });
  await router.load();
  render(<RouterProvider router={router} />);
  return screen.findByRole("link");
}

describe("NavItem count", () => {
  it("shows nothing at zero", async () => {
    const link = await renderNav(0);
    expect(link.textContent).toBe("Review queue");
    expect(screen.queryByTestId("nav-count")).toBeNull();
  });

  it("shows the count with an accessible label", async () => {
    const link = await renderNav(3);
    const pill = screen.getByTestId("nav-count");
    expect(pill.querySelector("[aria-hidden]")?.textContent).toBe("3");
    expect(pill.querySelector(".sr-only")?.textContent).toBe("3 pending requests sent to you");
    expect(link.textContent).toBe("Review queue33 pending requests sent to you");
  });

  it("caps the visible count at 99+", () => {
    expect(formatNavCount(1)).toBe("1");
    expect(formatNavCount(99)).toBe("99");
    expect(formatNavCount(100)).toBe("99+");
  });
});
