import { useMemo, useState } from "react";
import { Popover } from "@base-ui/react/popover";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";
import { CalendarIcon, ChevronLeftIcon, ChevronRightIcon } from "@/ds/icons";
import { Button } from "./button";
import { IconButton } from "./icon-button";
import { TimeField } from "./time-field";

/*
 * A point in time, picked from a month grid plus a TimeField. The trigger
 * is TextField-shaped like a Select; the popup is the design system's
 * overlay surface. Weeks start Monday, matching WeekdayPicker. The value
 * is RFC 3339 (or null); the picker works in the viewer's local time.
 */
export interface DateTimeFieldProps {
  value: string | null;
  onChange: (next: string | null) => void;
  "aria-label": string;
  placeholder?: string;
  disabled?: boolean;
  className?: string;
}

const WEEKDAY_LABELS = ["M", "T", "W", "T", "F", "S", "S"];

const pad = (part: number) => String(part).padStart(2, "0");

function sameDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

function parseValue(value: string | null): Date | null {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

/* Six rows of seven, starting on the Monday on or before the first of the
 * month, so the grid never changes height between months. */
function monthGrid(view: Date): Date[] {
  const first = new Date(view.getFullYear(), view.getMonth(), 1);
  const offset = (first.getDay() + 6) % 7;
  const cells: Date[] = [];
  for (let index = 0; index < 42; index++) {
    cells.push(new Date(view.getFullYear(), view.getMonth(), 1 - offset + index));
  }
  return cells;
}

export function formatDateTime(value: string | null): string {
  const date = parseValue(value);
  if (!date) return "";
  return new Intl.DateTimeFormat(undefined, {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
}

export function DateTimeField({
  value,
  onChange,
  "aria-label": ariaLabel,
  placeholder = "Pick a date and time",
  disabled = false,
  className,
}: DateTimeFieldProps) {
  const [open, setOpen] = useState(false);
  const selected = useMemo(() => parseValue(value), [value]);
  return (
    <Popover.Root
      open={open}
      onOpenChange={setOpen}
    >
      <Popover.Trigger
        aria-label={ariaLabel}
        disabled={disabled}
        className={cn(
          "flex h-[30px] min-w-40 cursor-pointer items-center justify-between gap-2",
          "rounded-sm border-hairline border-edge bg-control pr-2 pl-2.5",
          "text-body text-ink select-none shadow-sm",
          "transition-[border-color,box-shadow] duration-100 ease-standard",
          "hover:border-edge-strong",
          "disabled:pointer-events-none disabled:opacity-50",
          focusRing,
          className,
        )}
      >
        <span className={cn("truncate tabular-nums", !selected && "text-ink-faint")}>
          {selected ? formatDateTime(value) : placeholder}
        </span>
        <CalendarIcon width={14} height={14} className="shrink-0 text-ink-muted" />
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Positioner align="start" sideOffset={4}>
          <Popover.Popup
            className={cn(
              /* ds-root re-establishes the baseline inside the portal. */
              "ds-root w-[268px] rounded-md bg-surface-raised p-3 outline-none",
              "shadow-overlay ring-hairline",
              "[transform-origin:var(--transform-origin)]",
              "transition-[opacity,scale] duration-120 ease-out",
              "data-starting-style:scale-[0.98] data-starting-style:opacity-0",
              "data-ending-style:scale-[0.98] data-ending-style:opacity-0",
            )}
          >
            <DateTimePicker value={value} onChange={onChange} aria-label={ariaLabel} />
          </Popover.Popup>
        </Popover.Positioner>
      </Popover.Portal>
    </Popover.Root>
  );
}

/* The calendar and time editor can also live directly inside a larger
 * filter popup, without adding another trigger or nested overlay. */
export function DateTimePicker({
  value,
  onChange,
  "aria-label": ariaLabel,
}: Pick<DateTimeFieldProps, "value" | "onChange" | "aria-label">) {
  const selected = useMemo(() => parseValue(value), [value]);
  const today = new Date();

  function commit(date: Date) {
    onChange(date.toISOString());
  }

  function pickDay(day: Date) {
    const next = new Date(day);
    if (selected) {
      next.setHours(selected.getHours(), selected.getMinutes(), 0, 0);
    } else {
      next.setHours(0, 0, 0, 0);
    }
    commit(next);
  }

  function pickTime(time: string) {
    const [hours, minutes] = time.split(":").map((part) => Number.parseInt(part, 10));
    const next = new Date(selected ?? today);
    next.setHours(hours, minutes, 0, 0);
    commit(next);
  }

  const timeValue = selected
    ? `${pad(selected.getHours())}:${pad(selected.getMinutes())}`
    : "00:00";

  return (
    <div role="group" aria-label={ariaLabel}>
      <DateCalendar value={value} onChange={pickDay} />

      <div className="mt-3 flex items-center justify-between gap-2 border-t-hairline border-edge pt-3">
        <TimeField aria-label={`${ariaLabel} time`} value={timeValue} onChange={pickTime} />
        <div className="flex items-center gap-1">
          <Button size="sm" variant="ghost" onClick={() => onChange(null)}>
            Clear
          </Button>
          <Button size="sm" variant="ghost" onClick={() => commit(new Date())}>
            Now
          </Button>
        </div>
      </div>
    </div>
  );
}

/* A calendar without a time field or overlay. An optional end marks a
 * selected range; the parent decides which endpoint a click sets. */
export function DateCalendar({
  value,
  rangeEnd = null,
  onChange,
}: {
  value: string | null;
  rangeEnd?: string | null;
  onChange: (day: Date) => void;
}) {
  const selected = useMemo(() => parseValue(value), [value]);
  const end = useMemo(() => parseValue(rangeEnd), [rangeEnd]);
  const [view, setView] = useState<Date>(() => selected ?? new Date());
  const today = new Date();
  const cells = useMemo(() => monthGrid(view), [view]);
  const monthLabel = new Intl.DateTimeFormat(undefined, {
    month: "long",
    year: "numeric",
  }).format(view);

  function shiftMonth(delta: number) {
    setView((current) => new Date(current.getFullYear(), current.getMonth() + delta, 1));
  }

  return (
    <div>
      <div className="flex items-center justify-between">
        <IconButton
          aria-label="Previous month"
          size="sm"
          onClick={() => shiftMonth(-1)}
        >
          <ChevronLeftIcon width={14} height={14} />
        </IconButton>
        <span className="text-body font-medium text-ink" aria-live="polite">
          {monthLabel}
        </span>
        <IconButton aria-label="Next month" size="sm" onClick={() => shiftMonth(1)}>
          <ChevronRightIcon width={14} height={14} />
        </IconButton>
      </div>

      <div className="mt-2 grid grid-cols-7" role="row">
        {WEEKDAY_LABELS.map((label, index) => (
          <span
            key={index}
            className="flex h-6 items-center justify-center text-micro text-ink-faint"
          >
            {label}
          </span>
        ))}
      </div>
      <div className="grid grid-cols-7 gap-y-0.5" role="grid" aria-label="Calendar">
        {cells.map((day) => {
          const inMonth = day.getMonth() === view.getMonth();
          const isSelected = Boolean((selected && sameDay(day, selected)) || (end && sameDay(day, end)));
          const inRange = Boolean(selected && end && day > selected && day < end);
          const isToday = sameDay(day, today);
          return (
            <button
              key={day.toISOString()}
              type="button"
              role="gridcell"
              aria-selected={isSelected}
              aria-label={day.toDateString()}
              onClick={() => onChange(day)}
              className={cn(
                "mx-auto flex size-[30px] cursor-pointer items-center justify-center rounded-sm",
                "text-body tabular-nums transition-colors duration-100",
                inMonth ? "text-ink" : "text-ink-faint",
                isSelected
                  ? "bg-accent text-ink-on-accent"
                  : inRange
                    ? "bg-fill-selected text-accent"
                    : "hover:bg-fill-hover active:bg-fill-active",
                isToday && !isSelected && "font-semibold text-accent",
                focusRing,
              )}
            >
              {day.getDate()}
            </button>
          );
        })}
      </div>
    </div>
  );
}
