import { Section, Demo } from "../section";

const ROLES = [
  {
    cls: "text-display font-display",
    name: "display",
    spec: "28/34 · display serif 300 · -0.021em",
    sample: "Every consequential action, reviewed.",
  },
  {
    cls: "text-title-lg",
    name: "title-lg",
    spec: "20/28 · 600 · -0.017em",
    sample: "Pending approvals",
  },
  {
    cls: "text-title",
    name: "title",
    spec: "16/24 · 600 · -0.014em",
    sample: "Refund request from support agent",
  },
  {
    cls: "text-body-lg",
    name: "body-lg",
    spec: "14/21 · 400 · -0.010em",
    sample: "The agent wants to issue a $420 refund to an enterprise customer.",
  },
  {
    cls: "text-body",
    name: "body",
    spec: "13/20 · 400 · -0.008em",
    sample: "The agent wants to issue a $420 refund to an enterprise customer.",
  },
  {
    cls: "text-label",
    name: "label",
    spec: "12/16 · 500",
    sample: "Requested by",
  },
  {
    cls: "text-micro",
    name: "micro",
    spec: "11/16 · 500 · +0.005em",
    sample: "Updated 2 minutes ago",
  },
];

export function TypographySection() {
  return (
    <Section
      id="typography"
      title="Typography"
      lede="Inter 92 for interface text, Roboto Mono strictly for machine content, a display serif for rare display moments (Martina Plantijn in the hosted edition, Georgia here). Sizes are semantic roles, not t-shirt sizes."
    >
      <Demo label="Type scale" bare>
        <div className="flex flex-col divide-y divide-edge rounded-lg border-hairline border-edge bg-surface shadow-sm">
          {ROLES.map((role) => (
            <div
              key={role.name}
              className="flex flex-col gap-1 px-5 py-4 sm:flex-row sm:items-baseline sm:gap-6"
            >
              <div className="w-36 shrink-0 font-mono text-micro font-normal text-ink-muted">
                <span className="text-ink-secondary">{role.name}</span>
                <br />
                {role.spec}
              </div>
              <div className={`${role.cls} min-w-0 text-ink`}>{role.sample}</div>
            </div>
          ))}
        </div>
      </Demo>

      <Demo
        label="Machine content"
        hint="Roboto Mono has no size-adjust: declare it 1px smaller than adjacent Inter text"
      >
        <div className="flex flex-col gap-2">
          <p className="text-body text-ink">
            Agent <span className="font-mono text-[12px] text-ink-secondary">billing-agent-01</span>{" "}
            called{" "}
            <span className="font-mono text-[12px] text-ink-secondary">stripe.refunds.create</span>{" "}
            with a payload of 3 fields.
          </p>
          <pre className="mt-2 overflow-x-auto rounded-md bg-canvas p-3 font-mono text-[12px] leading-[19px] text-ink-secondary ring-hairline">
{`{
  "charge": "ch_3Nq2XT2eZvKYlo2C",
  "amount": 42000,
  "reason": "requested_by_customer"
}`}
          </pre>
        </div>
      </Demo>
    </Section>
  );
}
