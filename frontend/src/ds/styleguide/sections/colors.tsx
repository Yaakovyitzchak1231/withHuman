import { Section } from "../section";

/*
 * Display manifest for the token grid. Values are duplicated from
 * tokens.css knowingly (prototype); the swatch itself always renders the
 * live var, so a drift shows up visually.
 */
interface TokenSpec {
  token: string;
  light: string;
  dark: string;
  note?: string;
}

const GROUPS: { title: string; tokens: TokenSpec[] }[] = [
  {
    title: "Surfaces",
    tokens: [
      { token: "canvas", light: "#f9f9f8", dark: "#0e1015", note: "page background" },
      { token: "surface", light: "#ffffff", dark: "#16181f", note: "cards, rows" },
      { token: "surface-raised", light: "#ffffff", dark: "#1b1e26", note: "menus, popovers" },
      { token: "surface-overlay", light: "#ffffff", dark: "#1f232c", note: "dialogs" },
    ],
  },
  {
    title: "Ink",
    tokens: [
      { token: "ink", light: "#0a0d17", dark: "#e9eaef", note: "primary text" },
      { token: "ink-secondary", light: "#40434e", dark: "#b4b8c3" },
      { token: "ink-muted", light: "#6e7180", dark: "#858a99" },
      { token: "ink-faint", light: "#9b9ea9", dark: "#5b6070", note: "placeholders" },
    ],
  },
  {
    title: "Edges and fills",
    tokens: [
      { token: "edge", light: "12% ink", dark: "9% white", note: "hairlines" },
      { token: "edge-strong", light: "24% ink", dark: "18% white" },
      { token: "fill-hover", light: "4% ink", dark: "5% white" },
      { token: "fill-active", light: "7% ink", dark: "8% white" },
      { token: "fill-selected", light: "8% accent", dark: "14% accent" },
    ],
  },
  {
    title: "Accent",
    tokens: [
      { token: "accent", light: "#3d3af0", dark: "#5e86ff" },
      { token: "accent-hover", light: "#302dd8", dark: "#7597ff" },
      { token: "accent-subtle", light: "9% accent", dark: "15% accent" },
      { token: "ink-on-accent", light: "#ffffff", dark: "#0b1020" },
    ],
  },
  {
    title: "Decision semantics",
    tokens: [
      { token: "approve", light: "#148a44", dark: "#34c274" },
      { token: "approve-subtle", light: "10% approve", dark: "14% approve" },
      { token: "deny", light: "#d5303e", dark: "#f2555f" },
      { token: "deny-subtle", light: "9% deny", dark: "13% deny" },
      { token: "warn", light: "#b25e09", dark: "#dfa03c" },
      { token: "warn-subtle", light: "10% warn", dark: "13% warn" },
    ],
  },
];

function Swatch({ spec }: { spec: TokenSpec }) {
  return (
    <div className="flex items-center gap-3 py-2">
      <div
        className="h-9 w-9 rounded-sm ring-hairline"
        style={{ backgroundColor: `var(--ds-${spec.token})` }}
      />
      <div className="min-w-0">
        <div className="flex items-baseline gap-2">
          <span className="font-mono text-label font-medium text-ink">
            {spec.token}
          </span>
          {spec.note ? (
            <span className="text-micro font-normal text-ink-faint">
              {spec.note}
            </span>
          ) : null}
        </div>
        <div className="font-mono text-micro font-normal text-ink-muted">
          {spec.light} / {spec.dark}
        </div>
      </div>
    </div>
  );
}

export function ColorsSection() {
  return (
    <Section
      id="colors"
      title="Color"
      lede="Semantic tokens only: components never touch raw palette values. Every token pairs a light and dark value and flips with the theme."
    >
      <div className="grid grid-cols-1 gap-x-10 gap-y-6 sm:grid-cols-2">
        {GROUPS.map((group) => (
          <div key={group.title}>
            <h3 className="mb-1 text-label text-ink-secondary">{group.title}</h3>
            <div className="flex flex-col">
              {group.tokens.map((spec) => (
                <Swatch key={spec.token} spec={spec} />
              ))}
            </div>
          </div>
        ))}
      </div>
    </Section>
  );
}
