import { describe, expect, it } from "vitest";
import { buildAdvancedConfigUpdate, deriveAdvancedInitialValues } from "./channel-advanced-config";
import { getAdvancedFields } from "./channel-advanced-dialog";
import { configSchema } from "../channel-schemas";
import { ESSENTIAL_CONFIG_KEYS } from "./channel-advanced-config";

describe("channel advanced config payload", () => {
  it("removes a stale allow_from entry when the allowed users picker is cleared", () => {
    const existing = {
      dm_policy: "pairing",
      group_policy: "open",
      allow_from: ["195835936795841454"],
      groups: {
        "-100123": { allow_from: ["group-user"] },
      },
    };

    const values = deriveAdvancedInitialValues(existing);
    const next = buildAdvancedConfigUpdate(existing, { ...values, allow_from: undefined });

    expect(next).not.toHaveProperty("allow_from");
    expect(next.dm_policy).toBe("pairing");
    expect(next.group_policy).toBe("open");
    expect(next.groups).toEqual(existing.groups);
  });

  it("persists an explicit empty allow_from array when a tags field emits one", () => {
    const existing = {
      allow_from: ["195835936795841454"],
      proxy: "http://localhost:8080",
    };

    const next = buildAdvancedConfigUpdate(existing, {
      allow_from: [],
      proxy: "",
    });

    expect(next.allow_from).toEqual([]);
    expect(next).not.toHaveProperty("proxy");
  });

  it("flattens nested delivery config for editing and writes it back nested", () => {
    const existing = {
      chat_behavior: {
        final_split: {
          enabled: "inherit",
        },
      },
    };

    const values = deriveAdvancedInitialValues(existing);
    expect(values["chat_behavior.final_split.enabled"]).toBe("inherit");

    const next = buildAdvancedConfigUpdate(existing, {
      ...values,
      "chat_behavior.final_split.enabled": "off",
    });

    expect(next).toEqual({
      chat_behavior: {
        final_split: {
          enabled: "off",
        },
      },
    });
  });

  it("shows Telegram manager settings in the advanced edit dialog", () => {
    const groups = getAdvancedFields("telegram");

    expect(groups.telegramManagement.map((field) => field.key)).toEqual([
      "telegram_manager.enabled",
      "telegram_manager.allowed_actions",
    ]);
  });

  it("shows team task approval in the advanced edit dialog for existing Telegram channels", () => {
    expect(getAdvancedFields("telegram").teamTaskApproval.map((field) => field.key)).toEqual(["task_approval.enabled"]);
    expect(getAdvancedFields("discord").teamTaskApproval).toEqual([]);
  });

  it("puts every non-essential Telegram config field into an edit group", () => {
    const grouped = new Set(Object.values(getAdvancedFields("telegram")).flat().map((f) => f.key));
    const missing = configSchema.telegram!
      .filter((f) => !ESSENTIAL_CONFIG_KEYS.has(f.key) && !grouped.has(f.key))
      .map((f) => f.key);
    expect(missing).toEqual([]);
  });

  it("toggles task_approval.enabled without dropping other config", () => {
    const existing = {
      dm_policy: "pairing",
      proxy: "http://proxy:8080",
      telegram_manager: { enabled: true, allowed_actions: ["topic"] },
      groups: { "-100123": { require_mention: false } },
    };
    const values = deriveAdvancedInitialValues(existing);
    const enabled = buildAdvancedConfigUpdate(existing, { ...values, "task_approval.enabled": true });
    expect(enabled).toEqual({ ...existing, task_approval: { enabled: true } });

    const disabled = buildAdvancedConfigUpdate(enabled, {
      ...deriveAdvancedInitialValues(enabled),
      "task_approval.enabled": false,
    });
    expect(disabled).toEqual({ ...existing, task_approval: { enabled: false } });
  });
});
