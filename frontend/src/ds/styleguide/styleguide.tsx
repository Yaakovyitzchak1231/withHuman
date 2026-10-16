import { useEffect } from "react";
import { extensions, type StyleguideSection } from "@/app/extensions";
import { ThemeProvider } from "@/ds/theme/theme-provider";
import { ThemeToggle } from "@/ds/theme/theme-toggle";
import { ColorsSection } from "./sections/colors";
import { LogoSection } from "./sections/logo";
import { TypographySection } from "./sections/typography";
import { MarkdownSection } from "./sections/markdown";
import { SurfacesSection } from "./sections/surfaces";
import { ButtonsSection } from "./sections/buttons";
import { KbdSection } from "./sections/kbd";
import { IconButtonsSection } from "./sections/icon-buttons";
import { BadgesSection } from "./sections/badges";
import { TextFieldsSection } from "./sections/text-fields";
import { TogglesSection } from "./sections/toggles";
import { TooltipsSection } from "./sections/tooltips";
import { SparklinesSection } from "./sections/sparklines";
import { HoverCardsSection } from "./sections/hover-cards";
import { InfoCardsSection } from "./sections/info-cards";
import { MenusSection } from "./sections/menus";
import { DialogsSection } from "./sections/dialogs";
import { CardsSection } from "./sections/cards";
import { TabsSection } from "./sections/tabs";
import { SelectsSection } from "./sections/selects";
import { CompoundSelectSection } from "./sections/compound-select";
import { ToastsSection } from "./sections/toasts";
import { CommandPaletteSection } from "./sections/command-palette";
import { AvatarsSection } from "./sections/avatars";
import { CalloutsSection } from "./sections/callouts";
import { LoadingEmptySection } from "./sections/loading-empty";
import { ListsSection } from "./sections/lists";
import { ToggleGroupsSection } from "./sections/toggle-groups";
import { DetailBlocksSection } from "./sections/detail-blocks";
import { SettingsSection_ } from "./sections/settings";
import { TablesSection } from "./sections/tables";
import { RevisionHistorySection } from "./sections/revision-history";
import { TimelinesSection } from "./sections/timelines";
import { SteppersSection } from "./sections/steppers";
import { ChecklistsSection } from "./sections/checklists";
import { PolicyBuilderSection } from "./sections/policy-builder";
import { ReviewerRoutingSection } from "./sections/reviewer-routing";
import { SchedulingSection } from "./sections/scheduling";
import { AccessSection } from "./sections/access";
import { ToastProvider } from "@/ds/components/toast";

/* Nav and content render from one registry so they cannot drift. */
const CORE_SECTIONS: StyleguideSection[] = [
  { id: "logo", label: "Logo", Component: LogoSection },
  { id: "colors", label: "Color", Component: ColorsSection },
  { id: "typography", label: "Typography", Component: TypographySection },
  { id: "markdown", label: "Markdown", Component: MarkdownSection },
  { id: "surfaces", label: "Surfaces & motion", Component: SurfacesSection },
  { id: "buttons", label: "Button", Component: ButtonsSection },
  { id: "kbd", label: "Kbd", Component: KbdSection },
  { id: "icon-buttons", label: "IconButton", Component: IconButtonsSection },
  { id: "badges", label: "Badges", Component: BadgesSection },
  { id: "avatars", label: "Avatar", Component: AvatarsSection },
  { id: "text-fields", label: "TextField", Component: TextFieldsSection },
  { id: "toggles", label: "Switch & Checkbox", Component: TogglesSection },
  { id: "toggle-groups", label: "ToggleGroup", Component: ToggleGroupsSection },
  { id: "scheduling", label: "Scheduling fields", Component: SchedulingSection },
  { id: "cards", label: "Card", Component: CardsSection },
  { id: "lists", label: "List", Component: ListsSection },
  { id: "tables", label: "Table", Component: TablesSection },
  { id: "revision-history", label: "RevisionHistory", Component: RevisionHistorySection },
  { id: "detail-blocks", label: "Detail blocks", Component: DetailBlocksSection },
  { id: "timelines", label: "Timeline", Component: TimelinesSection },
  { id: "steppers", label: "Stepper", Component: SteppersSection },
  { id: "checklists", label: "Checklist", Component: ChecklistsSection },
  { id: "policy-builder", label: "Policy builder", Component: PolicyBuilderSection },
  { id: "reviewer-routing", label: "Reviewer routing", Component: ReviewerRoutingSection },
  { id: "settings", label: "Settings", Component: SettingsSection_ },
  { id: "access", label: "Access control", Component: AccessSection },
  { id: "tabs", label: "Tabs", Component: TabsSection },
  { id: "selects", label: "Select", Component: SelectsSection },
  { id: "compound-select", label: "CompoundSelect", Component: CompoundSelectSection },
  { id: "sparklines", label: "Sparkline", Component: SparklinesSection },
  { id: "tooltips", label: "Tooltip", Component: TooltipsSection },
  { id: "hover-cards", label: "Hover card", Component: HoverCardsSection },
  { id: "info-cards", label: "Info card", Component: InfoCardsSection },
  { id: "menus", label: "Menu", Component: MenusSection },
  { id: "dialogs", label: "Dialog", Component: DialogsSection },
  { id: "toasts", label: "Toast", Component: ToastsSection },
  { id: "callouts", label: "Callout", Component: CalloutsSection },
  { id: "loading-empty", label: "Spinner & EmptyState", Component: LoadingEmptySection },
  { id: "command-palette", label: "Command palette", Component: CommandPaletteSection },
];

/* Core's sections, with the sections of the block kinds this edition
 * registered right after the policy builder they extend, so the bundle
 * that carries a kind's components is the one that documents them. */
function sections(): StyleguideSection[] {
  const kinds = extensions.blockKinds.flatMap((kind) => (kind.styleguide ? [kind.styleguide] : []));
  const after = CORE_SECTIONS.findIndex((section) => section.id === "policy-builder") + 1;
  return [...CORE_SECTIONS.slice(0, after), ...kinds, ...CORE_SECTIONS.slice(after)];
}

export function Styleguide() {
  useEffect(() => {
    document.title = "wH: Styleguide";
  }, []);
  const SECTIONS = sections();
  return (
    <ThemeProvider>
      <ToastProvider>
      <div className="ds-root min-h-screen">
        <header className="sticky top-0 z-10 border-b-hairline border-edge bg-canvas/90 backdrop-blur-md">
          <div className="mx-auto flex h-12 max-w-[1080px] items-center justify-between px-6">
            <span className="font-mono text-label text-ink">
              withHuman <span className="text-ink-faint">/</span>{" "}
              <span className="text-ink-muted">design</span>
            </span>
            <ThemeToggle />
          </div>
        </header>

        <div className="mx-auto flex max-w-[1080px] gap-12 px-6 py-10">
          <nav
            aria-label="Styleguide sections"
            className="sticky top-24 hidden h-max max-h-[calc(100dvh-7.5rem)] w-40 shrink-0 overflow-y-auto overscroll-contain md:block"
          >
            <p className="mb-2 text-micro text-ink-faint">Contents</p>
            <ul className="flex flex-col">
              {SECTIONS.map((section) => (
                <li key={section.id}>
                  <a
                    href={`#${section.id}`}
                    className="block rounded-sm px-2 py-1 text-label font-normal text-ink-muted transition-colors duration-100 hover:bg-fill-hover hover:text-ink"
                  >
                    {section.label}
                  </a>
                </li>
              ))}
            </ul>
          </nav>

          <main className="flex min-w-0 flex-1 flex-col gap-16 pb-32">
            <div>
              <h1 className="font-display text-display text-ink">
                Design system
              </h1>
              <p className="mt-2 max-w-[560px] text-body-lg text-ink-muted">
                The working reference for the withHuman product interface.
                Built for reviewing consequential actions: dense, quiet, and
                sturdy.
              </p>
            </div>
            {SECTIONS.map(({ id, Component }) => (
              <Component key={id} />
            ))}
          </main>
        </div>
      </div>
      </ToastProvider>
    </ThemeProvider>
  );
}
