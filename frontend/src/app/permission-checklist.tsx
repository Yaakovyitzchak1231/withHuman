import { Badge } from "@/ds/components/badge";
import { Button } from "@/ds/components/button";
import { Checkbox } from "@/ds/components/checkbox";
import { LockIcon } from "@/ds/icons";
import type { APIKeyPermission } from "../types";
import { areaLabel } from "./permission-labels";

/*
 * The permission picker a personal API key is scoped with, shared by
 * the create-key form and the OAuth consent page: the same vocabulary,
 * grouped by area, with the fresh-sign-in mark on what needs it.
 */

export interface PermissionArea {
  area: string;
  permissions: APIKeyPermission[];
}

/* Areas keep their first-seen order, which is the registry's order. */
export function groupPermissions(bearable: APIKeyPermission[]): PermissionArea[] {
  const groups: PermissionArea[] = [];
  for (const permission of bearable) {
    const group = groups.find((candidate) => candidate.area === permission.area);
    if (group) group.permissions.push(permission);
    else groups.push({ area: permission.area, permissions: [permission] });
  }
  return groups;
}

/* Deciding a request is the one permission that asks for a fresh
 * sign-in on its own; dangerous ones do too. */
export const needsFreshSignIn = (permission: APIKeyPermission) =>
  permission.dangerous || permission.key === "request.decide";

export function PermissionChecklist({
  areas,
  checked,
  onToggle,
  onSelectAll,
  onClear,
}: {
  areas: PermissionArea[];
  checked: ReadonlySet<string>;
  onToggle: (key: string, next: boolean) => void;
  onSelectAll: () => void;
  onClear: () => void;
}) {
  return (
    <div className="flex flex-col rounded-md border-hairline border-edge bg-surface">
      <div className="flex items-center justify-between border-b-hairline border-edge py-1 pr-1 pl-3">
        <span className="text-label text-ink-muted">
          {checked.size} selected
        </span>
        <span className="flex gap-1">
          <Button size="sm" variant="ghost" type="button" onClick={onSelectAll}>
            Select all
          </Button>
          <Button size="sm" variant="ghost" type="button" onClick={onClear}>
            Clear
          </Button>
        </span>
      </div>
      <div className="max-h-[360px] overflow-y-auto">
        {areas.length === 0 ? (
          <p className="px-3 py-3 text-body text-ink-muted">
            None of your permissions can be carried by a key.
          </p>
        ) : (
          areas.map((group) => (
            <div key={group.area}>
              <p className="bg-fill-hover px-3 py-1.5 text-label text-ink-secondary">
                {areaLabel(group.area)}
              </p>
              {group.permissions.map((permission) => (
                <label
                  key={permission.key}
                  className="flex cursor-pointer items-start gap-2.5 border-b-hairline border-edge px-3 py-2 last:border-b-0 hover:bg-fill-hover"
                >
                  <span className="flex h-5 shrink-0 items-center">
                    <Checkbox
                      aria-label={permission.key}
                      checked={checked.has(permission.key)}
                      onCheckedChange={(next) => onToggle(permission.key, next)}
                    />
                  </span>
                  <span className="flex min-w-0 flex-col gap-0.5">
                    <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                      <code className="font-mono text-[12px] text-ink">{permission.key}</code>
                      {needsFreshSignIn(permission) ? (
                        <Badge tone="neutral" icon={<LockIcon width={14} height={14} />}>
                          Needs a fresh sign-in
                        </Badge>
                      ) : null}
                    </span>
                    <span className="text-label font-normal text-ink-muted">
                      {permission.description}
                    </span>
                  </span>
                </label>
              ))}
            </div>
          ))
        )}
      </div>
    </div>
  );
}
