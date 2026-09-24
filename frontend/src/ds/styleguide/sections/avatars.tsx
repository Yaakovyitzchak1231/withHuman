import { Avatar } from "@/ds/components/avatar";
import { Section, Demo, DemoRow } from "../section";

/* Deterministic stand-in portraits so the demo needs no network. */
function portrait(skin: string, bg: string): string {
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'><rect width='32' height='32' fill='${bg}'/><circle cx='16' cy='12.5' r='6' fill='${skin}'/><path d='M4 32a12 9 0 0 1 24 0Z' fill='${skin}'/></svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

const PORTRAITS = {
  ana: portrait("#8a5a3b", "#c9d6f2"),
  marcus: portrait("#e8b48f", "#d9ccee"),
};

export function AvatarsSection() {
  return (
    <Section
      id="avatars"
      title="Avatar"
      lede="A profile picture when there is one; otherwise humans are round with a tint derived from their name, and agents are square and mono."
    >
      <Demo label="Humans" hint="the last one has a broken image URL and falls back">
        <DemoRow className="gap-4">
          <Avatar name="Ana Suarez" src={PORTRAITS.ana} size="lg" />
          <Avatar name="Marcus Chen" src={PORTRAITS.marcus} size="lg" />
          <Avatar name="Priya Patel" size="lg" />
          <Avatar name="Jo Berg" size="md" />
          <Avatar name="Sam Okafor" size="sm" />
          <Avatar name="Lena Fischer" src="/broken-avatar.png" size="lg" />
        </DemoRow>
      </Demo>

      <Demo label="Agents">
        <DemoRow className="gap-4">
          <Avatar name="billing agent" kind="agent" size="lg" />
          <Avatar name="support agent" kind="agent" size="md" />
          <Avatar name="deploy bot" kind="agent" size="sm" />
        </DemoRow>
      </Demo>

      <Demo label="In a row" hint="reviewers stack with a slight overlap">
        <div className="flex items-center">
          <div className="flex -space-x-1.5">
            <Avatar name="Ana Suarez" src={PORTRAITS.ana} className="ring-2 ring-surface" />
            <Avatar name="Marcus Chen" src={PORTRAITS.marcus} className="ring-2 ring-surface" />
            <Avatar name="Priya Patel" className="ring-2 ring-surface" />
          </div>
          <span className="ml-2.5 text-micro text-ink-muted">
            3 reviewers can approve
          </span>
        </div>
      </Demo>
    </Section>
  );
}
