// @vitest-environment jsdom
import { useEffect } from "react";
import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useDiscordOutcome } from "./discord-outcome";

const { toast } = vi.hoisted(() => ({ toast: vi.fn() }));
vi.mock("@/ds/components/toast", () => ({ useToast: () => ({ add: toast }) }));

function ReturningToOnboarding() {
  useDiscordOutcome();
  useEffect(() => { window.history.replaceState(null, "", "/welcome"); }, []);
  return null;
}

afterEach(() => { cleanup(); vi.clearAllMocks(); });

describe("Discord OAuth return during onboarding", () => {
  it.each([
    ["/settings/integrations?discord=connected", "Discord connected"],
    ["/settings/notifications?discord=linked", "Discord linked"],
    ["/settings/notifications?discord_error=access_denied", "Discord was not linked"],
    ["/settings/integrations?discord_error=guild_taken", "Discord was not connected"],
  ])("preserves feedback from %s before returning to setup", (path, title) => {
    window.history.replaceState(null, "", path);
    const view = render(<ReturningToOnboarding />);
    expect(window.location.pathname).toBe("/welcome");
    expect(toast).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ title }));
    view.unmount();
    render(<ReturningToOnboarding />);
    expect(toast).toHaveBeenCalledOnce();
  });
});
