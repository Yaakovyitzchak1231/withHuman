import { useState } from "react";
import { DateCalendar, DateTimeField, DateTimePicker } from "@/ds/components/date-time-field";
import { TimeField } from "@/ds/components/time-field";
import { WeekdayPicker, type Weekday } from "@/ds/components/weekday-picker";
import { DurationField } from "@/ds/components/duration-field";
import { Section, Demo, DemoRow, KeyboardHint } from "../section";

export function SchedulingSection() {
  const [start, setStart] = useState("09:00");
  const [end, setEnd] = useState("18:00");
  const [days, setDays] = useState<Weekday[]>(["mon", "tue", "wed", "thu", "fri"]);
  const [escalateAfter, setEscalateAfter] = useState(900);
  const [from, setFrom] = useState<string | null>(null);
  const [to, setTo] = useState<string | null>(null);
  const [range, setRange] = useState<{ from: string | null; to: string | null }>({ from: null, to: null });

  return (
    <Section
      id="scheduling"
      title="Scheduling fields"
      lede="Inputs for working-hours intervals, escalation timers, and points in time: TimeField commits canonical 24-hour times on blur, WeekdayPicker toggles day chips in week order, DateTimeField picks a moment from a month grid."
    >
      <Demo
        label="DateTimeField"
        hint={from ? `value: ${from}` : "nothing selected"}
      >
        <DemoRow>
          <DateTimeField aria-label="From" value={from} onChange={setFrom} />
          <span className="text-label text-ink-muted">to</span>
          <DateTimeField aria-label="To" value={to} onChange={setTo} placeholder="Open ended" />
          <DateTimeField aria-label="Disabled example" value={null} onChange={() => {}} disabled />
        </DemoRow>
        <KeyboardHint>
          A Select-shaped trigger opening the month grid; weeks start Monday
          like WeekdayPicker. Picking a day keeps the time, the TimeField
          below the grid sets it, Now jumps to the current minute, Clear
          empties the field. The value is RFC 3339; the picker works in
          local time.
        </KeyboardHint>
      </Demo>

      <Demo label="DateTimePicker" hint="inline calendar and time editor">
        <div className="w-[268px] rounded-md border-hairline border-edge bg-surface-raised p-3">
          <DateTimePicker aria-label="Inline date" value={from} onChange={setFrom} />
        </div>
        <KeyboardHint>
          Embed the calendar directly in a filter popup. It shares the
          DateTimeField editor without opening another dropdown.
        </KeyboardHint>
      </Demo>

      <Demo label="DateCalendar" hint="first click starts a range, second click ends it">
        <div className="w-[268px] rounded-md border-hairline border-edge bg-surface-raised p-3">
          <DateCalendar
            value={range.from}
            rangeEnd={range.to}
            onChange={(day) => setRange((current) =>
              !current.from || current.to
                ? { from: day.toISOString(), to: null }
                : { ...current, to: day.toISOString() },
            )}
          />
        </div>
        <KeyboardHint>
          Both endpoints and the dates between them are highlighted.
          Another click starts a new range, without endpoint buttons.
        </KeyboardHint>
      </Demo>

      <Demo label="TimeField" hint={`value: ${start}`}>
        <DemoRow>
          <TimeField aria-label="Start time" value={start} onChange={setStart} />
          <TimeField aria-label="Disabled time" value="00:00" onChange={() => {}} disabled />
        </DemoRow>
        <KeyboardHint>
          Enter or blur commits; "9", "930", and "09:30" all normalize.
          Invalid input reverts to the last committed value. Tab in the
          hours moves to the minutes; Shift+Tab in the minutes moves back.
        </KeyboardHint>
      </Demo>

      <Demo label="WeekdayPicker" hint={`${days.length} days selected`}>
        <DemoRow>
          <WeekdayPicker aria-label="Active days" value={days} onChange={setDays} />
          <WeekdayPicker
            aria-label="Disabled example"
            value={["sat", "sun"]}
            onChange={() => {}}
            disabled
          />
        </DemoRow>
      </Demo>

      <Demo label="Composed" hint="a working-hours interval and a level timer">
        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center gap-2">
            <WeekdayPicker aria-label="Interval days" value={days} onChange={setDays} />
            <div className="flex items-center gap-2">
              <TimeField aria-label="Interval start" value={start} onChange={setStart} />
              <span className="text-label text-ink-muted">to</span>
              <TimeField aria-label="Interval end" value={end} onChange={setEnd} />
            </div>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-label text-ink-secondary">Escalate after</span>
            <DurationField
              aria-label="Escalate after"
              value={escalateAfter}
              onChange={setEscalateAfter}
            />
          </div>
        </div>
      </Demo>
    </Section>
  );
}
