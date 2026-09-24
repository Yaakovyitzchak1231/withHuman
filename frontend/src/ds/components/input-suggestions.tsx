import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { cn } from "@/ds/lib/cn";

/* Shared by free-text fields and creatable chips. Highlighting is explicit:
 * Enter keeps typed text unless the user has navigated to a suggestion. */
export function useInputSuggestions(value: string, suggestions: readonly string[], onSelect: (value: string) => void, keepOpen = false) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const [filtering, setFiltering] = useState(false);
  const [highlight, setHighlight] = useState(-1);
  const query = filtering ? value.trim().toLowerCase() : "";
  const matches = [...new Set(suggestions)].filter((item) => item.toLowerCase().includes(query));
  const expanded = open && matches.length > 0;
  const active = highlight < matches.length ? highlight : -1;

  function select(value: string) {
    onSelect(value);
    setHighlight(-1);
    setFiltering(false);
    setOpen(keepOpen);
  }

  return {
    list: { id, matches, highlight: active, select, setHighlight, expanded },
    input: {
      role: suggestions.length ? "combobox" : undefined,
      "aria-autocomplete": suggestions.length ? "list" as const : undefined,
      "aria-expanded": suggestions.length ? expanded : undefined,
      "aria-controls": expanded ? id : undefined,
      "aria-activedescendant": expanded && active >= 0 ? `${id}-${active}` : undefined,
      autoComplete: "off",
      onFocus: () => { setOpen(true); setFiltering(false); setHighlight(-1); },
      onClick: () => { if (!open) setFiltering(false); setOpen(true); },
      onBlur: () => { setOpen(false); setHighlight(-1); },
      onKeyDown: (event: KeyboardEvent<HTMLInputElement>) => {
        if (event.nativeEvent.isComposing) return;
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
          if (!matches.length) return;
          event.preventDefault();
          setOpen(true);
          setHighlight(event.key === "ArrowDown" ? Math.min(active + 1, matches.length - 1) : active < 0 ? matches.length - 1 : Math.max(active - 1, 0));
        } else if (event.key === "Enter" && expanded && active >= 0) {
          event.preventDefault();
          select(matches[active]);
        } else if (event.key === "Escape" && open) {
          event.preventDefault();
          event.stopPropagation();
          setOpen(false);
        }
      },
    },
    changed: () => { setOpen(true); setFiltering(true); setHighlight(-1); },
  };
}

export function InputSuggestions({ id, matches, highlight, select, setHighlight, expanded, details = {} }: ReturnType<typeof useInputSuggestions>["list"] & { details?: Record<string, string> }) {
  const listRef = useRef<HTMLUListElement>(null);
  useEffect(() => {
    if (expanded && highlight >= 0) listRef.current?.children[highlight]?.scrollIntoView({ block: "nearest" });
  }, [expanded, highlight]);
  if (!expanded) return null;
  return (
    <ul ref={listRef} id={id} role="listbox" aria-label="Suggested values"
      className="absolute top-full right-0 left-0 z-30 mt-1 max-h-60 overflow-y-auto rounded-md bg-surface-raised p-1 shadow-overlay ring-hairline">
      {matches.map((value, index) => (
        <li key={value} id={`${id}-${index}`} role="option" aria-selected={index === highlight}
          onMouseDown={(event) => event.preventDefault()}
          onClick={(event) => { event.stopPropagation(); select(value); }}
          onMouseEnter={() => setHighlight(index)}
          className={cn("cursor-pointer rounded-sm px-2 py-1.5 font-mono text-[12px] break-all text-ink", index === highlight && "bg-fill-hover")}>
          {value}
          {details[value] ? <span className="block font-sans text-micro text-ink-muted">{details[value]}</span> : null}
        </li>
      ))}
    </ul>
  );
}
