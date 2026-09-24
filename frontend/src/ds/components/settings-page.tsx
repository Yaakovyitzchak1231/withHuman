import type { ReactNode } from "react";
import { cn } from "@/ds/lib/cn";
import { Page, type PageWidth } from "./page";
import { PageHeader } from "./page-header";

export interface SettingsPageProps {
  /* The way back to the list this page is a detour from: a
   * PageBackLink, on its own row above the title. */
  back?: ReactNode;
  title: string;
  /* Use the standard list measure when a settings table needs more columns. */
  width?: PageWidth;
  description?: ReactNode;
  /* The page's one primary affordance (Invite member, New team), on
   * the title line. */
  action?: ReactNode;
  /* SettingsSections and the odd Callout; they stack with room to
   * read as distinct groups. */
  children: ReactNode;
  className?: string;
}

/* Settings default to the narrow measure, with the Page measures available
 * for tables. One title line with its
 * description, then sections. The header never carries anything but a
 * title, a description, and at most one action, so pages line up
 * whatever they hold. */
export function SettingsPage({ back, title, description, action, children, className, width = "narrow" }: SettingsPageProps) {
  return (
    <Page width={width} className={cn("gap-8", className)}>
      <PageHeader back={back}>
        <div className="flex items-start justify-between gap-4">
          <div className="flex min-w-0 flex-col gap-1">
            <h1 className="text-title-lg text-ink">{title}</h1>
            {description ? (
              <p className="max-w-[560px] text-body text-ink-muted">{description}</p>
            ) : null}
          </div>
          {action ? <div className="flex shrink-0 items-center pt-0.5">{action}</div> : null}
        </div>
      </PageHeader>
      {children}
    </Page>
  );
}
