import type {
  AcceptanceReportItem, TaskCheckpointPending, TaskContract, TaskContractInput,
  TeamTaskComment, TeamTaskData,
} from "@/types/team";

// Giới hạn kích thước contract, khớp internal/tools/team_task_contract.go.
export const CONTRACT_MAX_STRING = 2000;
export const CONTRACT_MAX_ITEMS = 20;

// Loại task miễn contract (khớp tools.TaskContractRequired).
const CONTRACT_EXEMPT_TYPES = new Set(["request", "note"]);

/**
 * Contract có bắt buộc khi tạo task không: team bật task_contract, task được
 * giao ngay (assignTo) và task_type không phải request/note. Task backlog chưa
 * giao tạo được không cần contract; backend chặn ở bước giao.
 */
export function isContractRequired(enabled: boolean, taskType: string, assigning: boolean): boolean {
  return enabled && assigning && !CONTRACT_EXEMPT_TYPES.has(taskType || "general");
}

/** Contract của task, null khi metadata không có contract hợp lệ tối thiểu. */
export function getTaskContract(task: Pick<TeamTaskData, "metadata"> | null | undefined): TaskContract | null {
  const c = task?.metadata?.contract;
  if (!c || typeof c !== "object" || typeof c.objective !== "string") return null;
  return {
    ...c,
    deliverables: Array.isArray(c.deliverables) ? c.deliverables : [],
    acceptance: Array.isArray(c.acceptance) ? c.acceptance : [],
    amendments: Array.isArray(c.amendments) ? c.amendments : [],
  };
}

/** Ai đang được chờ duyệt (metadata.checkpoint_pending), null khi không chờ. */
export function getCheckpointPending(task: Pick<TeamTaskData, "metadata"> | null | undefined): TaskCheckpointPending | null {
  const v = task?.metadata?.checkpoint_pending;
  return v === "user" || v === "lead" ? v : null;
}

/** Báo cáo nghiệm thu theo index (đếm từ 1). */
export function getAcceptanceReport(task: Pick<TeamTaskData, "metadata"> | null | undefined): Map<number, AcceptanceReportItem> {
  const out = new Map<number, AcceptanceReportItem>();
  const raw = task?.metadata?.acceptance_report;
  if (!Array.isArray(raw)) return out;
  for (const item of raw) {
    if (item && typeof item.index === "number") out.set(item.index, item);
  }
  return out;
}

// Tiền tố comment mà tool clarify ghi (internal/tools/team_tasks_clarify.go).
const CLARIFY_PREFIXES = [
  { prefix: "Question: ", kind: "question" as const },
  { prefix: "Missing tool: ", kind: "missing_tool" as const },
];

export interface ClarifyQuestion {
  kind: "question" | "missing_tool";
  text: string;
  agentId?: string;
  createdAt: string;
}

/** Câu hỏi clarify mới nhất của member (comment của agent có tiền tố clarify). */
export function getLatestClarifyQuestion(comments: TeamTaskComment[]): ClarifyQuestion | null {
  let latest: ClarifyQuestion | null = null;
  for (const c of comments) {
    if (!c.agent_id) continue;
    const match = CLARIFY_PREFIXES.find((p) => c.content.startsWith(p.prefix));
    if (!match) continue;
    if (latest && c.created_at < latest.createdAt) continue;
    latest = { kind: match.kind, text: c.content.slice(match.prefix.length), agentId: c.agent_id, createdAt: c.created_at };
  }
  return latest;
}

/* ── Form contract ────────────────────────────────────────────── */

/** Trạng thái form contract; các trường danh sách là văn bản, mỗi dòng một mục. */
export interface ContractFormState {
  objective: string;
  deliverables: string;
  acceptance: string;
  scopeIn: string;
  scopeOut: string;
  inputs: string;
  needs: string;
  onUnclear: "ask" | "assume_and_note";
  maxIterations: string;
}

export const EMPTY_CONTRACT_FORM: ContractFormState = {
  objective: "", deliverables: "", acceptance: "", scopeIn: "", scopeOut: "",
  inputs: "", needs: "", onUnclear: "ask", maxIterations: "",
};

/** Tách văn bản thành danh sách, mỗi dòng một mục, bỏ dòng trống. */
export function splitLines(text: string): string[] {
  return text.split("\n").map((s) => s.trim()).filter(Boolean);
}

/** Form chưa nhập gì (bỏ qua on_unclear vì luôn có giá trị mặc định). */
export function isContractFormEmpty(f: ContractFormState): boolean {
  return [f.objective, f.deliverables, f.acceptance, f.scopeIn, f.scopeOut, f.inputs, f.needs, f.maxIterations]
    .every((v) => v.trim() === "");
}

export type ContractFormError =
  | { field: "objective" | "deliverables" | "acceptance"; code: "required" }
  | { field: keyof ContractFormState; code: "tooLong" | "tooMany" | "invalidNumber" };

/**
 * Kiểm form contract trước khi gửi. Contract bắt buộc thì ba trường chính phải
 * có; contract tùy chọn mà đã nhập dở cũng phải đủ ba trường, vì backend luôn
 * kiểm contract có mặt.
 */
export function validateContractForm(f: ContractFormState, required: boolean): ContractFormError[] {
  const errors: ContractFormError[] = [];
  if (!required && isContractFormEmpty(f)) return errors;
  if (!f.objective.trim()) errors.push({ field: "objective", code: "required" });
  if (splitLines(f.deliverables).length === 0) errors.push({ field: "deliverables", code: "required" });
  if (splitLines(f.acceptance).length === 0) errors.push({ field: "acceptance", code: "required" });
  if (f.objective.trim().length > CONTRACT_MAX_STRING) errors.push({ field: "objective", code: "tooLong" });
  const lists: (keyof ContractFormState)[] = ["deliverables", "acceptance", "scopeIn", "scopeOut", "inputs", "needs"];
  for (const field of lists) {
    const items = splitLines(f[field] as string);
    if (items.length > CONTRACT_MAX_ITEMS) errors.push({ field, code: "tooMany" });
    if (items.some((s) => s.length > CONTRACT_MAX_STRING)) errors.push({ field, code: "tooLong" });
  }
  const mi = f.maxIterations.trim();
  if (mi && !(/^\d+$/.test(mi) && Number(mi) > 0)) errors.push({ field: "maxIterations", code: "invalidNumber" });
  return errors;
}

/** Dựng contract gửi lên teams.tasks.create; undefined khi form trống. */
export function buildContractInput(f: ContractFormState): TaskContractInput | undefined {
  if (isContractFormEmpty(f)) return undefined;
  const out: TaskContractInput = {
    objective: f.objective.trim(),
    deliverables: splitLines(f.deliverables),
    acceptance: splitLines(f.acceptance),
    on_unclear: f.onUnclear,
  };
  const optional: [keyof TaskContractInput, string][] = [
    ["scope_in", f.scopeIn], ["scope_out", f.scopeOut], ["inputs", f.inputs], ["needs", f.needs],
  ];
  for (const [key, text] of optional) {
    const items = splitLines(text);
    if (items.length > 0) (out as Record<string, unknown>)[key] = items;
  }
  const mi = Number(f.maxIterations.trim());
  if (f.maxIterations.trim() && Number.isInteger(mi) && mi > 0) out.limits = { max_iterations: mi };
  return out;
}
