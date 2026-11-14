import { describe, expect, it } from "vitest";
import en from "./locales/en/memory.json";
import vi from "./locales/vi/memory.json";
import zh from "./locales/zh/memory.json";

function leafKeys(value: Record<string, unknown>, prefix = ""): string[] {
  return Object.entries(value).flatMap(([key, entry]) => {
    const path = `${prefix}${key}`;
    return typeof entry === "object" && entry !== null
      ? leafKeys(entry as Record<string, unknown>, `${path}.`)
      : [path];
  });
}

describe("memory translations", () => {
  for (const [language, catalog] of Object.entries({ vi, zh })) {
    it(`${language} includes every English memory key`, () => {
      const translated = new Set(leafKeys(catalog));
      expect(leafKeys(en).filter((key) => !translated.has(key))).toEqual([]);
    });
  }
});
