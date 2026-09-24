import { useEffect, useRef, useState } from "react";
import { cn } from "@/ds/lib/cn";
import { TextField } from "./text-field";

/* Accepts "9", "9:30", "09:30", and "0930"; returns canonical "HH:MM". */
function parseTime(text: string): string | null {
  const trimmed = text.trim();
  const match = trimmed.includes(":")
    ? /^(\d{1,2}):(\d{1,2})$/.exec(trimmed)
    : trimmed.length > 2
      ? /^(\d{1,2})(\d{2})$/.exec(trimmed)
      : /^(\d{1,2})$/.exec(trimmed);
  if (!match) return null;
  const hours = Number.parseInt(match[1], 10);
  const minutes = match[2] === undefined ? 0 : Number.parseInt(match[2], 10);
  if (hours > 23 || minutes > 59) return null;
  const pad = (part: number) => String(part).padStart(2, "0");
  return `${pad(hours)}:${pad(minutes)}`;
}

export interface TimeFieldProps {
  /* Canonical 24-hour "HH:MM" in, "HH:MM" out. */
  value: string;
  onChange: (next: string) => void;
  "aria-label": string;
  className?: string;
  disabled?: boolean;
}

/*
 * Time-of-day input for working-hours intervals. Unlike DurationField
 * it commits on blur or Enter, not per keystroke: a partial time is
 * ambiguous while it is being typed. Invalid input reverts.
 */
export function TimeField({
  value,
  onChange,
  "aria-label": ariaLabel,
  className,
  disabled = false,
}: TimeFieldProps) {
  const [text, setText] = useState(value);
  const inputRef = useRef<HTMLInputElement | null>(null);
  /* Segment selection that must apply after a setText re-render, which
   * would otherwise reset the caret to the end of the input. */
  const pendingSelection = useRef<[number, number] | null>(null);

  /* Resync when the value changes from outside (a discard, a load). */
  useEffect(() => {
    setText((current) => (parseTime(current) === value ? current : value));
  }, [value]);

  useEffect(() => {
    if (pendingSelection.current && inputRef.current) {
      const [start, end] = pendingSelection.current;
      pendingSelection.current = null;
      inputRef.current.setSelectionRange(start, end);
    }
  }, [text]);

  function commit() {
    const parsed = parseTime(text);
    if (parsed === null) {
      setText(value);
      return;
    }
    setText(parsed);
    if (parsed !== value) onChange(parsed);
  }

  return (
    <TextField
      ref={inputRef}
      disabled={disabled}
      aria-label={ariaLabel}
      value={text}
      placeholder="09:00"
      onChange={(event) =>
        setText(event.target.value.replace(/[^0-9:]/g, ""))
      }
      onBlur={commit}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          commit();
          return;
        }
        /* Tab moves between the segments before leaving the field:
         * hours to minutes forward, minutes to hours backward. */
        if (event.key !== "Tab") return;
        const input = event.currentTarget;
        const colon = text.indexOf(":");
        const caret = input.selectionStart ?? 0;
        const inHours = colon === -1 || caret <= colon;
        if (!event.shiftKey && inHours) {
          event.preventDefault();
          const canonical = parseTime(text) ?? value;
          if (canonical === text) {
            input.setSelectionRange(3, 5);
          } else {
            pendingSelection.current = [3, 5];
            setText(canonical);
          }
        } else if (event.shiftKey && !inHours) {
          event.preventDefault();
          input.setSelectionRange(0, colon);
        }
      }}
      className={cn("w-16 text-center tabular-nums", className)}
    />
  );
}
