import { useEffect, useState } from "react";
import { api } from "../api";
import type { Installation } from "../types";
import { useAccess } from "./authorization";
import type { TodoItem } from "./extensions";
import { useRuntimeConfig } from "./use-runtime-config";

/* The update notice's logic, kept free of screen components because the
 * shell's to-do bell imports it through extensions.tsx. */

export const VERSION_PATH = "/settings/version";


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
