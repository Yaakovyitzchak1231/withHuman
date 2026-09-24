import { useEffect, useState } from "react";
import { cn } from "@/ds/lib/cn";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from "./select";
import { TextField } from "./text-field";

interface Unit {
  label: string;
  seconds: number;
}

const UNITS: Unit[] = [
  { label: "minutes", seconds: 60 },
  { label: "hours", seconds: 3600 },
  { label: "days", seconds: 86400 },
];

/* Largest unit that divides the value evenly keeps the display
 * human ("8 hours", never "480 minutes"). */
function decompose(seconds: number): { amount: number; unit: Unit } {
  for (const unit of [...UNITS].reverse()) {
    if (seconds > 0 && seconds % unit.seconds === 0) {
      return { amount: seconds / unit.seconds, unit };
    }
  }
  return { amount: Math.max(1, Math.round(seconds / 60)), unit: UNITS[0] };
}

export interface DurationFieldProps {
  /* Seconds in, seconds out; the human units are presentation. */
  value: number;
  onChange: (seconds: number) => void;
  "aria-label": string;
  disabled?: boolean;
  className?: string;
}

export function DurationField({
  value,
  onChange,
  "aria-label": ariaLabel,
  disabled = false,
  className,
}: DurationFieldProps) {
  const [text, setText] = useState(() => String(decompose(value).amount));
  const [unit, setUnit] = useState<Unit>(() => decompose(value).unit);

  /* Resync when the value changes from outside (a discard, a load). */
  useEffect(() => {
    const parsed = Number.parseInt(text, 10);
    if (!Number.isFinite(parsed) || parsed * unit.seconds !== value) {
      const next = decompose(value);
      setText(String(next.amount));
      setUnit(next.unit);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);

  function emit(nextText: string, nextUnit: Unit) {
    const parsed = Number.parseInt(nextText, 10);
    if (Number.isFinite(parsed) && parsed > 0) {
      onChange(parsed * nextUnit.seconds);
    }
  }

  return (
    <div className={cn("flex items-center gap-2", className)}>
      <TextField
        aria-label={ariaLabel}
        inputMode="numeric"
        disabled={disabled}
        value={text}
        onChange={(event) => {
          const next = event.target.value.replace(/[^0-9]/g, "");
          setText(next);
          emit(next, unit);
        }}
        className="w-16 text-right"
      />
      <Select
        items={UNITS}
        itemToStringLabel={(item) => (item as Unit).label}
        value={unit}
        disabled={disabled}
        onValueChange={(next) => {
          if (!next) return;
          const nextUnit = next as Unit;
          setUnit(nextUnit);
          emit(text, nextUnit);
        }}
      >
        <SelectTrigger aria-label={`${ariaLabel} unit`} className="w-28" />
        <SelectContent<Unit>>
          {(item) => (
            <SelectItem key={item.label} value={item}>
              {item.label}
            </SelectItem>
          )}
        </SelectContent>
      </Select>
    </div>
  );
}
