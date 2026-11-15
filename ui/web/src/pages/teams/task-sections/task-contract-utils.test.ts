import { describe, expect, it } from "vitest";
import {
  EMPTY_CONTRACT_FORM, buildContractInput, getAcceptanceReport, getCheckpointPending,
  getLatestClarifyQuestion, getTaskContract, isContractRequired, validateContractForm,
} from "./task-contract-utils";

describe("isContractRequired", () => {
  it("chỉ bắt buộc khi team bật, có assignee và loại task không được miễn", () => {
    expect(isContractRequired(true, "general", true)).toBe(true);
    expect(isContractRequired(true, "delegation", true)).toBe(true);
    expect(isContractRequired(true, "escalation", true)).toBe(true);
    expect(isContractRequired(true, "escalation", false)).toBe(false);
    expect(isContractRequired(true, "request", true)).toBe(false);
    expect(isContractRequired(true, "note", true)).toBe(false);
    expect(isContractRequired(false, "general", true)).toBe(false);
  });
});

describe("validateContractForm", () => {
  it("báo thiếu ba trường chính khi bắt buộc", () => {
    const errs = validateContractForm(EMPTY_CONTRACT_FORM, true);
    expect(errs.map((e) => e.field)).toEqual(["objective", "deliverables", "acceptance"]);
  });

  it("không báo lỗi khi tùy chọn và form trống", () => {
    expect(validateContractForm(EMPTY_CONTRACT_FORM, false)).toEqual([]);
  });

  it("contract tùy chọn nhập dở vẫn phải đủ trường chính", () => {
    const errs = validateContractForm({ ...EMPTY_CONTRACT_FORM, objective: "Viết báo cáo" }, false);
    expect(errs.map((e) => e.field)).toEqual(["deliverables", "acceptance"]);
  });

  it("chặn quá 20 mục và số lần lặp không hợp lệ", () => {
    const many = Array.from({ length: 21 }, (_, i) => `mục ${i}`).join("\n");
    const errs = validateContractForm({
      ...EMPTY_CONTRACT_FORM, objective: "x", deliverables: "a", acceptance: many, maxIterations: "abc",
    }, true);
    expect(errs).toContainEqual({ field: "acceptance", code: "tooMany" });
    expect(errs).toContainEqual({ field: "maxIterations", code: "invalidNumber" });
  });
});

describe("buildContractInput", () => {
  it("tách dòng, bỏ dòng trống và chỉ gửi trường tùy chọn có giá trị", () => {
    expect(buildContractInput({
      ...EMPTY_CONTRACT_FORM,
      objective: " Tóm tắt doanh số Q3 ",
      deliverables: "report.md\n\n",
      acceptance: "Đủ 4 vùng\nCó nguồn",
      scopeOut: "Không dự báo",
      maxIterations: "15",
    })).toEqual({
      objective: "Tóm tắt doanh số Q3",
      deliverables: ["report.md"],
      acceptance: ["Đủ 4 vùng", "Có nguồn"],
      on_unclear: "ask",
      scope_out: ["Không dự báo"],
      limits: { max_iterations: 15 },
    });
  });

  it("trả undefined khi form trống", () => {
    expect(buildContractInput(EMPTY_CONTRACT_FORM)).toBeUndefined();
  });
});

describe("đọc metadata task", () => {
  it("đọc contract, checkpoint_pending và báo cáo nghiệm thu", () => {
    const task = {
      metadata: {
        contract: { objective: "o", deliverables: ["d"], acceptance: ["a1", "a2"], version: 2 },
        checkpoint_pending: "user" as const,
        acceptance_report: [{ index: 1, met: true, evidence: "e" }, { index: 2, met: false }],
      },
    };
    expect(getTaskContract(task)?.amendments).toEqual([]);
    expect(getCheckpointPending(task)).toBe("user");
    expect(getAcceptanceReport(task).get(2)?.met).toBe(false);
    expect(getTaskContract({ metadata: {} })).toBeNull();
    expect(getCheckpointPending({ metadata: { checkpoint_pending: "x" as never } })).toBeNull();
  });

  it("lấy câu hỏi clarify mới nhất của agent", () => {
    const q = getLatestClarifyQuestion([
      { id: "1", task_id: "t", agent_id: "a", content: "Question: cũ?", created_at: "2026-11-14T10:00:00Z" },
      { id: "2", task_id: "t", user_id: "u", content: "Question: của người", created_at: "2026-11-14T12:00:00Z" },
      { id: "3", task_id: "t", agent_id: "a", content: "Missing tool: web_fetch", created_at: "2026-11-14T11:00:00Z" },
      { id: "4", task_id: "t", agent_id: "a", content: "ghi chú khác", created_at: "2026-11-14T13:00:00Z" },
    ]);
    expect(q).toMatchObject({ kind: "missing_tool", text: "web_fetch" });
    expect(getLatestClarifyQuestion([])).toBeNull();
  });
});
