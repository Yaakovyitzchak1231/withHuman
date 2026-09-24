import { Sparkline } from "@/ds/components/sparkline";
import { Section, Demo, DemoRow } from "../section";

const SERIES: { label: string; values: number[]; total: number }[] = [
  { label: "Rising", values: [0, 1, 1, 3, 4, 6, 9], total: 24 },
  { label: "Spiky", values: [2, 0, 7, 1, 0, 5, 2], total: 17 },
  { label: "Quiet week", values: [0, 0, 0, 0, 0, 0, 0], total: 0 },
  { label: "One burst", values: [0, 0, 12, 0, 0, 0, 1], total: 13 },
];

export function SparklinesSection() {
  return (
    <Section
      id="sparklines"
      title="Sparkline"
      lede="A single series in a table cell: a thin line on a zero baseline, the newest point in the accent. It gives the shape; the figure beside it gives the scale."
    >
      <Demo label="Beside a figure" hint="the agents list uses this for requests over seven days">
        <DemoRow className="gap-8">
          {SERIES.map((series) => (
            <div key={series.label} className="flex items-center gap-2">
              <Sparkline values={series.values} label={`${series.label}, last 7 days`} />
              <span className="text-body text-ink tabular-nums">{series.total}</span>
            </div>
          ))}
        </DemoRow>
      </Demo>
    </Section>
  );
}
