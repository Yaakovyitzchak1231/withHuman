import { useEffect, useState } from "react";
import { useRouter } from "@tanstack/react-router";
import { Badge, type Tone } from "@/ds/components/badge";
import { Button } from "@/ds/components/button";
import { Callout } from "@/ds/components/callout";
import { CopyButton } from "@/ds/components/copy-button";
import { SettingRow } from "@/ds/components/setting-row";
import { SettingsPage } from "@/ds/components/settings-page";
import { SettingsSection } from "@/ds/components/settings-section";
import { api } from "../api";
import type { Installation } from "../types";
import { useAccess } from "./authorization";
import type { TodoItem } from "./extensions";
import { useRuntimeConfig } from "./use-runtime-config";
import { useTitle } from "./use-title";

export const VERSION_PATH = "/settings/version";
const DESCRIPTION = "See which release you run and whether an update is available.";

const SEVERITY_TONE: Record<string, Tone> = {
  critical: "deny",
  high: "deny",
  medium: "warn",
  low: "neutral",
};

/* Whether this person gets the Version page: the deployment checks for
 * updates and they administer the organization. */
export function useVersionPageAvailable(): boolean {
  const { config } = useRuntimeConfig();
  const { holdsAnywhere } = useAccess();
  return config?.update_checks_enabled === true && holdsAnywhere("organization.settings.write");
}

/* The to-do bell's item when a newer release or a security fix is
 * waiting. Null where the deployment never checks, so an open-edition
 * deployment with telemetry off shows no bell. Read when the shell
 * mounts and again when the window regains focus; a failed read keeps
 * the last answer. */
export function useUpdateTodos(): TodoItem[] | null {
  const available = useVersionPageAvailable();
  const [installation, setInstallation] = useState<Installation | null>(null);
  useEffect(() => {
    if (!available) return;
    let cancelled = false;
    function read() {
      api
        .installation()
        .then((result) => {
          if (!cancelled) setInstallation(result);
        })
        .catch(() => undefined);
    }
    read();
    window.addEventListener("focus", read);
    return () => {
      cancelled = true;
      window.removeEventListener("focus", read);
    };
  }, [available]);

  if (!available) return null;
  return updateTodos(installation);
}

export function updateTodos(installation: Installation | null): TodoItem[] {
  if (!installation) return [];
  if (installation.advisories.length > 0) {
    return [
      {
        key: "installation:security",
        title: "Update withHuman",
        description: "A security fix is available.",
        to: VERSION_PATH,
      },
    ];
  }
  if (installation.update_available && installation.latest_version) {
    return [
      {
        key: "installation:update",
        title: "Update withHuman",
        description: `Version ${installation.latest_version} is available.`,
        to: VERSION_PATH,
      },
    ];
  }
  return [];
}

export interface VersionPage {
  installation: Installation | null;
  error: string;
}

export function loadVersionPage(): Promise<VersionPage> {
  return api.installation().then(
    (installation) => ({ installation, error: "" }),
    (reason: unknown) => ({
      installation: null,
      error: reason instanceof Error ? reason.message : "Could not load the version",
    }),
  );
}

export function VersionScreen({ page }: { page: VersionPage }) {
  useTitle("Version");
  const router = useRouter();
  const { installation, error } = page;

  return (
    <SettingsPage title="Version" description={DESCRIPTION}>
      {error ? (
        <Callout tone="warn" title="Could not load the version">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span>{error}</span>
            <Button size="sm" variant="ghost" onClick={() => void router.invalidate()}>
              Try again
            </Button>
          </div>
        </Callout>
      ) : !installation ? null : (
        <>
          <SettingsSection title="Release">
            <SettingRow label="Running version">
              <span className="font-mono text-label text-ink">{installation.version}</span>
            </SettingRow>
            <SettingRow
              label="Latest release"
              description={installation.latest_version ? undefined : "Not checked yet."}
            >
              {installation.latest_version ? (
                <>
                  <span className="font-mono text-label text-ink">{installation.latest_version}</span>
                  {installation.update_available ? (
                    <Badge tone="warn">Update available</Badge>
                  ) : (
                    <Badge tone="approve">Up to date</Badge>
                  )}
                </>
              ) : null}
            </SettingRow>
          </SettingsSection>
          {installation.advisories.length > 0 ? (
            <SettingsSection title="Security advisories">
              {installation.advisories.map((advisory) => (
                <SettingRow
                  key={advisory.id}
                  label={advisory.summary}
                  description={
                    advisory.fixed_in ? `Fixed in ${advisory.fixed_in}.` : "No fixed release yet."
                  }
                >
                  <Badge tone={SEVERITY_TONE[advisory.severity] ?? "warn"}>
                    {advisory.severity
                      ? advisory.severity[0].toUpperCase() + advisory.severity.slice(1)
                      : "Unrated"}
                  </Badge>
                  <a
                    href={advisory.url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-label text-accent hover:underline"
                  >
                    {advisory.id}
                  </a>
                </SettingRow>
              ))}
            </SettingsSection>
          ) : null}
          <SettingsSection title="Telemetry">
            <SettingRow
              label="Installation ID"
              description="Sent once a day with the running version."
            >
              <span className="font-mono text-label text-ink-muted">{installation.id}</span>
              <CopyButton value={installation.id} aria-label="Copy installation ID" />
            </SettingRow>
          </SettingsSection>
        </>
      )}
    </SettingsPage>
  );
}
