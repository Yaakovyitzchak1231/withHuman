import { useRef, useState, type KeyboardEvent } from "react";
import { cn } from "@/ds/lib/cn";
import { XIcon } from "@/ds/icons";
import { InputSuggestions, useInputSuggestions } from "./input-suggestions";

export interface TagInputProps {
  value: string[];
  onChange: (next: string[]) => void;
  placeholder?: string;
  /* Machine values (policy scopes, hostnames) render mono. */
  mono?: boolean;
  /* Read only: the chips stay, nothing can be added or removed. */
  disabled?: boolean;
  "aria-label": string;
  className?: string;
  suggestions?: string[];
}

/* Creatable chips: Enter or comma adds what was typed, Backspace on an
 * empty input removes the last chip, duplicates are ignored. */
export function TagInput({
  value,
  onChange,
  placeholder,
  mono = false,
  disabled = false,
  "aria-label": ariaLabel,
  className,
  suggestions = [],
}: TagInputProps) {
  const [draft, setDraft] = useState("");
  const inputRef = useRef<HTMLInputElement | null>(null);

  const autocomplete = useInputSuggestions(draft, disabled ? [] : suggestions.filter((item) => !value.includes(item)), commit, true);

  function commit(raw = draft) {
    const tag = raw.trim();
    if (!tag || disabled) return;
    if (!value.includes(tag)) onChange([...value, tag]);
    setDraft("");
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    autocomplete.input.onKeyDown(event);
    if (event.defaultPrevented || event.nativeEvent.isComposing) return;
    if (event.key === "Enter" || event.key === ",") {
      event.preventDefault();
      commit();
      return;
    }
    if (event.key === "Backspace" && draft === "" && value.length > 0 && !disabled) {
      event.preventDefault();
      onChange(value.slice(0, -1));
    }
  }

  return (
    <div
      onClick={() => inputRef.current?.focus()}
      aria-disabled={disabled || undefined}
      className={cn(
        "relative flex min-h-[30px] w-full flex-wrap items-center gap-1 rounded-md",
        "border border-edge bg-surface px-1.5 py-1",
        "transition-[border-color,box-shadow] duration-100",
        disabled
          ? "cursor-default opacity-60"
          : "cursor-text hover:border-edge-strong focus-within:border-accent focus-within:shadow-[0_0_0_2.5px_var(--ds-accent-subtle)] focus-within:hover:border-accent",
        className,
      )}
    >
      {value.map((tag) => (
        <span
          key={tag}
          className={cn(
            "flex h-5 items-center gap-1 rounded-sm bg-fill-active pr-0.5 pl-1.5",
            mono ? "font-mono text-[11px]" : "text-label",
            "text-ink-secondary",
          )}
        >
          {tag}
          {disabled ? null : (
          <button
            type="button"
            aria-label={`Remove ${tag}`}
            onClick={(event) => {
              event.stopPropagation();
              onChange(value.filter((existing) => existing !== tag));
            }}
            className={cn(
              "flex size-4 cursor-pointer items-center justify-center rounded-xs",
              "text-ink-muted transition-colors duration-100",
              "hover:bg-fill-hover hover:text-ink",
            )}
          >
            <XIcon width={11} height={11} />
          </button>
          )}
        </span>
      ))}
      <input
        {...autocomplete.input}
        ref={inputRef}
        aria-label={ariaLabel}
        value={draft}
        placeholder={value.length === 0 ? placeholder : undefined}
        onChange={(event) => { setDraft(event.target.value); autocomplete.changed(); }}
        onKeyDown={onKeyDown}
        onBlur={() => { autocomplete.input.onBlur(); commit(); }}
        disabled={disabled}
        size={1}
        data-1p-ignore
        data-lpignore
        data-bwignore
        className={cn(
          "min-w-[60px] flex-1 bg-transparent outline-none",
          mono ? "font-mono text-[12px]" : "text-body",
          "text-ink placeholder:text-ink-faint",
        )}
      />
      <InputSuggestions {...autocomplete.list} />
    </div>
  );
}
