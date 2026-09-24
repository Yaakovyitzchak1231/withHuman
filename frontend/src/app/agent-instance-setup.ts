import type { RuntimeConfig } from "./use-runtime-config";

const DEFAULT_DOWNLOAD_URL = "https://downloads.withhuman.ai";

/** Quote one POSIX shell argument, leaving simple URLs and slugs readable. */
function shellQuote(value: string): string {
  return /^[a-zA-Z0-9_./:@%-]+$/.test(value)
    ? value
    : `'${value.replaceAll("'", "'\"'\"'")}'`;
}

export function agentInstanceSetup(
  config: RuntimeConfig | null,
  agentSlug: string,
): { command: string; providerURL: string } | null {
  if (!config?.public_origin || !config.cli_download_url) return null;
  const origin = config.public_origin.replace(/\/+$/, "");
  const downloads = config.cli_download_url.replace(/\/+$/, "");
  try {
    for (const value of [origin, downloads]) {
      const url = new URL(value);
      if (
        !["http:", "https:"].includes(url.protocol) ||
        url.username ||
        url.password ||
        url.search ||
        url.hash
      ) {
        return null;
      }
    }
    if (new URL(origin).pathname !== "/") return null;
  } catch {
    return null;
  }
  const mirror =
    downloads === DEFAULT_DOWNLOAD_URL
      ? ""
      : `WITHHUMAN_CLI_BASE=${shellQuote(downloads)} `;
  return {
    command: `curl -fsSL ${shellQuote(`${downloads}/install.sh`)} | ${mirror}sh -s -- agent install --agent ${shellQuote(agentSlug)} --url ${shellQuote(origin)}`,
    providerURL: `${origin}/api/aap`,
  };
}
