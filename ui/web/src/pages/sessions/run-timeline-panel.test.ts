import { describe, expect, it } from "vitest";
import { getDeliveryAttempts, getRunTimelineDisplay } from "./run-timeline-panel";

describe("getRunTimelineDisplay", () => {
  it("marks failed items as destructive", () => {
    const display = getRunTimelineDisplay({ item_type: "tool.result", status: "failed" });
    expect(display.labelKey).toBe("detail.timeline.failed");
    expect(display.dotClass).toContain("destructive");
  });

  it("distinguishes tool call and assistant message items", () => {
    expect(getRunTimelineDisplay({ item_type: "tool.call", status: "running" }).labelKey).toBe(
      "detail.timeline.toolCall",
    );
    expect(getRunTimelineDisplay({ item_type: "assistant.message", status: "completed" }).labelKey).toBe(
      "detail.timeline.assistant",
    );
  });

  it("labels delivered and undelivered files", () => {
    expect(getRunTimelineDisplay({ item_type: "delivery", status: "completed" }).labelKey).toBe(
      "detail.timeline.delivery",
    );
    const failed = getRunTimelineDisplay({ item_type: "delivery", status: "failed" });
    expect(failed.labelKey).toBe("detail.timeline.deliveryFailed");
    expect(failed.dotClass).toContain("destructive");
  });

  it("reads attempts only from delivery items", () => {
    expect(getDeliveryAttempts({ item_type: "delivery", metadata: { attempts: 3 } })).toBe(3);
    expect(getDeliveryAttempts({ item_type: "delivery", metadata: {} })).toBe(0);
    expect(getDeliveryAttempts({ item_type: "tool.result", metadata: { attempts: 3 } })).toBe(0);
  });
});
