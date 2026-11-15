import { describe, expect, it } from "vitest";
import { preserveServerOnlyTeamSettings } from "./preserve-server-only-team-settings";

describe("preserveServerOnlyTeamSettings", () => {
  it("keeps task_contract and version when saving unrelated settings", () => {
    const out = preserveServerOnlyTeamSettings(
      { version: 2, task_contract: { enabled: true }, workspace_scope: "isolated" },
      { workspace_scope: "shared" },
    );
    expect(out).toEqual({ version: 2, task_contract: { enabled: true }, workspace_scope: "shared" });
  });

  it("does not add keys the team never had", () => {
    expect(preserveServerOnlyTeamSettings({}, { workspace_scope: "shared" })).toEqual({ workspace_scope: "shared" });
  });
});
