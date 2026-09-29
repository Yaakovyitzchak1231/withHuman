import { useMemo } from "react";
import type { Principal, Resource, ScopeKind } from "../types";
import { useSession } from "./principal";

/* Pure authorization over the principal's grants, mirroring the server's
 * authorization package: can() answers one action against one resource,
 * scopeOf() says where a permission is held, and holdsAnywhere() is the
 * cheap navigation check. The server is the authority; these only decide
 * what the app offers. */

/* Whether the principal holds the permission at any scope. */
export function holdsAnywhere(principal: Principal, permission: string): boolean {
  return principal.grants.some((grant) => grant.permission === permission);
}

/* Whether a grant of the permission covers the resource. An organization
 * grant covers everything; a team, agent or pipeline grant covers only
 * the resource of the same kind and id (an agent's id is its slug, a
 * pipeline's its key). With no resource, the action is about the
 * organization itself. */
export function can(principal: Principal, permission: string, resource?: Resource): boolean {
  const target: Resource = resource ?? { kind: "organization" };
  return principal.grants.some((grant) => {
    if (grant.permission !== permission) return false;
    if (grant.scope_kind === "organization") return true;
    if (target.kind === "organization") return false;
    return grant.scope_kind === target.kind && grant.scope_id === target.id;
  });
}

export type ScopeSet = "all" | Array<{ kind: ScopeKind; id: string }>;

/* The scopes the principal holds the permission in: "all" when an
 * organization grant exists, else the list of team, agent and pipeline
 * scopes (empty when the permission is not held at all). */
export function scopeOf(principal: Principal, permission: string): ScopeSet {
  const held = principal.grants.filter((grant) => grant.permission === permission);
  if (held.some((grant) => grant.scope_kind === "organization")) return "all";
  const scopes: Array<{ kind: ScopeKind; id: string }> = [];
  for (const grant of held) {
    if (!grant.scope_id) continue;
    if (scopes.some((scope) => scope.kind === grant.scope_kind && scope.id === grant.scope_id)) {
      continue;
    }
    scopes.push({ kind: grant.scope_kind, id: grant.scope_id });
  }
  return scopes;
}

export interface Access {
  can: (permission: string, resource?: Resource) => boolean;
  holdsAnywhere: (permission: string) => boolean;
  scopeOf: (permission: string) => ScopeSet;
  /* Holds the built-in owner role at organization scope. Only owners
   * grant owner, so the pickers keep that role out of reach otherwise. */
  isOwner: boolean;
}

/* The helpers bound to the signed-in session. */
export function useAccess(): Access {
  const session = useSession();
  const principal = session.principal;
  const isOwner = session.assignments.some(
    (assignment) => assignment.is_owner && assignment.scope_kind === "organization",
  );
  return useMemo(
    () => ({
      can: (permission, resource) => can(principal, permission, resource),
      holdsAnywhere: (permission) => holdsAnywhere(principal, permission),
      scopeOf: (permission) => scopeOf(principal, permission),
      isOwner,
    }),
    [principal, isOwner],
  );
}
