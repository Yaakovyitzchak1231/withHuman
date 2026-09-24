// @vitest-environment jsdom
import type { ComponentType, ReactNode } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { OrganizationPolicy } from "../types";
import { registerExtensions, type OrganizationPolicyRowProps } from "./extensions";
import { OrganizationScreen } from "./organization";

vi.mock("@tanstack/react-router", () => ({
  createLink: (component: ComponentType) => component,
  useRouter: () => ({ invalidate: vi.fn() }),
  useBlocker: () => ({ proceed: vi.fn(), reset: vi.fn(), status: "idle" }),
  Link: ({ children }: { children: ReactNode }) => <a>{children}</a>,
}));
vi.mock("@/ds/components/toast", () => ({ useToast: () => ({ add: vi.fn() }) }));
beforeEach(() => { vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} }); });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

/* A row the way an edition registers one: a control over a knob core
 * serves, edited through the page's own draft. */
function OrganizationKeysRow({ draft, update, canEdit }: OrganizationPolicyRowProps) {
  return (
    <label>
      Allow organization API keys
      <input
        type="checkbox"
        checked={draft.organizationAPIKeysAllowed}
        disabled={!canEdit}
        onChange={(event) => update({ organizationAPIKeysAllowed: event.target.checked })}
      />
    </label>
  );
}

const policy: OrganizationPolicy = {
  organization_api_keys_allowed: false,
  allow_self_approval: false,
  mfa_mode: "optional",
  approval_reauth_window: "24h0m0s",
  claim_retrigger_window: "15m0s",
  unrouted_requests: "any_reviewer",
  request_visibility: "all_readers",
  personal_api_keys_allowed: true,
};

it("renders an edition's rows on the organization policies page and batches their edits behind the save bar", () => {
  registerExtensions({ organizationPolicyRows: [OrganizationKeysRow] });
  render(<OrganizationScreen page={{ policy, error: "" }} canRead canEdit />);
  const row = screen.getByLabelText("Allow organization API keys") as HTMLInputElement;
  expect(row.checked).toBe(false);
  expect(screen.queryByText("1 unsaved change")).toBeNull();
  fireEvent.click(row);
  expect(row.checked).toBe(true);
  expect(screen.getByText("1 unsaved change")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Discard" }));
  expect(row.checked).toBe(false);
  expect(screen.queryByText("1 unsaved change")).toBeNull();
});

it("disables an edition's rows with core's when the person cannot edit", () => {
  render(<OrganizationScreen page={{ policy, error: "" }} canRead canEdit={false} />);
  expect((screen.getByLabelText("Allow organization API keys") as HTMLInputElement).disabled).toBe(true);
});
