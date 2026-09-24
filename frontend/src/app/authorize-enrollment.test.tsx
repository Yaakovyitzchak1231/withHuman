// @vitest-environment jsdom
import type { ComponentType, ReactNode } from "react";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { Agent } from "../types";
import {
  AuthorizeEnrollmentScreen,
  type EnrollmentPage,
} from "./authorize-enrollment";

const { allowed, toast } = vi.hoisted(() => ({
  allowed: { issue: true, create: true },
  toast: vi.fn(),
}));
vi.mock("@tanstack/react-router", () => ({
  createLink: (component: ComponentType) => component,
  useNavigate: () => vi.fn(),
  Link: ({ children }: { children: ReactNode }) => <a>{children}</a>,
}));
vi.mock("./principal", () => ({
  useSession: () => ({ organization: { name: "Example" } }),
}));
vi.mock("./authorization", () => ({
  useAccess: () => ({
    holdsAnywhere: (permission: string) =>
      permission === "agent.write" ? allowed.create : allowed.issue,
    can: (permission: string) =>
      permission === "agent.write" ? allowed.create : allowed.issue,
  }),
}));
vi.mock("@/ds/components/toast", () => ({ useToast: () => ({ add: toast }) }));

const coding = {
  slug: "coding-agents",
  name: "Coding agents",
  status: "active",
} as Agent;
const page: EnrollmentPage = {
  preview: {
    runtime: "claude-code",
    instance_name: "desktop",
    status: "pending",
    expires_at: "2099-01-01T00:00:00Z",
  },
  agents: [coding],
  error: "",
};

beforeEach(() => {
  allowed.issue = true;
  allowed.create = true;
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function setup(data: EnrollmentPage = page) {
  const onSearch = vi.fn().mockResolvedValue(undefined);
  render(
    <AuthorizeEnrollmentScreen
      page={data}
      search={{ user_code: "ABC-1234" }}
      onSearch={onSearch}
    />,
  );
  return { onSearch };
}

describe("CLI enrollment authorization", () => {
  it("requires an explicit selection and confirmation", async () => {
    const authorize = vi
      .spyOn(api, "authorizeAgentEnrollment")
      .mockResolvedValue({} as never);
    setup();
    expect(
      (
        screen.getByRole("button", {
          name: "Connect instance",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    fireEvent.click(screen.getByRole("radio", { name: "Coding agents coding-agents" }));
    expect(authorize).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Connect instance" }));
    await screen.findByText("Instance authorized");
    expect(authorize).toHaveBeenCalledWith("ABC-1234", "coding-agents");
  });

  it("fixes the CLI-requested slug and offers no alternate target or creation", async () => {
    const authorize = vi
      .spyOn(api, "authorizeAgentEnrollment")
      .mockResolvedValue({} as never);
    setup({
      ...page,
      preview: {
        ...page.preview!,
        requested_agent_slug: coding.slug,
        agent: coding,
      },
    });
    expect(screen.getAllByText("coding-agents").length).toBeGreaterThan(0);
    expect(screen.queryByRole("radio")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Create new agent" }),
    ).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Connect instance" }));
    await screen.findByText("Instance authorized");
    expect(authorize).toHaveBeenCalledWith("ABC-1234", "coding-agents");
  });

  it("creates a parent first, retains it after authorization failure, and retries that target", async () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue(coding);
    const authorize = vi
      .spyOn(api, "authorizeAgentEnrollment")
      .mockRejectedValueOnce(new Error("Temporarily unavailable"))
      .mockResolvedValueOnce({} as never);
    setup({ ...page, agents: [] });
    fireEvent.click(screen.getByRole("button", { name: "Create new agent" }));
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Coding agents" },
    });
    expect((screen.getByLabelText("Slug") as HTMLInputElement).value).toBe("coding-agents");
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await screen.findByRole("button", { name: "Connect instance" });
    expect(create).toHaveBeenCalledWith("coding-agents", "Coding agents");
    expect(authorize).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Connect instance" }));
    await screen.findByRole("alert");
    fireEvent.click(screen.getByRole("button", { name: "Connect instance" }));
    await screen.findByText("Instance authorized");
    expect(create).toHaveBeenCalledTimes(1);
    expect(authorize).toHaveBeenNthCalledWith(2, "ABC-1234", "coding-agents");
  });

  it("keeps the code when searching and paging", async () => {
    const { onSearch } = setup({ ...page, next_offset: 25 });
    fireEvent.change(screen.getByLabelText("Search agents"), {
      target: { value: "operations" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() =>
      expect(onSearch).toHaveBeenCalledWith({
        user_code: "ABC-1234",
        q: "operations",
        offset: 0,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(onSearch).toHaveBeenCalledWith({
      user_code: "ABC-1234",
      offset: 25,
    });
  });

  it("withholds connection actions without issuance permission", () => {
    allowed.issue = false;
    setup();
    expect(screen.getByText("Permission required")).toBeTruthy();
    expect(
      screen.queryByRole("button", { name: "Connect instance" }),
    ).toBeNull();
  });

  it("shows completed authorization on reload without issuing another request", () => {
    const authorize = vi.spyOn(api, "authorizeAgentEnrollment");
    setup({
      ...page,
      preview: { ...page.preview!, status: "consumed", agent: coding },
    });
    expect(screen.getByText("Instance authorized")).toBeTruthy();
    expect(authorize).not.toHaveBeenCalled();
  });
});
