import { useCallback, useMemo, useState } from "react";
import { Button } from "@/ds/components/button";
import {
  CommandPalette,
  useCommandK,
  type Command,
} from "@/ds/components/command";
import { Kbd } from "@/ds/components/kbd";
import { useToast } from "@/ds/components/toast";
import { useTheme } from "@/ds/theme/theme-provider";
import {
  ArrowUpRightIcon,
  CheckIcon,
  CopyIcon,
  MoonIcon,
  SunIcon,
  XIcon,
} from "@/ds/icons";
import { Section, Demo, DemoRow, KeyboardHint } from "../section";

export function CommandPaletteSection() {
  const [open, setOpen] = useState(false);
  const toast = useToast();
  const { resolvedTheme, setTheme } = useTheme();

  useCommandK(useCallback(() => setOpen((value) => !value), []));

  const commands = useMemo<Command[]>(
    () => [
      {
        id: "approve",
        label: "Approve request",
        group: "Request",
        keys: "A",
        icon: <CheckIcon width={15} height={15} />,
        action: () =>
          toast.add({ type: "approve", title: "Request approved" }),
      },
      {
        id: "deny",
        label: "Deny request",
        group: "Request",
        keys: "D",
        icon: <XIcon width={15} height={15} />,
        action: () => toast.add({ type: "deny", title: "Request denied" }),
      },
      {
        id: "copy-id",
        label: "Copy request id",
        group: "Request",
        keys: "mod+C",
        icon: <CopyIcon width={15} height={15} />,
        action: () => toast.add({ title: "Request id copied" }),
      },
      {
        id: "open-temporal",
        label: "Open in Temporal",
        group: "Navigate",
        icon: <ArrowUpRightIcon width={15} height={15} />,
        action: () => toast.add({ title: "Opening Temporal" }),
      },
      {
        id: "toggle-theme",
        label:
          resolvedTheme === "dark"
            ? "Switch to light theme"
            : "Switch to dark theme",
        group: "Workspace",
        icon:
          resolvedTheme === "dark" ? (
            <SunIcon width={15} height={15} />
          ) : (
            <MoonIcon width={15} height={15} />
          ),
        action: () => setTheme(resolvedTheme === "dark" ? "light" : "dark"),
      },
    ],
    [resolvedTheme, setTheme, toast],
  );

  return (
    <Section
      id="command-palette"
      title="Command palette"
      lede="Every action, one keystroke away. Filtering, grouping, and highlight tracking come from an inline autocomplete inside the dialog overlay."
    >
      <Demo label="Try it" hint="actions fire real toasts and theme changes">
        <DemoRow className="gap-3">
          <Button variant="primary" onClick={() => setOpen(true)}>
            Open command palette
          </Button>
          <span className="flex items-center gap-1.5 text-micro text-ink-faint">
            or press <Kbd keys="mod+K" />
          </span>
        </DemoRow>
        <KeyboardHint>
          Type to filter; Up and Down move the highlight; Enter runs; Esc
          closes.
        </KeyboardHint>
      </Demo>
      <CommandPalette open={open} onOpenChange={setOpen} commands={commands} />
    </Section>
  );
}
