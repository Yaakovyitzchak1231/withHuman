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
import { useTitle } from "./use-title";

const DESCRIPTION = "See which release you run and whether an update is available.";

const SEVERITY_TONE: Record<string, Tone> = {
  critical: "deny",
  high: "deny",
  medium: "warn",
  low: "neutral",
};

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
