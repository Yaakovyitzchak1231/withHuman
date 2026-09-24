// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { InfoCard } from "./info-card";
import { Menu, MenuContent, MenuTrigger } from "./menu";
import { Table } from "./table";
import { TextField } from "./text-field";

const docsLinks = [{ href: "https://withhuman.ai/docs/web-app/escalation-paths", label: "Escalation paths guide" }];

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    unobserve() {}
    disconnect() {}
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function hover(trigger: HTMLElement) {
  fireEvent.mouseEnter(trigger);
  fireEvent.mouseMove(trigger, { clientX: 10, clientY: 10 });
  return screen.findByRole("dialog");
}

async function leave(trigger: HTMLElement) {
  fireEvent.mouseLeave(trigger, { relatedTarget: document.body, clientX: 1000, clientY: 1000 });
  fireEvent.mouseMove(document.body, { clientX: 1000, clientY: 1000 });
  await act(() => new Promise((resolve) => setTimeout(resolve, 150)));
}

describe("info card dismissal", () => {
  it("closes on pointer departure without moving focus to the icon", async () => {
    render(<InfoCard title="Path key">A permanent identifier.</InfoCard>);
    const trigger = screen.getByRole("button", { name: "About Path key" });
    await hover(trigger);
    await leave(trigger);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement).not.toBe(trigger);
  });

  it.each(["Escape", "second click"])("does not pin later hovers after dismissal with %s", async (dismissal) => {
    render(<InfoCard title="Path key">A permanent identifier.</InfoCard>);
    const trigger = screen.getByRole("button", { name: "About Path key" });
    act(() => trigger.focus());
    fireEvent.click(trigger);
    await screen.findByRole("dialog");
    await leave(trigger);
    expect(screen.getByRole("dialog")).toBeTruthy();
    if (dismissal === "Escape") fireEvent.keyDown(trigger, { key: "Escape" });
    else fireEvent.click(trigger);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement).toBe(trigger);

    await hover(trigger);
    await leave(trigger);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("keeps keyboard-focused help open across pointer departure", async () => {
    render(<InfoCard title="Path key" docsLinks={docsLinks}>A permanent identifier.</InfoCard>);
    const trigger = screen.getByRole("button", { name: "About Path key" });
    fireEvent.keyDown(document.body, { key: "Tab" });
    act(() => trigger.focus());
    const link = await screen.findByRole("link", { name: /Escalation paths guide/ });
    await leave(trigger);
    expect(screen.getByRole("dialog")).toBeTruthy();
    act(() => link.focus());
    fireEvent.keyDown(link, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement).toBe(trigger);
  });
});

describe("info cards in product controls", () => {
  it("opens field help without submitting the form or breaking the field label", async () => {
    const submit = vi.fn((event) => event.preventDefault());
    render(
      <form onSubmit={submit}>
        <TextField label="Path key" info="A permanent identifier for routing." docsLinks={docsLinks} />
      </form>,
    );
    expect(screen.getByRole("textbox", { name: "Path key" })).toBeTruthy();
    const trigger = screen.getByRole("button", { name: "About Path key" });
    fireEvent.click(trigger);
    const link = await screen.findByRole("link", { name: /Escalation paths guide/ });
    expect(link.getAttribute("href")).toBe(docsLinks[0].href);
    expect(link.getAttribute("target")).toBe("_blank");
    expect(submit).not.toHaveBeenCalled();
    act(() => link.focus());
    fireEvent.keyDown(link, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement).toBe(trigger);
    fireEvent.click(trigger);
    expect(await screen.findByRole("dialog", { name: "Path key" })).toBeTruthy();
    fireEvent.click(trigger);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("keeps column help independent of sorting", async () => {
    const onSort = vi.fn();
    render(
      <Table
        aria-label="Pipelines"
        items={[]}
        getId={(item: string) => item}
        selected={new Set()}
        onSelectedChange={() => {}}
        columns={[{ id: "revision", label: "Revision", sortable: true, info: "The saved version.", docsLinks, cell: (item) => item }]}
        onSort={onSort}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "About Revision" }));
    const link = await screen.findByRole("link", { name: /Escalation paths guide/ });
    // Suppress navigation while exercising the actual link's event path.
    link.addEventListener("click", (event) => event.preventDefault());
    fireEvent.click(link);
    expect(onSort).not.toHaveBeenCalled();
    fireEvent.keyDown(link, { key: "Escape" });
    fireEvent.click(screen.getByRole("button", { name: "Revision" }));
    expect(onSort).toHaveBeenCalledExactlyOnceWith({ column: "revision", direction: "asc" });
  });

  it("keeps menu help and links separate from actions and dismisses the nested card first", async () => {
    const action = vi.fn();
    render(
      <Menu actions={[{ id: "notify", label: "Notify", info: "Ask people to review.", docsLinks, action }]}>
        <MenuTrigger>Add a step</MenuTrigger>
        <MenuContent />
      </Menu>,
    );
    const menuTrigger = screen.getByRole("combobox");
    fireEvent.click(menuTrigger);
    const help = await screen.findByRole("button", { name: "About Notify" });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("combobox", { name: "Filter actions" })));
    act(() => help.focus());
    const link = await screen.findByRole("link", { name: /Escalation paths guide/ });
    act(() => link.focus());
    expect(document.activeElement).toBe(link);
    link.addEventListener("click", (event) => event.preventDefault());
    fireEvent.click(link);
    expect(action).not.toHaveBeenCalled();
    expect(menuTrigger.getAttribute("aria-expanded")).toBe("true");
    fireEvent.keyDown(link, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Notify" })).toBeNull());
    expect(document.activeElement).toBe(help);
    expect(menuTrigger.getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(screen.getByRole("gridcell", { name: "Notify" }));
    expect(action).toHaveBeenCalledOnce();
    await waitFor(() => expect(menuTrigger.getAttribute("aria-expanded")).toBe("false"));
  });
});
