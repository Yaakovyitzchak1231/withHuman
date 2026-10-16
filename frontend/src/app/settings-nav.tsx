import { NavGroupLabel, NavItem } from "./nav-item";
import { useAccess } from "./authorization";
import { ExtensionSettingsNav, useExtensionSettingsNav } from "./extensions";
import { VERSION_PATH, useVersionPageAvailable } from "./installation";

/* The sidebar's contents while a settings page is open: two groups,
 * "You" for per-person preferences every member has, "Organization"
 * for the administration pages the person can read. The group hides
 * when none of its entries do. */
export function SettingsNav() {
  const { holdsAnywhere } = useAccess();
  const canReadOrganization = holdsAnywhere("organization.read");
  const canReadMembers = holdsAnywhere("membership.read");
  const canReadTeams = holdsAnywhere("team.read");
  const canReadConnections = holdsAnywhere("connection.read");
  const versionPage = useVersionPageAvailable();
  const extensionYou = useExtensionSettingsNav("you");
  const extensionOrganization = useExtensionSettingsNav("organization");

  const organization = [
    canReadOrganization ? (
      <NavItem key="organization" to="/settings/organization">
        Organization policies
      </NavItem>
    ) : null,
    canReadOrganization ? (
      <NavItem key="tool-presentations" to="/settings/tool-presentations">
        Tool presentations
      </NavItem>
    ) : null,
    canReadMembers ? (
      <NavItem key="members" to="/settings/members">
        Members
      </NavItem>
    ) : null,
    canReadTeams ? (
      <NavItem key="teams" to="/settings/teams">
        Teams
      </NavItem>
    ) : null,
    canReadConnections ? (
      <NavItem key="integrations" to="/settings/integrations">
        Integrations
      </NavItem>
    ) : null,
    versionPage ? (
      <NavItem key="version" to={VERSION_PATH}>
        Version
      </NavItem>
    ) : null,
    extensionOrganization.length > 0 ? (
      <ExtensionSettingsNav key="extensions" entries={extensionOrganization} />
    ) : null,
  ].filter(Boolean);

  return (
    <>
      <NavGroupLabel>You</NavGroupLabel>
      <NavItem to="/settings/notifications">Notifications</NavItem>
      <NavItem to="/settings/appearance">Appearance</NavItem>
      <NavItem to="/settings/account">Account</NavItem>
      <NavItem to="/settings/personal-api-keys">Personal API keys</NavItem>
      <ExtensionSettingsNav entries={extensionYou} />
      {organization.length > 0 ? (
        <>
          <NavGroupLabel>Organization</NavGroupLabel>
          {organization}
        </>
      ) : null}
    </>
  );
}
