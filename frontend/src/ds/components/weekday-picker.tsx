import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";

export type Weekday = "mon" | "tue" | "wed" | "thu" | "fri" | "sat" | "sun";

/* Weeks start Monday: this picker exists for working-hours intervals. */
const WEEKDAYS: { value: Weekday; label: string; name: string }[] = [
  { value: "mon", label: "M", name: "Monday" },
  { value: "tue", label: "T", name: "Tuesday" },
  { value: "wed", label: "W", name: "Wednesday" },
  { value: "thu", label: "T", name: "Thursday" },
  { value: "fri", label: "F", name: "Friday" },
  { value: "sat", label: "S", name: "Saturday" },
  { value: "sun", label: "S", name: "Sunday" },
];

export interface WeekdayPickerProps {
  value: Weekday[];
  onChange: (next: Weekday[]) => void;
  "aria-label": string;
  disabled?: boolean;
  className?: string;
}

/* Multi-press day chips; the emitted array is always in week order. */
export function WeekdayPicker({
  value,
  onChange,
  "aria-label": ariaLabel,
  disabled = false,
  className,
}: WeekdayPickerProps) {
  function toggle(day: Weekday) {
    const next = value.includes(day)
      ? value.filter((existing) => existing !== day)
      : [...value, day];
    onChange(WEEKDAYS.map((d) => d.value).filter((d) => next.includes(d)));
  }

  return (
    <div
      role="group"
      aria-label={ariaLabel}
      className={cn(
        "inline-flex items-center gap-0.5 rounded-md border-hairline border-edge bg-canvas p-0.5",
        disabled && "pointer-events-none opacity-50",
        className,
      )}
    >
      {WEEKDAYS.map((day) => {
        const pressed = value.includes(day.value);
        return (
          <button
            key={day.value}
            type="button"
            aria-label={day.name}
            aria-pressed={pressed}
            disabled={disabled}
            onClick={() => toggle(day.value)}
            className={cn(
              "flex h-6 min-w-7 cursor-pointer items-center justify-center",
              "rounded-[5px] px-1.5 text-label text-ink-muted select-none",
              "transition-colors duration-100",
              "hover:bg-fill-hover hover:text-ink-secondary",
              pressed && "bg-control text-ink shadow-sm ring-hairline",
              focusRing,
            )}
          >
            {day.label}
          </button>
        );
      })}
    </div>
  );
}
