import { useEffect, useMemo, type ReactNode } from "react";
import { Autocomplete } from "@base-ui/react/autocomplete";
import { Dialog as BaseDialog } from "@base-ui/react/dialog";
import { cn } from "@/ds/lib/cn";
import { SearchIcon } from "@/ds/icons";
import { Kbd } from "./kbd";

export interface Command {
  id: string;
  label: string;
  /* Section heading the command sorts under. */
  group: string;
  /* Shortcut hint, e.g. "A" or "mod+shift+P". Display only. */
  keys?: string;
  icon?: ReactNode;
  action: () => void;
}

interface PaletteItem {
  value: string;
  label: string;
  command: Command;
}

interface PaletteGroup {
  value: string;
  items: PaletteItem[];
}

/* Binds mod+K to toggle the palette while mounted. */
export function useCommandK(onToggle: () => void) {
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key.toLowerCase() === "k" && (event.metaKey || event.ctrlKey)) {
        event.preventDefault();
        onToggle();
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onToggle]);
}

export interface CommandPaletteProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  commands: Command[];
  placeholder?: string;
}

export function CommandPalette({
  open,
  onOpenChange,
  commands,
  placeholder = "Type a command",
}: CommandPaletteProps) {
  const groups = useMemo<PaletteGroup[]>(() => {
    const byGroup = new Map<string, PaletteItem[]>();
    for (const command of commands) {
      const items = byGroup.get(command.group) ?? [];
      items.push({ value: command.id, label: command.label, command });
      byGroup.set(command.group, items);
    }
    return [...byGroup.entries()].map(([value, items]) => ({ value, items }));
  }, [commands]);

  function run(command: Command) {
    onOpenChange(false);
    command.action();
  }

  return (
    <BaseDialog.Root open={open} onOpenChange={onOpenChange}>
      <BaseDialog.Portal>
        <BaseDialog.Backdrop
          className={cn(
            "fixed inset-0 bg-scrim",
            "transition-opacity duration-160 ease-standard",
            "data-starting-style:opacity-0 data-ending-style:opacity-0",
          )}
        />
        <BaseDialog.Viewport className="fixed inset-0 flex items-start justify-center px-4 pt-[14vh]">
          <BaseDialog.Popup
            aria-label="Command palette"
            onKeyDownCapture={(event) => {
              /* One Escape closes, even while the list holds a highlight
               * that the autocomplete would clear first. */
              if (event.key === "Escape") {
                event.preventDefault();
                event.stopPropagation();
                onOpenChange(false);
              }
            }}
            className={cn(
              /* ds-root re-establishes the baseline inside the portal. */
              "ds-root flex w-[min(560px,100%)] flex-col overflow-hidden",
              "rounded-xl bg-surface-overlay shadow-overlay ring-hairline",
              "transition-[opacity,scale,translate] duration-160 ease-out",
              "data-starting-style:-translate-y-2 data-starting-style:scale-[0.98] data-starting-style:opacity-0",
              "data-ending-style:opacity-0",
            )}
          >
            <Autocomplete.Root
              open
              inline
              items={groups}
              autoHighlight="always"
              keepHighlight
            >
              <div className="flex items-center gap-2.5 border-b-hairline border-edge px-3.5">
                <SearchIcon
                  width={15}
                  height={15}
                  className="shrink-0 text-ink-faint"
                />
                <Autocomplete.Input
                  aria-label="Search commands"
                  placeholder={placeholder}
                  /* Password managers attach to bare inputs and their
                   * overlay eats the first Escape; opt out. */
                  data-1p-ignore=""
                  data-lpignore="true"
                  data-bwignore=""
                  className={cn(
                    "h-11 w-full bg-transparent text-body-lg text-ink outline-none",
                    "placeholder:text-ink-faint",
                  )}
                />
                <Kbd keys="esc" />
              </div>
              <BaseDialog.Close className="sr-only">
                Close command palette
              </BaseDialog.Close>

              <div className="max-h-[min(48vh,320px)] overflow-y-auto overscroll-contain p-1">
                <Autocomplete.Empty>
                  <p className="px-2.5 py-6 text-body text-ink-muted">
                    No matching commands.
                  </p>
                </Autocomplete.Empty>
                <Autocomplete.List>
                  {(group: PaletteGroup) => (
                    <Autocomplete.Group
                      key={group.value}
                      items={group.items}
                      className="not-last:mb-1"
                    >
                      <Autocomplete.GroupLabel className="px-2.5 pt-2 pb-1 text-micro text-ink-faint select-none">
                        {group.value}
                      </Autocomplete.GroupLabel>
                      <Autocomplete.Collection>
                        {(item: PaletteItem) => (
                          <Autocomplete.Item
                            key={item.value}
                            value={item}
                            onClick={() => run(item.command)}
                            className={cn(
                              "flex h-8 cursor-pointer items-center gap-2.5 rounded-sm px-2.5",
                              "text-body text-ink outline-none select-none",
                              "data-highlighted:bg-fill-hover",
                            )}
                          >
                            {item.command.icon ? (
                              <span className="flex text-ink-muted">
                                {item.command.icon}
                              </span>
                            ) : null}
                            <span className="min-w-0 flex-1 truncate">
                              {item.label}
                            </span>
                            {item.command.keys ? (
                              <Kbd keys={item.command.keys} />
                            ) : null}
                          </Autocomplete.Item>
                        )}
                      </Autocomplete.Collection>
                    </Autocomplete.Group>
                  )}
                </Autocomplete.List>
              </div>

              <div className="flex items-center gap-4 border-t-hairline border-edge px-3.5 py-2">
                <span className="flex items-center gap-1.5 text-micro text-ink-faint">
                  <Kbd keys="up" /> <Kbd keys="down" /> navigate
                </span>
                <span className="flex items-center gap-1.5 text-micro text-ink-faint">
                  <Kbd keys="enter" /> run
                </span>
              </div>
            </Autocomplete.Root>
          </BaseDialog.Popup>
        </BaseDialog.Viewport>
      </BaseDialog.Portal>
    </BaseDialog.Root>
  );
}
