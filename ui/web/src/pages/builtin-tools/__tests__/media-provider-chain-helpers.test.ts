import { describe, expect, it } from "vitest";
import { buildChainSettings, parseImageInputMode, resolveDynamicParamField } from "../media-provider-chain-helpers";
import type { ParamField } from "../media-provider-params-schema";

const imageModelField: ParamField = {
  key: "image_model",
  label: "Image model",
  type: "select",
  optionsSource: "image_models",
};

const source = {
  imageModels: [
    { id: "gpt-image-2.5-flare", name: "GPT Image 2.5 Flare" },
    { id: "gpt-image-2", name: "GPT Image 2" },
  ],
  defaultImageModel: "gpt-image-2.5-flare",
};

describe("resolveDynamicParamField", () => {
  it("fills options and default from the fetched image catalog", () => {
    const field = resolveDynamicParamField(imageModelField, source, undefined, "recommended");
    expect(field.default).toBe("gpt-image-2.5-flare");
    expect(field.options).toEqual([
      { value: "gpt-image-2.5-flare", label: "GPT Image 2.5 Flare · gpt-image-2.5-flare (recommended)" },
      { value: "gpt-image-2", label: "GPT Image 2 · gpt-image-2" },
    ]);
  });

  it("keeps a stored model the catalog no longer lists", () => {
    const field = resolveDynamicParamField(imageModelField, source, "gpt-image-1.5", "recommended");
    expect(field.options?.[field.options.length - 1]).toEqual({ value: "gpt-image-1.5", label: "gpt-image-1.5" });
  });

  it("leaves static fields untouched", () => {
    const sizeField: ParamField = { key: "size", label: "Size", type: "select", default: "1024*1024" };
    expect(resolveDynamicParamField(sizeField, source, undefined, "recommended")).toBe(sizeField);
  });
});

describe("buildChainSettings", () => {
  const providers = [{ provider_id: "", provider: "p", model: "m", enabled: true, timeout: 120, max_retries: 2, params: {} }];

  it("keeps image_input when another tool saves", () => {
    const next = buildChainSettings({ image_input: "file_ref", other: 1 }, providers, null);
    expect(next).toEqual({ image_input: "file_ref", other: 1, providers });
  });

  it("sets the selected read_image mode", () => {
    expect(buildChainSettings({}, providers, "inline").image_input).toBe("inline");
  });

  it("removes the key for auto", () => {
    expect(buildChainSettings({ image_input: "inline" }, providers, "auto")).toEqual({ providers });
  });

  it("drops the legacy flat provider and model", () => {
    expect(buildChainSettings({ provider: "old", model: "old-model" }, [], null)).toEqual({ providers: [] });
  });
});

describe("parseImageInputMode", () => {
  it("falls back to auto for missing or unknown values", () => {
    expect(parseImageInputMode({})).toBe("auto");
    expect(parseImageInputMode({ image_input: "x" })).toBe("auto");
    expect(parseImageInputMode({ image_input: "file_ref" })).toBe("file_ref");
  });
});
