import type { ReactNode } from "react";
import { cn } from "@/ds/lib/cn";

export interface SettingRowProps {
  /* A leading 16px icon, for rows that are things (a channel, a phone)
   * rather than options. */
  icon?: ReactNode;
  label: ReactNode;
  description?: ReactNode;
  /* The control on the right: a Switch, Select, Button, badge, or field. */
  children?: ReactNode;
  /* Content the row unfolds below itself, for the one row that needs
   * something from you (a code to enter). */
  panel?: ReactNode;
  className?: string;
}

/* One setting: label and description on the left, its control on the
 * right. Stack rows inside a SettingsSection; they separate with
 * hairlines. */
export function SettingRow({
  icon,
  label,
  description,
  children,
  panel,
  className,
}: SettingRowProps) {
  return (
    <div className={cn("border-b-hairline border-edge last:border-b-0", className)}>
      <div className="flex min-h-[52px] items-center justify-between gap-8 py-3">
        <div className="flex min-w-0 items-center gap-3">
          {icon ? <span className="flex shrink-0 text-ink-muted">{icon}</span> : null}
          <div className="flex min-w-0 flex-col gap-0.5">
            <span className="text-body text-ink">{label}</span>
            {description ? (
              <span className="max-w-[400px] text-label font-normal text-ink-muted">
                {description}
              </span>
            ) : null}
          </div>
        </div>
        {children ? (
          <div className="flex shrink-0 items-center gap-2">{children}</div>
        ) : null}
      </div>
      {panel ? <div className="pb-4">{panel}</div> : null}
    </div>
  );
}
