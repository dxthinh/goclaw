/**
 * Smoke test render tĩnh (react-dom/server) cho thẻ contract và khối phản hồi.
 * Repo chưa có @testing-library/react nên chỉ kiểm HTML sinh ra, không tương tác.
 * i18n được khởi tạo với tiếng Anh khi import, nên kiểm chuỗi tiếng Anh thật.
 */
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { TeamTaskData } from "@/types/team";
import { TaskDetailContract } from "./task-detail-contract";
import { TaskDetailFeedback, type TaskFeedbackActions } from "./task-detail-feedback";

const baseTask: TeamTaskData = { id: "t1", team_id: "team", subject: "s", status: "in_progress", priority: 0, task_number: 42 };

const actions: TaskFeedbackActions = {
  answerTask: vi.fn(async () => {}),
  requestTaskChanges: vi.fn(async () => {}),
  approveTask: vi.fn(async () => {}),
};

const feedback = (task: TeamTaskData) => renderToStaticMarkup(
  <TaskDetailFeedback task={task} teamId="team" comments={[]} actions={actions} resolveMember={() => undefined} onDone={() => {}} />,
);

describe("TaskDetailContract", () => {
  it("không hiện gì khi task không có contract", () => {
    expect(renderToStaticMarkup(<TaskDetailContract task={baseTask} />)).toBe("");
  });

  it("hiện tiêu chí kèm trạng thái bằng chữ và amendment", () => {
    const html = renderToStaticMarkup(<TaskDetailContract task={{
      ...baseTask,
      metadata: {
        contract: {
          objective: "Tóm tắt Q3", deliverables: ["report.md"], acceptance: ["Đủ 4 vùng", "Có nguồn", "Một trang"], version: 2,
          amendments: [{ version: 2, text: "Thêm vùng Bắc", by: "user", at: "2026-11-14T13:20:00Z", kind: "changes_requested" }],
        },
        acceptance_report: [{ index: 1, met: true, evidence: "đủ" }, { index: 2, met: false }],
      },
    }} />);
    expect(html).toContain("Tóm tắt Q3");
    expect(html).toContain("min-w-[600px]");
    expect(html).toContain(">Met<");
    expect(html).toContain("Not met");
    expect(html).toContain("Not reported");
    expect(html).toContain("Thêm vùng Bắc");
  });
});

describe("TaskDetailFeedback", () => {
  it("hiện ô trả lời khi task awaiting_input", () => {
    const html = feedback({ ...baseTask, status: "awaiting_input" });
    expect(html).toContain("Waiting for an answer");
    expect(html).toContain("Send answer");
  });

  it("chờ user: nút duyệt, yêu cầu sửa và gợi ý /approve", () => {
    const html = feedback({ ...baseTask, status: "in_review", metadata: { checkpoint_pending: "user" } });
    expect(html).toContain("/approve 42 in the original chat");
    expect(html).toContain("Approve result");
    expect(html).toContain("Request changes");
  });

  it("chờ lead: không có gợi ý /approve", () => {
    const html = feedback({ ...baseTask, status: "in_review", metadata: { checkpoint_pending: "lead" } });
    expect(html).toContain("Approve the result or request changes");
    expect(html).not.toContain("/approve");
  });

  it("in_review không có checkpoint_pending và trạng thái khác thì không hiện", () => {
    expect(feedback({ ...baseTask, status: "in_review" })).toBe("");
    expect(feedback(baseTask)).toBe("");
  });
});
