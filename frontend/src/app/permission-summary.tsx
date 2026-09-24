import { useRef, useState } from "react";
import { Popover } from "@base-ui/react/popover";
import { Badge } from "@/ds/components/badge";
import { TextField } from "@/ds/components/text-field";
import { HoverCardTitle, HoverCardBody } from "@/ds/components/hover-card";
import { ShieldIcon } from "@/ds/icons";
import { permissionLabel } from "./permission-labels";
import type { PermissionDescriptor, RoleAssignment } from "../types";

export function AssignmentBadge({
  assignment,
}: {
  assignment: RoleAssignment;
  holder?: "team";
}) {
  if (assignment.is_owner) return <Badge>Organization ownership</Badge>;
  const scope =
    assignment.scope_kind === "organization"
      ? "Entire organization"
      : assignment.scope_name || assignment.scope_id;
  return (
    <Badge>
      {assignment.permissions.length} permissions · {scope}
      {assignment.via_team_name ? ` · via ${assignment.via_team_name}` : ""}
    </Badge>
  );
}

/** Count each permission once without dropping its distinct scopes or sources. */
function permissionRows(assignments: RoleAssignment[]) {
  const rows = new Map<string, Map<string, RoleAssignment>>();
  for (const assignment of assignments) {
    const sourceId = JSON.stringify([
      assignment.via_team_id ?? "",
      assignment.via_team_name ?? "",
      assignment.source,
      assignment.scope_kind,
      assignment.scope_id ?? "",
      assignment.is_owner,
    ]);
    for (const permission of assignment.permissions) {
      const sources = rows.get(permission) ?? new Map<string, RoleAssignment>();
      sources.set(sourceId, assignment);
      rows.set(permission, sources);
    }
  }
  return [...rows.entries()].sort(([a], [b]) => a.localeCompare(b));
}

export function PermissionsSummary({
  assignments,
  definitions = [],
}: {
  assignments: RoleAssignment[];
  definitions?: PermissionDescriptor[];
}) {
  const [query, setQuery] = useState("");
  const contentRef = useRef<HTMLDivElement>(null);
  const rows = permissionRows(assignments);
  const label = `${rows.length} ${rows.length === 1 ? "permission" : "permissions"}`;
  const descriptions = new Map(
    definitions.map((permission) => [permission.key, permission.description]),
  );
  const search = query.trim().toLowerCase();
  const visibleRows = rows.filter(([key]) =>
    [key, permissionLabel(key, descriptions.get(key)), descriptions.get(key)]
      .join(" ")
      .toLowerCase()
      .includes(search),
  );
  if (!rows.length)
    return <span className="text-label text-ink-muted">No permissions</span>;
  return (
    <Popover.Root
      onOpenChange={(open, details) => {
        // Keep the search usable while the pointer moves away during typing.
        if (
          !open &&
          details.reason === "trigger-hover" &&
          contentRef.current?.contains(document.activeElement)
        ) {
          details.cancel();
          return;
        }
        if (!open) setQuery("");
      }}
    >
      <Popover.Trigger
        openOnHover
        delay={300}
        closeDelay={100}
        onClick={(event) => event.stopPropagation()}
        onDoubleClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => event.stopPropagation()}
        className="inline-flex max-w-full cursor-pointer rounded-full outline-none transition-colors hover:bg-fill-hover data-popup-open:bg-fill-hover focus-visible:ring-2 focus-visible:ring-accent"
      >
        <Badge
          icon={<ShieldIcon width={14} height={14} />}
          className="max-w-full"
        >
          <span className="truncate">{label}</span>
        </Badge>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Positioner
          side="bottom"
          align="start"
          sideOffset={6}
          collisionPadding={12}
        >
          <Popover.Popup
            ref={contentRef}
            onClick={(event) => event.stopPropagation()}
            onDoubleClick={(event) => event.stopPropagation()}
            onKeyDown={(event) => event.stopPropagation()}
            className="ds-root w-[440px] max-w-[calc(100vw-24px)] rounded-md bg-surface-raised p-3.5 text-body text-ink shadow-overlay ring-hairline outline-none"
          >
            <Popover.Title render={<HoverCardTitle />}>{label}</Popover.Title>
            <div className="mt-3">
              <TextField
                type="search"
                aria-label="Search permissions"
                placeholder="Search permissions"
                value={query}
                onValueChange={setQuery}
              />
            </div>
            <HoverCardBody className="max-h-[min(440px,60vh)] overflow-y-auto overscroll-contain pt-0">
              {visibleRows.length === 0 ? (
                <p role="status" className="py-6 text-center text-ink-muted">
                  No permissions match your search.
                </p>
              ) : (
                <table
                  className="w-full table-fixed border-collapse text-left text-label leading-5 [overflow-wrap:anywhere]"
                  aria-label="Permissions and access scopes"
                >
                  <thead className="sticky top-0 bg-surface-raised">
                    <tr>
                      <th
                        scope="col"
                        className="w-[44%] py-2 pr-4 text-micro font-medium text-ink-muted"
                      >
                        Permission
                      </th>
                      <th
                        scope="col"
                        className="py-2 text-micro font-medium text-ink-muted"
                      >
                        Applies to
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {visibleRows.map(([key, sources]) => (
                      <tr key={key} className="border-t-hairline border-edge">
                        <th
                          scope="row"
                          className="py-3 pr-4 align-top font-medium text-ink"
                          title={descriptions.get(key)}
                        >
                          {permissionLabel(key, descriptions.get(key))}
                        </th>
                        <td className="py-3 align-top">
                          <ul className="m-0 flex list-none flex-col gap-3 p-0">
                            {[...sources.entries()].map(
                              ([sourceId, assignment]) => {
                                const team =
                                  assignment.via_team_name ||
                                  assignment.via_team_id;
                                const source = assignment.is_owner
                                  ? "Organization ownership"
                                  : team
                                    ? `Via ${team}${assignment.source === "directory" ? " (directory)" : ""}`
                                    : assignment.source === "directory"
                                      ? "Directory-managed"
                                      : "Direct";
                                return (
                                  <li key={sourceId}>
                                    <div className="text-ink">
                                      {assignment.scope_kind ===
                                      "organization" ? (
                                        "Entire organization"
                                      ) : (
                                        <>
                                          <span className="text-ink-secondary">
                                            {assignment.scope_kind === "team"
                                              ? "Team"
                                              : "Agent"}
                                            :{" "}
                                          </span>
                                          <span>
                                            {assignment.scope_name ||
                                              assignment.scope_id}
                                          </span>
                                          {assignment.scope_kind === "agent" &&
                                          assignment.scope_name ? (
                                            <span className="ml-1.5 font-mono text-micro text-ink-muted">
                                              {assignment.scope_id}
                                            </span>
                                          ) : null}
                                        </>
                                      )}
                                    </div>
                                    <div className="mt-0.5 text-micro leading-4 text-ink-muted">
                                      {source}
                                    </div>
                                  </li>
                                );
                              },
                            )}
                          </ul>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </HoverCardBody>
          </Popover.Popup>
        </Popover.Positioner>
      </Popover.Portal>
    </Popover.Root>
  );
}
