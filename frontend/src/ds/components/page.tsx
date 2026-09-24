import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  type ComponentProps,
  type CSSProperties,
} from "react";
import { cn } from "@/ds/lib/cn";
import { SIDEBAR_REOPEN_SIZE, SidebarReopenControl, useSidebar } from "./sidebar-reopen";

/*
 * The column every workspace screen lays its content in: centred, 32px
 * of padding on every side (16px on a phone-width pane), and one of
 * three measures. A screen picks by what it holds, not by taste: narrow
 * for something you read and act on, default for lists and forms, wide
 * for dense tables.
 *
 * While the sidebar is collapsed the shell floats the reopen control
 * at the pane's corner. A page centred with room leaves it there. One
 * hugging the pane's edge, whose title would sit against that corner,
 * claims the control and places it on the title line at its own inset,
 * moving the title's first line right so it starts 12px after it.
 * Nothing moves vertically, so collapsing the sidebar never shifts a
 * page.
 */
export type PageWidth = "narrow" | "default" | "wide";

const WIDTHS: Record<PageWidth, string> = {
  narrow: "max-w-[760px]",
  default: "max-w-[980px]",
  wide: "max-w-[1240px]",
};

/* The padding is a pair of variables so the control can be placed
 * against it. The header offset is one too, so a screen whose header
 * does not start with a title (a back button, say) can take it with
 * pl-(--page-header-offset); a title takes it through its first line. */
const COLUMN = [
  "relative mx-auto flex w-full flex-col gap-6",
  "[--page-px:32px] [--page-pt:32px] px-(--page-px) py-(--page-pt)",
  "@max-[640px]/pane:[--page-px:16px] @max-[640px]/pane:[--page-pt:20px]",
  "[&>:first-child:not([data-page-back])_h1]:indent-(--page-header-offset)",
].join(" ");

/* From the control's left edge to where the title starts: the control
 * and a 12px gap. */
const REACH = SIDEBAR_REOPEN_SIZE + 12;

/* A page inside another page (a request rendered within the guided
 * setup) is a column, not the top of the pane: no control, no offset. */
const NestedContext = createContext(false);

export interface PageProps extends ComponentProps<"div"> {
  width?: PageWidth;
}

export function Page({ width = "default", className, style, children, ...props }: PageProps) {
  const nested = useContext(NestedContext);
  const sidebar = useSidebar();
  const ref = useRef<HTMLDivElement>(null);
  /* How far the page sits from the pane's edge when centred. */
  const [slack, setSlack] = useState(0);
  useEffect(() => {
    const page = ref.current;
    const pane = page?.parentElement;
    if (!page || !pane) return;
    const measure = () => setSlack(Math.max(0, (pane.clientWidth - page.offsetWidth) / 2));
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(pane);
    observer.observe(page);
    return () => observer.disconnect();
  }, []);

  const placesControl = !nested && sidebar?.collapsed === true && slack < REACH;
  const offset = placesControl ? REACH - slack : 0;
  const vars = { "--page-header-offset": `${offset}px` } as CSSProperties;
  const claim = sidebar?.claim;
  useEffect(() => {
    if (!claim) return;
    claim(placesControl);
    return () => claim(false);
  }, [claim, placesControl]);

  return (
    <NestedContext.Provider value>
      <div
        ref={ref}
        className={cn(COLUMN, WIDTHS[width], className)}
        style={{ ...vars, ...style }}
        {...props}
      >
        {children}
        {/* After the content, so the screen's header stays the first
            child the title rule above reaches for. */}
        {placesControl ? (
          <SidebarReopenControl
            className="absolute z-20"
            style={{
              top: "calc(var(--page-pt) + 1px)",
              left: `calc(var(--page-px) - ${slack}px)`,
            }}
          />
        ) : null}
      </div>
    </NestedContext.Provider>
  );
}
