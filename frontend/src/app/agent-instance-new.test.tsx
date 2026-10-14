// @vitest-environment jsdom
import type { ComponentType, ReactNode } from "react";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, RequestError } from "../api";
import type { AgentDetail, Principal, Resource } from "../types";
import { can } from "./authorization";
import {
  loadNewAgentInstancePage,
  NewAgentInstanceScreen,
} from "./agent-instance-new";
import type { RuntimeConfig } from "./use-runtime-config";

const mocks = vi.hoisted(() => ({
  invalidate: vi.fn().mockResolvedValue(undefined),
  can: vi.fn(),
  config: null as RuntimeConfig | null,
}));
vi.mock("./use-runtime-config", () => ({
  useRuntimeConfig: () => ({ config: mocks.config }),
}));
vi.mock("./authorization", async (original) => ({
  ...(await original<typeof import("./authorization")>()),
  useAccess: () => ({ can: mocks.can }),
}));
vi.mock("@tanstack/react-router", () => ({
  createLink: (component: ComponentType) => component,
  useRouter: () => ({ invalidate: mocks.invalidate }),
  Link: ({
    children,
    to,
    params,
    search,
  }: {
    children: ReactNode;
    to: string;
    params: { agentSlug: string };
    search: { tab: string };
  }) => (
    <a href={`${to.replace("$agentSlug", params.agentSlug)}?tab=${search.tab}`}>
      {children}
    </a>
  ),
}));

const agent = {
  slug: "support",
  name: "Support assistant",
  status: "active",
} as AgentDetail;
const issued = {
  agent_instance: { id: "4f1c2b7e-8d3a-4c55-9b1e-2a7d6f0c9e31", name: "support-server-01" },
  credential: { token: "whc_test_secret", expires_at: "2027-01-01T00:00:00Z" },
} as Awaited<ReturnType<typeof api.createAgentInstance>>;

beforeEach(() => {
  mocks.can.mockReturnValue(true);
  mocks.config = {
    public_origin: "https://app.example.test",
    cli_download_url: "https://downloads.withhuman.ai",
  } as RuntimeConfig;
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  mocks.can.mockReset();
  mocks.invalidate.mockClear();
});

function show(value = agent) {
  return render(
    <NewAgentInstanceScreen
      agentSlug="support"
      page={{ agent: value, error: "" }}
    />,
  );
}
function selectKey() {
  fireEvent.click(screen.getByRole("radio", { name: /Generate an API key/ }));
  fireEvent.change(screen.getByRole("textbox", { name: "Instance name" }), {
    target: { value: "  support-server-01  " },
  });
}

describe("new instance page", () => {
  it("starts with two unselected choices and creates nothing when viewing CLI instructions", () => {
    const create = vi.spyOn(api, "createAgentInstance");
    show();
    expect(
      screen
        .getAllByRole("radio")
        .every((radio) => radio.getAttribute("aria-checked") === "false"),
    ).toBe(true);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("textbox", { name: "Instance name" })).toBeNull();
    fireEvent.click(
      screen.getByRole("radio", { name: /Install with the CLI/ }),
    );
    expect(screen.getByText(/curl -fsSL/).textContent).toContain(
      "--agent support --url https://app.example.test",
    );
    expect(
      screen.getByRole("link", { name: "View instances" }).getAttribute("href"),
    ).toBe("/agents/support?tab=instances");
    expect(create).not.toHaveBeenCalled();
  });

  it("opens setup help without selecting a method or issuing credentials", async () => {
    const create = vi.spyOn(api, "createAgentInstance");
    show();
    fireEvent.click(screen.getByRole("button", { name: "About Generate an API key" }));
    const guide = await screen.findByRole("link", { name: /Generate an API key guide/ });
    expect(guide.getAttribute("href")).toBe("https://withhuman.ai/docs/web-app/agents#api-key-setup");
    expect(screen.getAllByRole("radio").every((radio) => radio.getAttribute("aria-checked") === "false")).toBe(true);
    expect(screen.queryByRole("textbox", { name: "Instance name" })).toBeNull();
    expect(create).not.toHaveBeenCalled();
  });

  it("generates once and shows the key, provider URL, name, expiry and instructions", async () => {
    let resolve!: (value: typeof issued) => void;
    const create = vi.spyOn(api, "createAgentInstance").mockImplementation(
      () =>
        new Promise((done) => {
          resolve = done;
        }),
    );
    const view = show();
    selectKey();
    const form = screen
      .getByRole("button", { name: "Generate API key" })
      .closest("form")!;
    fireEvent.submit(form);
    fireEvent.submit(form);
    expect(create).toHaveBeenCalledTimes(1);
    expect(create).toHaveBeenCalledWith("support", "support-server-01");
    expect(
      screen
        .getByRole("button", { name: "Generate API key" })
        .hasAttribute("disabled"),
    ).toBe(true);
    await act(async () => resolve(issued));
    expect(screen.getByText(issued.credential.token)).toBeTruthy();
    expect(screen.getByText("https://app.example.test/api/aap")).toBeTruthy();
    expect(screen.getByText("support-server-01")).toBeTruthy();
    expect(screen.getByText("4f1c2b7e-8d3a-4c55-9b1e-2a7d6f0c9e31")).toBeTruthy();
    expect(screen.getByText("Key expires")).toBeTruthy();
    expect(screen.getByText(/Authorization: Bearer/)).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "Done" }).getAttribute("href"),
    ).toBe("/agents/support?tab=instances");
    expect(
      screen.queryByRole("button", { name: "Generate API key" }),
    ).toBeNull();
    expect(screen.queryByRole("radio")).toBeNull();
    expect(mocks.invalidate).toHaveBeenCalled();
    view.rerender(
      <NewAgentInstanceScreen
        agentSlug="support"
        page={{ agent: null, error: "Provider unavailable" }}
      />,
    );
    expect(screen.getByText(issued.credential.token)).toBeTruthy();
    view.unmount();
    show();
    expect(screen.queryByText(issued.credential.token)).toBeNull();
    expect(create).toHaveBeenCalledTimes(1);
  });

  it("offers pausing runs only when the server can deliver webhooks", () => {
    show();
    expect(
      screen.queryByRole("radio", { name: /Pause runs while waiting/ }),
    ).toBeNull();
    cleanup();
    mocks.config = { ...mocks.config!, webhook_enabled: true };
    show();
    expect(
      screen.getByRole("radio", { name: /Pause runs while waiting/ }),
    ).toBeTruthy();
  });

  describe("pausing runs while waiting", () => {
    const registration = {
      registration_id: "5f0c2a8e-8a59-4c1e-9d47-6b2f2d9b7c11",
      signing_secret: "whsec_c2VjcmV0LXNlY3JldC1zZWNyZXQtc2VjcmV0LXNlYw==",
      expires_at: "2027-01-01T01:00:00Z",
    };
    const verified = {
      ...issued,
      agent_instance: {
        ...issued.agent_instance,
        delivery: {
          type: "webhook",
          url: "https://agent.example.com/aap/events",
          registration_id: registration.registration_id,
        },
      },
    } as Awaited<ReturnType<typeof api.createAgentInstance>>;

    beforeEach(() => {
      mocks.config = { ...mocks.config!, webhook_enabled: true };
    });

    async function startRegistration() {
      show();
      fireEvent.click(
        screen.getByRole("radio", { name: /Pause runs while waiting/ }),
      );
      fireEvent.click(
        screen.getByRole("button", { name: "Generate signing secret" }),
      );
      await screen.findByText(registration.signing_secret);
    }
    function enterReceiver() {
      fireEvent.change(screen.getByRole("textbox", { name: "Instance name" }), {
        target: { value: " support-server-01 " },
      });
      fireEvent.change(screen.getByRole("textbox", { name: "Receiver URL" }), {
        target: { value: " https://agent.example.com/aap/events " },
      });
    }

    it("shows the registration once, verifies the receiver and shows the key", async () => {
      const register = vi
        .spyOn(api, "createAgentInstanceRegistration")
        .mockResolvedValue(registration);
      const create = vi
        .spyOn(api, "createAgentInstance")
        .mockResolvedValue(verified);
      await startRegistration();
      expect(register).toHaveBeenCalledTimes(1);
      expect(register).toHaveBeenCalledWith("support");
      expect(screen.getByText(registration.registration_id)).toBeTruthy();
      expect(screen.getByText("Copy your signing secret now")).toBeTruthy();
      expect(create).not.toHaveBeenCalled();
      const verify = screen.getByRole("button", { name: "Verify receiver" });
      expect(verify.hasAttribute("disabled")).toBe(true);
      enterReceiver();
      fireEvent.submit(verify.closest("form")!);
      await screen.findByText("Receiver verified");
      expect(create).toHaveBeenCalledTimes(1);
      expect(create).toHaveBeenCalledWith("support", "support-server-01", {
        registration_id: registration.registration_id,
        url: "https://agent.example.com/aap/events",
      });
      expect(screen.getByText(issued.credential.token)).toBeTruthy();
      expect(
        screen.getByText("https://agent.example.com/aap/events"),
      ).toBeTruthy();
      expect(screen.queryByText(registration.signing_secret)).toBeNull();
      expect(mocks.invalidate).toHaveBeenCalled();
    });

    it("keeps the registration after a failed verification so the person can retry", async () => {
      vi.spyOn(api, "createAgentInstanceRegistration").mockResolvedValue(
        registration,
      );
      const create = vi
        .spyOn(api, "createAgentInstance")
        .mockRejectedValueOnce(
          new RequestError(
            "The receiver answered 404 instead of 200.",
            422,
            "webhook_verification_failed",
          ),
        )
        .mockResolvedValueOnce(verified);
      await startRegistration();
      enterReceiver();
      fireEvent.click(screen.getByRole("button", { name: "Verify receiver" }));
      await waitFor(() =>
        expect(screen.getByRole("alert").textContent).toContain(
          "The receiver answered 404 instead of 200.",
        ),
      );
      expect(screen.getByText("Receiver was not verified")).toBeTruthy();
      expect(screen.getByText(registration.signing_secret)).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: "Verify receiver" }));
      await screen.findByText("Receiver verified");
      expect(create).toHaveBeenCalledTimes(2);
    });

    it("starts again when the registration expired", async () => {
      const register = vi
        .spyOn(api, "createAgentInstanceRegistration")
        .mockResolvedValue(registration);
      vi.spyOn(api, "createAgentInstance").mockRejectedValue(
        new RequestError(
          "This registration has expired. Start again.",
          410,
          "instance_registration_expired",
        ),
      );
      await startRegistration();
      enterReceiver();
      fireEvent.click(screen.getByRole("button", { name: "Verify receiver" }));
      await screen.findByText("Registration expired");
      expect(screen.queryByText(registration.signing_secret)).toBeNull();
      fireEvent.click(
        screen.getByRole("button", { name: "Generate signing secret" }),
      );
      await screen.findByText(registration.signing_secret);
      expect(register).toHaveBeenCalledTimes(2);
    });
  });

  it("keeps the form editable after a failed registration", async () => {
    vi.spyOn(api, "createAgentInstance").mockRejectedValue(
      new Error("An instance with this name already exists."),
    );
    show();
    selectKey();
    fireEvent.click(screen.getByRole("button", { name: "Generate API key" }));
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "An instance with this name already exists.",
      ),
    );
    expect(
      screen
        .getByRole("textbox", { name: "Instance name" })
        .hasAttribute("disabled"),
    ).toBe(false);
    expect(screen.queryByText("API key generated")).toBeNull();
  });

  it("rejects blank names", () => {
    const create = vi.spyOn(api, "createAgentInstance");
    show();
    selectKey();
    fireEvent.change(screen.getByRole("textbox", { name: "Instance name" }), {
      target: { value: "   " },
    });
    const button = screen.getByRole("button", { name: "Generate API key" });
    expect(button.hasAttribute("disabled")).toBe(true);
    fireEvent.submit(button.closest("form")!);
    expect(create).not.toHaveBeenCalled();
  });

  it.each(["disabled", "archived"] as const)(
    "does not offer setup for a %s agent",
    (status) => {
      show({ ...agent, status });
      expect(
        screen.getByText(
          status === "disabled" ? "Agent disabled" : "Agent archived",
        ),
      ).toBeTruthy();
      expect(screen.queryByRole("radio")).toBeNull();
    },
  );

  it("checks issuance permission against this agent, not another agent", () => {
    const principal = {
      grants: [
        { permission: "agent.read", scope_kind: "organization" },
        {
          permission: "agent.credential.issue",
          scope_kind: "agent",
          scope_id: "other",
        },
      ],
    } as Principal;
    mocks.can.mockImplementation((permission: string, resource: Resource) =>
      can(principal, permission, resource),
    );
    show();
    expect(screen.getByText("Permission required")).toBeTruthy();
    expect(screen.queryByRole("radio")).toBeNull();
  });

  it("offers a reload when configuration is unavailable", () => {
    mocks.config = null;
    show();
    expect(screen.getByRole("button", { name: "Reload page" })).toBeTruthy();
    expect(screen.queryByRole("radio")).toBeNull();
  });

  it("loads the agent and returns errors as page data", async () => {
    const load = vi.spyOn(api, "agent").mockResolvedValue(agent);
    expect(await loadNewAgentInstancePage("support")).toEqual({
      agent,
      error: "",
    });
    expect(load).toHaveBeenCalledWith("support");
    load.mockRejectedValue(new Error("Session expired"));
    const page = await loadNewAgentInstancePage("support");
    render(<NewAgentInstanceScreen agentSlug="support" page={page} />);
    expect(screen.getByText("Session expired")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(mocks.invalidate).toHaveBeenCalled();
  });
});
