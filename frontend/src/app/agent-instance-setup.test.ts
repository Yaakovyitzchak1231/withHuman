import { describe, expect, it } from "vitest";
import { agentInstanceSetup } from "./agent-instance-setup";
import type { RuntimeConfig } from "./use-runtime-config";

const config = {
  public_origin: "https://app.withhuman.ai/",
  cli_download_url: "https://downloads.withhuman.ai/",
} as RuntimeConfig;

describe("instance setup commands", () => {
  it("connects the chosen agent using the public provider origin", () => {
    expect(agentInstanceSetup(config, "support-assistant")).toEqual({
      command:
        "curl -fsSL https://downloads.withhuman.ai/install.sh | sh -s -- agent install --agent support-assistant --url https://app.withhuman.ai",
      providerURL: "https://app.withhuman.ai/api/aap",
    });
  });

  it("passes a download mirror to the installer and safely quotes shell arguments", () => {
    const mirror = "https://downloads.example.test/team's $(exit 9)`exit 8`";
    const result = agentInstanceSetup(
      {
        ...config,
        public_origin: "http://localhost:8080/",
        cli_download_url: mirror,
      },
      "support",
    );
    expect(result?.providerURL).toBe("http://localhost:8080/api/aap");
    expect(result?.command).toBe(
      "curl -fsSL 'https://downloads.example.test/team'\"'\"'s $(exit 9)`exit 8`/install.sh' | WITHHUMAN_CLI_BASE='https://downloads.example.test/team'\"'\"'s $(exit 9)`exit 8`' sh -s -- agent install --agent support --url http://localhost:8080",
    );
  });

  it("withholds setup when runtime configuration is missing or invalid", () => {
    expect(agentInstanceSetup(null, "support")).toBeNull();
    for (const public_origin of [
      "",
      "not a URL",
      "file:///tmp",
      "https://example.test/path",
      "https://example.test?x=1",
      "https://user:secret@example.test",
    ]) {
      expect(
        agentInstanceSetup({ ...config, public_origin }, "support"),
      ).toBeNull();
    }
    expect(
      agentInstanceSetup({ ...config, cli_download_url: "" }, "support"),
    ).toBeNull();
  });
});
